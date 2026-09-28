package elaborate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/testutil"
	"github.com/waj/fango/internal/types"
)

func TestGoldens(t *testing.T) {
	files := testutil.GlobFango(t, filepath.Join("..", "..", "testdata", "core"))
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f := source.NewFile(filepath.Base(path), content)
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
			ck := infer.NewChecker(sup, b, infer.NewEnv())
			staging.Install(ck)
			if errs := ck.InstallPrelude(); len(errs) > 0 {
				t.Fatalf("prelude errors: %v", errs)
			}
			// Module loading normally groups operator runs; these tests parse
			// directly, so they group against the prelude's table themselves.
			if errs := ck.Fixity.Resolve(m); len(errs) > 0 {
				t.Fatalf("fixity errors: %v", errs)
			}
			infos, inferErrs := ck.Module(m)
			if len(inferErrs) > 0 {
				t.Fatalf("infer errors: %v", inferErrs)
			}
			prog, elabErrs := elaborate.Module(infos, ck)
			if len(elabErrs) > 0 {
				t.Fatalf("elaborate errors: %v", elabErrs)
			}
			// The Core linter runs on every elaborated program in tests.
			if lintErrs := core.Lint(prog, b); len(lintErrs) > 0 {
				t.Fatalf("core lint: %v", lintErrs)
			}
			testutil.Golden(t, strings.TrimSuffix(path, ".fango")+".core", core.Dump(fixtureProgram(prog, ck.PreludeOwners)))
		})
	}
}

func TestEffectfulCallbackRetainsItsEffect(t *testing.T) {
	src := `effect Borrow
    read : () -> Int

identity : (() ->{e} Int) -> (() ->{e} Int)
identity action = action

saved =
    handle identity { _ -> read() } of
        read () -> resume 1

main = 0
`
	f := source.NewFile("<capture>", []byte(src))
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
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	staging.Install(ck)
	if errs := ck.InstallPrelude(); len(errs) > 0 {
		t.Fatalf("prelude errors: %v", errs)
	}
	if errs := ck.Fixity.Resolve(m); len(errs) > 0 {
		t.Fatalf("fixity errors: %v", errs)
	}
	infos, inferErrs := ck.Module(m)
	if len(inferErrs) > 0 {
		t.Fatalf("infer errors: %v", inferErrs)
	}
	if !ck.MarkEffectScoped("Borrow") {
		t.Fatal("Borrow effect was not installed")
	}
	prog, elabErrs := elaborate.Module(infos, ck)
	if len(elabErrs) != 0 {
		t.Fatal(elabErrs)
	}
	for _, d := range prog.Defs {
		if d.Name == "saved" {
			fn, ok := d.Type.(*types.TFun)
			if !ok || len(fn.Eff.Labels) == 0 {
				t.Fatal("returned callback lost its effect")
			}
		}
	}

}

// Keep fixture dumps focused on their source. The complete executable prelude
// is still elaborated and linted above, including all dictionary definitions.
func fixtureProgram(prog *core.Prog, preludeOwners map[string]bool) *core.Prog {
	result := *prog
	result.Defs = nil
	result.ADTs = nil
	for _, d := range prog.Defs {
		if !preludeOwners[d.Owner] {
			result.Defs = append(result.Defs, d)
		}
	}
	for _, a := range prog.ADTs {
		owner, _, qualified := strings.Cut(a.Con.Name, ".")
		if !qualified || !preludeOwners[owner] {
			result.ADTs = append(result.ADTs, a)
		}
	}
	return &result
}
