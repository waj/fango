package fixity

import (
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
)

// chainOf builds the flat run `a op b op c …` over single-letter variables,
// which is what the parser hands the pass.
func chainOf(ops ...string) *ast.OpChain {
	names := "abcdefgh"
	c := &ast.OpChain{Operands: []ast.Expr{&ast.Var{Name: names[0:1]}}}
	for i, op := range ops {
		c.Ops = append(c.Ops, ast.OpRef{Op: op})
		c.Operands = append(c.Operands, &ast.Var{Name: names[i+1 : i+2]})
	}
	return c
}

func table(entries map[string]Fixity) Table {
	t := Builtin()
	for op, f := range entries {
		t[op] = f
	}
	return t
}

func TestGrouping(t *testing.T) {
	std := table(map[string]Fixity{
		"*":  {7, ast.AssocLeft},
		"/":  {7, ast.AssocLeft},
		"+":  {6, ast.AssocLeft},
		"-":  {6, ast.AssocLeft},
		"++": {5, ast.AssocRight},
		"==": {4, ast.AssocNone},
		"<":  {4, ast.AssocNone},
	})
	tests := []struct {
		ops  []string
		want string
	}{
		{[]string{"+"}, "(binop + (var a) (var b))"},
		{[]string{"+", "+"}, "(binop + (binop + (var a) (var b)) (var c))"},
		{[]string{"+", "*"}, "(binop + (var a) (binop * (var b) (var c)))"},
		{[]string{"*", "+"}, "(binop + (binop * (var a) (var b)) (var c))"},
		{[]string{"+", "-", "+"}, "(binop + (binop - (binop + (var a) (var b)) (var c)) (var d))"},
		{[]string{"++", "++"}, "(binop ++ (var a) (binop ++ (var b) (var c)))"},
		{[]string{"++", "+"}, "(binop ++ (var a) (binop + (var b) (var c)))"},
		{[]string{"+", "++"}, "(binop ++ (binop + (var a) (var b)) (var c))"},
		{[]string{"*", "/", "*"}, "(binop * (binop / (binop * (var a) (var b)) (var c)) (var d))"},
		{[]string{"==", "+"}, "(binop == (var a) (binop + (var b) (var c)))"},
		// Undeclared operators take the infixl 9 default, tighter than all.
		{[]string{"<+>", "+"}, "(binop + (binop <+> (var a) (var b)) (var c))"},
		{[]string{"+", "<+>"}, "(binop + (var a) (binop <+> (var b) (var c)))"},
		// Short-circuit operators keep their built-in fixity.
		{[]string{"&&", "||"}, "(binop || (binop && (var a) (var b)) (var c))"},
		{[]string{"||", "&&"}, "(binop || (var a) (binop && (var b) (var c)))"},
		{[]string{"==", "&&"}, "(binop && (binop == (var a) (var b)) (var c))"},
	}
	for _, tc := range tests {
		r := &resolver{t: std}
		got := ast.DumpExpr(r.chain(chainOf(tc.ops...)))
		if got != tc.want {
			t.Errorf("chain %v:\n got %s\nwant %s", tc.ops, got, tc.want)
		}
		if len(r.errs) > 0 {
			t.Errorf("chain %v: unexpected error %s", tc.ops, r.errs[0].Body)
		}
	}
}

func TestGroupingErrors(t *testing.T) {
	std := table(map[string]Fixity{
		"==": {4, ast.AssocNone},
		"<":  {4, ast.AssocNone},
		"++": {5, ast.AssocRight},
		"<>": {5, ast.AssocLeft},
	})
	tests := []struct {
		ops  []string
		want string
	}{
		{[]string{"<", "<"}, "does not associate"},
		{[]string{"==", "<"}, "share an associativity"},
		{[]string{"++", "<>"}, "share an associativity"},
		// Non-adjacent same-level operators still collide once grouped.
		{[]string{"==", "++", "=="}, "does not associate"},
	}
	for _, tc := range tests {
		r := &resolver{t: std}
		e := r.chain(chainOf(tc.ops...))
		if len(r.errs) == 0 {
			t.Errorf("chain %v: expected an error, got %s", tc.ops, ast.DumpExpr(e))
			continue
		}
		if body := r.errs[0].Body; !contains(body, tc.want) {
			t.Errorf("chain %v: error %q does not mention %q", tc.ops, body, tc.want)
		}
		// Recovery must still collapse the chain.
		if _, unresolved := e.(*ast.OpChain); unresolved {
			t.Errorf("chain %v: recovery left an unresolved chain", tc.ops)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestAddConflict(t *testing.T) {
	tab := Builtin()
	decl := func(op string, a ast.Assoc, p int) *ast.FixityDecl {
		return &ast.FixityDecl{Op: op, Assoc: a, Prec: p, OpSpan: source.Span{}}
	}
	if errs := tab.Add(decl("<+>", ast.AssocLeft, 6)); len(errs) > 0 {
		t.Fatalf("first declaration: %v", errs[0].Body)
	}
	if errs := tab.Add(decl("<+>", ast.AssocLeft, 6)); len(errs) > 0 {
		t.Errorf("re-declaring the same fixity should be allowed: %v", errs[0].Body)
	}
	if errs := tab.Add(decl("<+>", ast.AssocRight, 6)); len(errs) != 1 || errs[0].Title != "CONFLICTING FIXITY" {
		t.Errorf("conflicting fixity was not reported: %v", errs)
	}
	if errs := tab.Add(decl("&&", ast.AssocLeft, 3)); len(errs) != 1 || errs[0].Title != "RESERVED OPERATOR" {
		t.Errorf("fixity for (&&) was not rejected: %v", errs)
	}
}

func TestDefaultFixity(t *testing.T) {
	f := Builtin().Lookup("<?>")
	if f.Prec != DefaultPrec || f.Assoc != DefaultAssoc {
		t.Errorf("undeclared operator got %v %d, want %v %d", f.Assoc, f.Prec, DefaultAssoc, DefaultPrec)
	}
}
