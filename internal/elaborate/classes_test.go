package elaborate_test

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
	"testing"
)

func TestClassEvidence(t *testing.T) {
	src := `class Twice a
    twice : a -> a

instance Twice Int
    twice x = x + x

applyTwice : Twice a => a -> a
applyTwice x = twice x

main : Int
main = applyTwice 21
`
	f := source.NewFile("classes.fango", []byte(src))
	ts, errs := lexer.Lex(f)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	m, errs := parser.Parse(ts, f)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	staging.Install(ck)
	if errs = ck.InstallPrelude(); len(errs) > 0 {
		t.Fatal(errs)
	}
	// Module loading normally groups operator runs; these tests parse
	// directly, so they group against the prelude's table themselves.
	if errs := ck.Fixity.Resolve(m); len(errs) > 0 {
		t.Fatalf("fixity errors: %v", errs)
	}
	infos, errs := ck.Module(m)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	p, errs := elaborate.Module(infos, ck)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if es := core.Lint(p, b); len(es) > 0 {
		for _, e := range es {
			t.Error(e)
		}
	}
	// A dictionary is typed evidence, not an unchecked runtime tag.
	for i := range p.Defs {
		if p.Defs[i].Name == "main" {
			app := p.Defs[i].Body.(*core.App)
			app.Args[0] = &core.IntLit{Val: 0, Ty: b.Int}
		}
	}
	if es := core.Lint(p, b); len(es) == 0 {
		t.Fatal("Core linter accepted an Int in place of class evidence")
	}
}
