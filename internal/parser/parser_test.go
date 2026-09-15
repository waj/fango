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

func TestTrailingLambdaNeedsParameterAndBody(t *testing.T) {
	for _, src := range []string{"apply \\ -> 1", "apply \\x ->", "[apply \\x ->, 2]"} {
		f := source.NewFile("<test>", []byte(src))
		toks, _ := lexer.Lex(f)
		_, errs := ParseExprInput(toks, f)
		if len(errs) == 0 {
			t.Fatalf("accepted %q", src)
		}
	}
}

func TestMalformedSemicolonBlocks(t *testing.T) {
	for _, src := range []string{
		"main = ; 1",
		"main = print 1;; 2",
		"main = print 1;",
		"main = x = 1",
	} {
		f := source.NewFile("<test>", []byte(src))
		toks, _ := lexer.Lex(f)
		_, errs := Parse(toks, f)
		if len(errs) == 0 {
			t.Errorf("accepted %q", src)
		}
	}
}

func TestInlineBodyClassificationStopsAtNestedDelimiters(t *testing.T) {
	src := "main =\n" +
		"    handle keep (\\_ -> readCounter()) with state = 0 of\n" +
		"        keepValue value -> resume value with state\n"
	f := source.NewFile("<test>", []byte(src))
	toks, lexErrs := lexer.Lex(f)
	if len(lexErrs) > 0 {
		t.Fatal(lexErrs)
	}
	_, errs := Parse(toks, f)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

// Delimiters inside an open tuple are punctuation, not sibling layout items.
// In particular they may align with the tuple opener when that opener begins
// a block expression or a case-branch pattern.
func TestDelimiterAlignedTuplesAtLayoutAnchor(t *testing.T) {
	src := "value =\n    ( first\n    , second\n    )\n\n" +
		"match value =\n    case value of\n        ( first\n        , second\n        ) -> first\n\n" +
		"typed :\n    ( Int\n    , String\n    ) -> ()\ntyped value = ()\n"
	f := source.NewFile("<test>", []byte(src))
	toks, lexErrs := lexer.Lex(f)
	if len(lexErrs) > 0 {
		t.Fatal(lexErrs)
	}
	_, errs := Parse(toks, f)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
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

// A `{` in expression position opens either an inferred record literal or a
// record update, and one token of lookahead past the label settles which: an
// update names its subject first, and that subject is never followed by `=`.
func TestInferredRecordVersusUpdate(t *testing.T) {
	cases := []struct{ src, want string }{
		{"{ x = 1 }", "(record-inferred (x (int 1)))"},
		{"{ x | y = 1 }", "(update (var x) (y (int 1)))"},
		{"{ x.y | z = 1 }", "(update (field y (var x)) (z (int 1)))"},
		{"{ f a | z = 1 }", "(update (app (var f) (var a)) (z (int 1)))"},
		// `==` and `=>` are their own lexemes, so neither can be mistaken for
		// the `=` that marks a field.
		{"{ x == y | z = 1 }", "(update (binop == (var x) (var y)) (z (int 1)))"},
		// A literal delimits itself, so it needs no parens as an argument.
		{"f { x = 1 }", "(app (var f) (record-inferred (x (int 1))))"},
		// A capitalized name immediately before `{` always names the record.
		{"Wrap { x = 1 }", "(record Wrap (x (int 1)))"},
	}
	for _, c := range cases {
		f := source.NewFile("<test>", []byte(c.src))
		toks, lexErrs := lexer.Lex(f)
		if len(lexErrs) > 0 {
			t.Fatalf("%q: %v", c.src, lexErrs)
		}
		e, errs := ParseExprInput(toks, f)
		if len(errs) > 0 {
			t.Errorf("%q: unexpected errors: %v", c.src, errs)
			continue
		}
		e, errs = fixity.Builtin().ResolveExpr(e)
		if len(errs) > 0 {
			t.Errorf("%q: grouping errors: %v", c.src, errs)
			continue
		}
		if got := ast.DumpExpr(e); got != c.want {
			t.Errorf("%q: got %s, want %s", c.src, got, c.want)
		}
	}
}

// `{}` can never be a literal, because a record type needs at least one field.
func TestEmptyBracesRejected(t *testing.T) {
	f := source.NewFile("<test>", []byte("{}"))
	toks, _ := lexer.Lex(f)
	_, errs := ParseExprInput(toks, f)
	if len(errs) == 0 || !strings.Contains(errs[0].Body, "at least one field") {
		t.Errorf("expected an empty-record error, got %v", errs)
	}
}
