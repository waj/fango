package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/testutil"
)

func TestGoldens(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "parse", "*.fango"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no testdata/parse/*.fango files")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f := source.NewFile(filepath.Base(path), content)
			toks, lexErrs := lexer.Lex(f)
			if len(lexErrs) > 0 {
				t.Fatalf("unexpected lex errors: %v", lexErrs)
			}
			m, errs := Parse(toks, f)
			out := ast.Dump(m)
			if len(errs) > 0 {
				out += "-- errors --\n" + testutil.DumpErrors(errs)
			}
			testutil.Golden(t, strings.TrimSuffix(path, ".fango")+".ast", out)
		})
	}
}

func TestParseExprInput(t *testing.T) {
	f := source.NewFile("<repl>", []byte("(1 +\n   2) * 3"))
	toks, _ := lexer.Lex(f)
	e, errs := ParseExprInput(toks, f)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	got := ast.DumpExpr(e)
	want := "(binop * (binop + (int 1) (int 2)) (int 3))"
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestParseExprInputUnfinished(t *testing.T) {
	for _, src := range []string{"1 +", "(1 + 2"} {
		f := source.NewFile("<repl>", []byte(src))
		toks, _ := lexer.Lex(f)
		_, errs := ParseExprInput(toks, f)
		if len(errs) == 0 || errs[len(errs)-1].Title != TitleUnexpectedEOF {
			t.Errorf("%q: expected %s error, got %v", src, TitleUnexpectedEOF, errs)
		}
	}
}
