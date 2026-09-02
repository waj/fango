package infer

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

func check(t *testing.T, src string) (*Checker, []DeclInfo, []error) {
	t.Helper()
	f := source.NewFile("<test>", []byte(src))
	toks, lexErrs := lexer.Lex(f)
	if len(lexErrs) > 0 {
		t.Fatalf("lex errors: %v", lexErrs)
	}
	m, parseErrs := parser.Parse(toks, f)
	if len(parseErrs) > 0 {
		t.Fatalf("parse errors: %v", parseErrs)
	}
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := NewChecker(sup, b, NewEnv())
	infos, errs := ck.Module(m)
	var out []error
	for _, e := range errs {
		out = append(out, checkErr{e.Title, e.Span.StartPos().Line})
	}
	return ck, infos, out
}

type checkErr struct {
	title string
	line  int
}

func (e checkErr) Error() string { return e.title }

func TestPositive(t *testing.T) {
	cases := []struct {
		src  string
		want string // "name : type" per decl, comma-separated, zonked
	}{
		{"x = 1", "x : number"},
		{"x = 1 + 2 * 3", "x : number"},
		{"x = 40\ny = x + 2", "x : number, y : number"},
		{"x = 1\ny = x\nmain = y - x", "x : number, y : number, main : number"},
	}
	for _, c := range cases {
		ck, infos, errs := check(t, c.src)
		if len(errs) > 0 {
			t.Errorf("%q: unexpected errors %v", c.src, errs)
			continue
		}
		var parts []string
		p := types.NewPrinter()
		for _, info := range infos {
			parts = append(parts, info.Name+" : "+p.Type(ck.Sub.Apply(info.Type)))
		}
		if got := strings.Join(parts, ", "); got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

func TestNegative(t *testing.T) {
	cases := []struct {
		src       string
		wantTitle string
		wantLine  int
	}{
		{"x = y", "NAMING ERROR", 1},
		{"x = 1\nx = 2", "MULTIPLE DEFINITIONS", 2},
		{"x = z + 1\nmain = x", "NAMING ERROR", 1},
		// Use-before-define is a naming error: source-order scoping.
		{"main = x\nx = 1", "NAMING ERROR", 1},
	}
	for _, c := range cases {
		_, _, errs := check(t, c.src)
		if len(errs) == 0 {
			t.Errorf("%q: expected an error", c.src)
			continue
		}
		e := errs[0].(checkErr)
		if e.title != c.wantTitle || e.line != c.wantLine {
			t.Errorf("%q: got %s at line %d, want %s at line %d",
				c.src, e.title, e.line, c.wantTitle, c.wantLine)
		}
	}
}

// Direct unifier tests for paths S0 surface syntax cannot reach yet:
// Number-kind rejection and the occurs check.
func TestUnifyNumberKind(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	sub := Subst{}

	n := sup.FreshVar(types.Number)
	if m := unify(n, b.Int, sub, b); m != nil {
		t.Errorf("number ~ Int should unify: %v", m.note)
	}

	n2 := sup.FreshVar(types.Number)
	if m := unify(n2, b.String, sub, b); m == nil {
		t.Error("number ~ String should fail")
	}

	// A general var unified with a number var must keep the Number kind.
	n3 := sup.FreshVar(types.Number)
	g := sup.FreshVar(types.General)
	if m := unify(g, n3, sub, b); m != nil {
		t.Fatalf("general ~ number should unify")
	}
	if m := unify(g, b.Bool, sub, b); m == nil {
		t.Error("after merging with a number var, Bool should be rejected")
	}
}

func TestUnifyOccurs(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	sub := Subst{}
	v := sup.FreshVar(types.General)
	fn := &types.TFun{Arg: v, Ret: b.Int}
	if m := unify(v, fn, sub, b); m == nil {
		t.Error("occurs check should reject v ~ (v -> Int)")
	}
}

func TestUnifyTConIdentityIsUnique(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	sub := Subst{}
	// Same name, different unique — a redefined REPL type must not unify.
	otherInt := &types.TCon{Unique: sup.NextUnique(), Name: "Int"}
	if m := unify(b.Int, otherInt, sub, b); m == nil {
		t.Error("TCons with equal names but different uniques must not unify")
	}
}
