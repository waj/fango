package infer_test

import (
	"fmt"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/staging"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/testutil"
	"github.com/waj/fango/internal/types"
)

// infer.Checker goldens include the inferred declarations and diagnostics.
func TestCheckerGoldens(t *testing.T) {
	files := testutil.GlobFango(t, filepath.Join("..", "..", "testdata", "check"))
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
			infos, errs := ck.Module(m)
			var out strings.Builder
			for _, info := range infos {
				fmt.Fprintf(&out, "%s : %s\n", info.Name, types.ShowScheme(checkedScheme(ck, info)))
			}
			if len(errs) > 0 {
				out.WriteString("-- errors --\n" + testutil.DumpErrors(errs))
			}
			testutil.Golden(t, strings.TrimSuffix(path, ".fango")+".types", out.String())
		})
	}
}
