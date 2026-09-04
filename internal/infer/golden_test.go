package infer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/testutil"
	"github.com/waj/fango/internal/types"
)

// Checker goldens include the inferred declarations and the Rule 3 choices;
// unlike Core goldens, they expose a force before row erasure.
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
			ck := NewChecker(sup, b, NewEnv())
			infos, errs := ck.Module(m)
			var out strings.Builder
			for _, info := range infos {
				fmt.Fprintf(&out, "%s : %s\n", info.Name, types.Show(ck.Sub.Apply(info.Type)))
			}
			type choice struct {
				line, col int
				text      string
			}
			var choices []choice
			for e, raw := range ck.ForceTypes {
				p := e.Span().StartPos()
				comp := ck.Sub.Apply(raw).(*types.TFun)
				choices = append(choices, choice{p.Line, p.Col, fmt.Sprintf("force %d:%d : %s => %s", p.Line, p.Col, types.Show(comp), types.Show(ck.Sub.Apply(comp.Ret)))})
			}
			for e, raw := range ck.DelayedApps {
				p := e.Span().StartPos()
				choices = append(choices, choice{p.Line, p.Col, fmt.Sprintf("delay %d:%d : %s", p.Line, p.Col, types.Show(ck.Sub.Apply(raw)))})
			}
			sort.Slice(choices, func(i, j int) bool {
				if choices[i].line != choices[j].line {
					return choices[i].line < choices[j].line
				}
				return choices[i].col < choices[j].col
			})
			for _, c := range choices {
				out.WriteString(c.text + "\n")
			}
			if len(errs) > 0 {
				out.WriteString("-- errors --\n" + testutil.DumpErrors(errs))
			}
			testutil.Golden(t, strings.TrimSuffix(path, ".fango")+".types", out.String())
		})
	}
}
