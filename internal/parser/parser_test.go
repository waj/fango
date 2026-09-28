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

func TestParenthesizedLambdaBodyAtEnclosingBlockColumn(t *testing.T) {
	for _, src := range []string{
		"main =\n    apply (\\x ->\n    value = x\n    value)\n    done",
		"main =\n    apply (\\x ->\nvalue = x\nvalue)\n    done",
		"main =\n    apply (\n        \\x ->\nvalue = x\nvalue\n    )\n    done",
		"main =\n    map (\\x ->\n        spawn (\\_ ->\n        value = x\n        value\n    )) values",
	} {
		f := source.NewFile("<test>", []byte(src))
		toks, lexErrs := lexer.Lex(f)
		if len(lexErrs) > 0 {
			t.Fatalf("unexpected lex errors for %q: %v", src, lexErrs)
		}
		_, errs := Parse(toks, f)
		if len(errs) > 0 {
			t.Errorf("could not parse %q: %v", src, errs)
		}
	}

	src := "main =\n    apply \\x ->\n    x"
	f := source.NewFile("<test>", []byte(src))
	toks, _ := lexer.Lex(f)
	_, errs := Parse(toks, f)
	if len(errs) == 0 {
		t.Errorf("accepted unparenthesized lambda at the enclosing block column")
	}
}

func TestFlexibleIndentationKeepsCanonicalTree(t *testing.T) {
	cases := []struct{ name, ragged, canonical string }{
		{"case branches", "match x =\n    case x of\n        True -> 1\n      False -> 2\n          _ -> 3\n", "match x =\n    case x of\n        True -> 1\n        False -> 2\n        _ -> 3\n"},
		{"nested cases", "match x y =\n    case x of\n        True ->\n            case y of\n                True -> 1\n              False -> 2\n      False -> 3\n", "match x y =\n    case x of\n        True ->\n            case y of\n                True -> 1\n                False -> 2\n        False -> 3\n"},
		{"constructor result before branch", "match x =\n    case x of\n        Just y ->\n            if y then\n                1\n            else\n                Found y\n        Nothing -> 0\n", "match x =\n    case x of\n        Just y ->\n            if y then\n                1\n            else\n                Found y\n        Nothing -> 0\n"},
		{"handler clauses", "run action =\n    handle action of\n        emit value -> resume value\n      log value -> resume value\n          return value -> value\n", "run action =\n    handle action of\n        emit value -> resume value\n        log value -> resume value\n        return value -> value\n"},
		{"block items", "main =\n        x = 1\n      y = x + 2\n       y\n", "main =\n    x = 1\n    y = x + 2\n    y\n"},
		{"nested blocks", "main =\n    x =\n            y = 1\n          y\n   x\n", "main =\n    x =\n        y = 1\n        y\n    x\n"},
		{"parenthesized lambda outdent", "main =\n    foo (\\_ ->\n        foo\n    bar\n    )\n", "main =\n    foo (\\_ ->\n        foo\n        bar\n    )\n"},
		{"parenthesized lambda binding outdent", "main =\n    foo (\\_ ->\n        x = 1\n    x\n    )\n", "main =\n    foo (\\_ ->\n        x = 1\n        x\n    )\n"},
		{"effect signatures", "effect Console\n        print : String -> ()\n      read : () -> String\n", "effect Console\n    print : String -> ()\n    read : () -> String\n"},
		{"short effect signature", "effect E\n        print : Int\n      x : Int\n", "effect E\n    print : Int\n    x : Int\n"},
		{"class signatures", "class Show a\n        show : a -> String\n      debug : a -> String\n", "class Show a\n    show : a -> String\n    debug : a -> String\n"},
		{"instance methods", "instance Show Int\n        show x = \"int\"\n      debug x = \"debug\"\n", "instance Show Int\n    show x = \"int\"\n    debug x = \"debug\"\n"},
		{"short instance method", "instance Show Int\n        show x = \"int\"\n      x = 1\n", "instance Show Int\n    show x = \"int\"\n    x = 1\n"},
		{"deriver methods", "deriver Show\n        show x = x\n      debug x = x\n", "deriver Show\n    show x = x\n    debug x = x\n"},
		{"short deriver method", "deriver Show\n        show x = x\n      x = 1\n", "deriver Show\n    show x = x\n    x = 1\n"},
	}
	parse := func(t *testing.T, src string) string {
		t.Helper()
		f := source.NewFile("<test>", []byte(src))
		toks, lexErrs := lexer.Lex(f)
		if len(lexErrs) > 0 {
			t.Fatalf("lex errors: %v", lexErrs)
		}
		m, errs := Parse(toks, f)
		if len(errs) > 0 {
			t.Fatalf("parse errors: %v", errs)
		}
		return ast.Dump(m)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := parse(t, tc.ragged), parse(t, tc.canonical); got != want {
				t.Errorf("different trees:\nragged: %s\ncanonical: %s", got, want)
			}
		})
	}
}

func TestFlexibleIndentationKeepsOwnerBoundary(t *testing.T) {
	for _, src := range []string{
		"main =\n    x = 1\ny\n",
		"main =\n    case x of\n        True -> 1\n    False -> 2\n",
		"effect Console\n    print : String -> ()\nread : () -> String\n",
		"main =\n    x = 1\n      y = 2\n    y\n",
	} {
		f := source.NewFile("<test>", []byte(src))
		toks, lexErrs := lexer.Lex(f)
		if len(lexErrs) > 0 {
			t.Fatalf("lex errors: %v", lexErrs)
		}
		_, errs := Parse(toks, f)
		if len(errs) == 0 {
			t.Errorf("accepted %q", src)
		}
	}
}

func TestTrailingBindingParsesAsIncompleteBlock(t *testing.T) {
	for _, src := range []string{
		"main =\n    apply (\\_ ->\n    value = x\n        bar\n    )\n",
		"main = x = 1\n",
	} {
		f := source.NewFile("<test>", []byte(src))
		toks, lexErrs := lexer.Lex(f)
		if len(lexErrs) > 0 {
			t.Fatal(lexErrs)
		}
		m, errs := Parse(toks, f)
		if len(errs) > 0 {
			t.Fatalf("unexpected parse errors: %v", errs)
		}
		if got := ast.Dump(m); !strings.Contains(got, "(missing-result)") {
			t.Fatalf("missing result marker in tree: %s", got)
		}
	}
}

func TestMalformedSemicolonBlocks(t *testing.T) {
	for _, src := range []string{
		"main = ; 1",
		"main = print 1;; 2",
		"main = print 1;",
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

func TestClosingParenthesesAtEnclosingLayoutColumn(t *testing.T) {
	src := "main =\n" +
		"    map (\\value -> spawn (\\_ ->\n" +
		"        answer = value\n" +
		"        answer\n" +
		"    )) values\n"
	f := source.NewFile("<test>", []byte(src))
	toks, lexErrs := lexer.Lex(f)
	if len(lexErrs) > 0 {
		t.Fatal(lexErrs)
	}
	if _, errs := Parse(toks, f); len(errs) > 0 {
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
