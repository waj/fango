package elaborate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
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
			infos, inferErrs := ck.Module(m)
			if len(inferErrs) > 0 {
				t.Fatalf("infer errors: %v", inferErrs)
			}
			prog, elabErrs := Module(infos, ck)
			if len(elabErrs) > 0 {
				t.Fatalf("elaborate errors: %v", elabErrs)
			}
			// The Core linter runs on every elaborated program in tests.
			if lintErrs := core.Lint(prog, b); len(lintErrs) > 0 {
				t.Fatalf("core lint: %v", lintErrs)
			}
			testutil.Golden(t, strings.TrimSuffix(path, ".fango")+".core", core.Dump(prog))
		})
	}
}
