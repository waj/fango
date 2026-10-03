package elaborate_test

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
)

// Equation groups lower to hidden worker parameters plus one decision tree at
// the innermost application; the goldens in testdata/core pin the exact
// shapes. These assert the invariants that are easiest to state directly.

func TestSingleIdentifierEquationKeepsSourceParameters(t *testing.T) {
	prog := elabPoly(t, "double : Int -> Int\ndouble n = n + n\nmain = double 2\n")
	def := findDef(t, prog, "double")
	if len(def.Params) != 1 || def.Params[0] != "n" {
		t.Fatalf("params = %v, want the source name [n]", def.Params)
	}
	if _, isCase := def.Body.(*core.Case); isCase {
		t.Fatalf("an identifier-only equation should not dispatch:\n%s", core.DumpExpr(def.Body))
	}
}

func TestEquationGroupBindsHiddenParameters(t *testing.T) {
	prog := elabPoly(t, `type Choice = Yes | No
both : Choice -> Choice -> Int
both Yes Yes = 3
both Yes No = 2
both No other = 0
main = both Yes No
`)
	def := findDef(t, prog, "both")
	for _, p := range def.Params {
		if !strings.HasPrefix(p, "_arg") {
			t.Fatalf("params = %v, want deterministic hidden names", def.Params)
		}
	}
	// One tree covers both columns: the outer switch tests the first
	// parameter and the inner switches test the second.
	c, ok := def.Body.(*core.Case)
	if !ok {
		t.Fatalf("body is %T, want a Case:\n%s", def.Body, core.DumpExpr(def.Body))
	}
	outer, ok := c.Tree.(*core.SwitchCtor)
	if !ok {
		t.Fatalf("tree root is %T, want a SwitchCtor", c.Tree)
	}
	if outer.Scrut != c.Bind {
		t.Fatalf("root tests %q, want the Case binder %q", outer.Scrut, c.Bind)
	}
	if !core.TreeMentions(c.Tree, def.Params[1]) {
		t.Fatalf("tree never tests the second column:\n%s", core.DumpExpr(def.Body))
	}
}

func TestFirstMatchOrderIsSourceOrder(t *testing.T) {
	prog := elabPoly(t, `grade : Int -> String
grade 0 = "zero"
grade 1 = "one"
grade _ = "many"
main = grade 1
`)
	def := findDef(t, prog, "grade")
	c, ok := def.Body.(*core.Case)
	if !ok {
		t.Fatalf("body is %T, want a Case", def.Body)
	}
	sw, ok := c.Tree.(*core.SwitchLit)
	if !ok {
		t.Fatalf("tree root is %T, want a SwitchLit", c.Tree)
	}
	var got []string
	for _, lit := range sw.Cases {
		got = append(got, core.DumpExpr(lit.Lit))
	}
	if len(got) != 2 || !strings.Contains(got[0], "0") || !strings.Contains(got[1], "1") {
		t.Fatalf("literal cases = %v, want source order 0 then 1", got)
	}
}

// A top-level destructuring group evaluates its right-hand side once: the
// subject is a definition of its own and every binder projects out of it.
func TestTopLevelPatternBindingSharesOneSubject(t *testing.T) {
	prog := elabPoly(t, `type Pair = Pair Int Int
Pair leftCount rightCount = Pair 1 2
main = leftCount + rightCount
`)
	var subject string
	for _, d := range prog.Defs {
		if strings.HasPrefix(d.Name, "_pattern_") {
			if subject != "" {
				t.Fatalf("more than one shared subject: %s and %s", subject, d.Name)
			}
			subject = d.Name
		}
	}
	if subject == "" {
		t.Fatalf("no shared subject definition:\n%s", core.Dump(prog))
	}
	for _, name := range []string{"leftCount", "rightCount"} {
		def := findDef(t, prog, name)
		if !core.Mentions(def.Body, subject) {
			t.Fatalf("`%s` does not project out of %s:\n%s", name, subject, core.DumpExpr(def.Body))
		}
	}
}

// Grouping is a surface notion: each operation still lowers to exactly one
// Core handler clause, and `return` to one Core return clause.
func TestHandlerGroupsKeepCoreClauseCardinality(t *testing.T) {
	prog := elabPoly(t, `type Choice = Yes | No
effect Ask
    ask : Choice -> Int
main =
    handle ask Yes on
        ask Yes -> resume 1
        ask No -> resume 0
        return 1 -> Yes
        return _ -> No
`)
	def := findDef(t, prog, "main")
	var handles []*core.Handle
	var walk func(core.Expr)
	walk = func(e core.Expr) {
		switch e := e.(type) {
		case *core.Handle:
			handles = append(handles, e)
		case *core.Let:
			walk(e.Rhs)
			walk(e.Body)
		case *core.Seq:
			walk(e.First)
			walk(e.Then)
		}
	}
	walk(def.Body)
	if len(handles) != 1 {
		t.Fatalf("found %d handles, want 1:\n%s", len(handles), core.DumpExpr(def.Body))
	}
	if len(handles[0].Clauses) != 1 {
		t.Fatalf("handler has %d Core clauses, want one per operation", len(handles[0].Clauses))
	}
	if handles[0].Return == nil {
		t.Fatal("the return group did not lower to a Core return clause")
	}
}

func findDef(t *testing.T, prog *core.Prog, name string) *core.Def {
	t.Helper()
	for i := range prog.Defs {
		if prog.Defs[i].Name == name {
			return &prog.Defs[i]
		}
	}
	t.Fatalf("program has no definition `%s`:\n%s", name, core.Dump(prog))
	return nil
}
