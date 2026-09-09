package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/fixity"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/testutil"
)

func TestGoldens(t *testing.T) {
	files := testutil.GlobFango(t, filepath.Join("..", "..", "testdata", "parse"))
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
			// Grouping is internal/fixity's job, and a flat chain is not
			// what any later phase sees, so the goldens record the grouped
			// tree. Each fixture declares the fixities it depends on, which
			// keeps what it is testing readable in the fixture itself.
			table := fixity.Builtin()
			errs = append(errs, table.Collect(m.Decls)...)
			errs = append(errs, table.Resolve(m)...)
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
	// A prompt entry is parsed on its own, so the caller groups it — the
	// REPL does the same against its session table.
	e, errs = fixity.Builtin().ResolveExpr(e)
	if len(errs) > 0 {
		t.Fatalf("unexpected grouping errors: %v", errs)
	}
	got := ast.DumpExpr(e)
	want := "(binop * (binop + (int 1) (int 2)) (int 3))"
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestWithRemainsAnOrdinaryIdentifier(t *testing.T) {
	f := source.NewFile("<repl>", []byte("with 1"))
	toks, lexErrs := lexer.Lex(f)
	if len(lexErrs) > 0 {
		t.Fatal(lexErrs)
	}
	e, errs := ParseExprInput(toks, f)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got, want := ast.DumpExpr(e), "(app (var with) (int 1))"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestParseExprInputUnfinished(t *testing.T) {
	for _, src := range []string{"1 +", "(1 + 2", "[1, 2", "[head |"} {
		f := source.NewFile("<repl>", []byte(src))
		toks, _ := lexer.Lex(f)
		_, errs := ParseExprInput(toks, f)
		if len(errs) == 0 || errs[len(errs)-1].Title != TitleUnexpectedEOF {
			t.Errorf("%q: expected %s error, got %v", src, TitleUnexpectedEOF, errs)
		}
	}
}

func TestMalformedLists(t *testing.T) {
	for _, src := range []string{"[1,]", "[1, | xs]", "[1 | ]", "[1 | xs | ys]"} {
		f := source.NewFile("<test>", []byte(src))
		toks, _ := lexer.Lex(f)
		_, errs := ParseExprInput(toks, f)
		if len(errs) == 0 {
			t.Errorf("%q: expected a syntax error", src)
		}
	}
	for _, src := range []string{
		"main xs = case xs of\n  [x,] -> x\n  _ -> 0",
		"main xs = case xs of\n  [x | ] -> x\n  _ -> 0",
	} {
		f := source.NewFile("<test>", []byte(src))
		toks, _ := lexer.Lex(f)
		_, errs := Parse(toks, f)
		if len(errs) == 0 {
			t.Errorf("%q: expected a syntax error", src)
		}
	}
}

func TestParseReflectionAndDeriver(t *testing.T) {
	f := source.NewFile("<test>", []byte("deriver Show\n    show info value = quote value\nx = typeOf (List Int)\n"))
	toks, lexErrs := lexer.Lex(f)
	if len(lexErrs) > 0 {
		t.Fatal(lexErrs)
	}
	m, errs := Parse(toks, f)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	got := ast.Dump(m)
	if !strings.Contains(got, "(deriver Show") || !strings.Contains(got, "(typeOf (List Int))") {
		t.Fatalf("unexpected dump:\n%s", got)
	}
}
