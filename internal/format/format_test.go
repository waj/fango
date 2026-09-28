package format

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/testutil"
)

func formatFile(t *testing.T, path string) (in, out []byte) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, errs := Source(source.NewFile(filepath.Base(path), content))
	if len(errs) > 0 {
		t.Fatalf("%s: %s: %s", path, errs[0].Title, errs[0].Body)
	}
	return content, got
}

func TestGoldens(t *testing.T) {
	files := testutil.GlobFango(t, filepath.Join("..", "..", "testdata", "format"))
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			_, got := formatFile(t, path)
			testutil.Golden(t, strings.TrimSuffix(path, ".fango")+".formatted", string(got))
		})
	}
}

func TestClosingParenthesesKeepTheirLine(t *testing.T) {
	src := "main =\n" +
		"    map (\\value -> spawn (\\_ ->\n" +
		"        answer = value\n" +
		"        answer\n" +
		"    )) values\n"
	for _, input := range []string{src, strings.Replace(src, "    )) values", "        )) values", 1)} {
		out, errs := Source(source.NewFile("<test>", []byte(input)))
		if len(errs) > 0 {
			t.Fatalf("formatting failed: %v", errs)
		}
		if string(out) != src {
			t.Errorf("closing parentheses moved:\n%s", out)
		}
	}
}

func TestParenthesizedLambdaBodyIndented(t *testing.T) {
	want := "main =\n" +
		"    map (\\value ->\n" +
		"        spawn (\\_ ->\n" +
		"            answer = value\n" +
		"            answer\n" +
		"        )\n" +
		"    ) values\n"
	for _, input := range []string{
		strings.ReplaceAll(want, "            answer", "        answer"),
		strings.ReplaceAll(want, "            answer", "    answer"),
	} {
		out, errs := Source(source.NewFile("<test>", []byte(input)))
		if len(errs) > 0 {
			t.Fatalf("formatting failed: %v", errs)
		}
		if string(out) != want {
			t.Errorf("body not indented:\n%s", out)
		}
	}
}

func TestMultilineParenthesisClosings(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{
			"main =\n    foo (bar (\\x ->\n        x)) baz\n",
			"main =\n    foo (bar (\\x ->\n        x\n    )) baz\n",
		},
		{
			"main =\n    foo (\n        bar (\\x ->\n            x)) baz\n",
			"main =\n    foo (\n        bar (\\x ->\n            x\n        )\n    ) baz\n",
		},
	} {
		out, errs := Source(source.NewFile("<test>", []byte(tc.input)))
		if len(errs) > 0 {
			t.Errorf("formatting %q failed: %v", tc.input, errs)
			continue
		}
		if string(out) != tc.want {
			t.Errorf("formatting %q:\n got: %s\nwant: %s", tc.input, out, tc.want)
		}
	}
}

// corpus is every .fango file the formatter should be able to handle: the
// fixtures, the bundled standard library, the examples, and the test data for
// the other stages. Files that do not lex or parse are skipped, since the
// formatter refuses those by design.
func corpus(t *testing.T) []string {
	t.Helper()
	roots := []string{
		filepath.Join("..", "..", "testdata"),
		filepath.Join("..", "..", "stdlib"),
		filepath.Join("..", "..", "examples"),
	}
	var paths []string
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".fango") {
				return err
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			f := source.NewFile(filepath.Base(path), content)
			if _, errs := Source(f); len(errs) > 0 {
				// A fixture that does not parse is not a formatter failure:
				// testdata deliberately holds malformed inputs.
				return nil
			}
			paths = append(paths, path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(paths) == 0 {
		t.Fatal("walked no formattable .fango files")
	}
	return paths
}

// TestIdempotent is the property that makes a formatter safe to run on save.
func TestIdempotent(t *testing.T) {
	for _, path := range corpus(t) {
		_, once := formatFile(t, path)
		twice, errs := Source(source.NewFile(filepath.Base(path), once))
		if len(errs) > 0 {
			t.Errorf("%s: reformatting the output failed: %s", path, errs[0].Title)
			continue
		}
		if string(twice) != string(once) {
			t.Errorf("%s: formatting is not idempotent", path)
		}
	}
}

// TestNoCommentLost is the cheapest guard on the thing most easily broken:
// Source already verifies it internally, so a failure here means the check
// itself regressed.
func TestNoCommentLost(t *testing.T) {
	for _, path := range corpus(t) {
		in, out := formatFile(t, path)
		_, before, _ := lexer.LexWithComments(source.NewFile("a", in))
		_, after, _ := lexer.LexWithComments(source.NewFile("b", out))
		if len(before) != len(after) {
			t.Errorf("%s: %d comments in, %d out", path, len(before), len(after))
			continue
		}
		// A multiset: sorting the import block moves a comment with its
		// import, so order is the fixtures' business, not this check's.
		if !bytes.Equal(commentTexts(before), commentTexts(after)) {
			t.Errorf("%s: the comments changed", path)
		}
	}
}

// TestTrailingNewline pins the file-level shape the formatter guarantees.
func TestTrailingNewline(t *testing.T) {
	for _, path := range corpus(t) {
		_, out := formatFile(t, path)
		if len(out) == 0 {
			continue
		}
		if !strings.HasSuffix(string(out), "\n") || strings.HasSuffix(string(out), "\n\n") {
			t.Errorf("%s: output must end with exactly one newline", path)
		}
	}
}

func TestRefusesFileThatDoesNotParse(t *testing.T) {
	f := source.NewFile("bad.fango", []byte("module Bad exposing (a)\n\na = = 1\n"))
	out, errs := Source(f)
	if out != nil {
		t.Fatalf("formatted a file that does not parse: %q", out)
	}
	if len(errs) == 0 {
		t.Fatal("want diagnostics for a file that does not parse")
	}
}

func TestRefusesFileThatDoesNotLex(t *testing.T) {
	f := source.NewFile("bad.fango", []byte("a = 1\n\tb = 2\n"))
	out, errs := Source(f)
	if out != nil {
		t.Fatalf("formatted a file that does not lex: %q", out)
	}
	if len(errs) == 0 {
		t.Fatal("want diagnostics for a file that does not lex")
	}
}

func TestInferredRecordPatternAfterConstructorKeepsGrouping(t *testing.T) {
	for _, src := range []string{
		"foo W ({ x = x }) = x\n",
		"foo (S ({ x = x })) = x\n",
		"foo = \\W ({ x = x }) -> x\n",
	} {
		out, errs := Source(source.NewFile("patterns.fango", []byte(src)))
		if len(errs) > 0 {
			t.Fatalf("%q: %s: %s", src, errs[0].Title, errs[0].Body)
		}
		if string(out) != src {
			t.Errorf("formatted %q as %q", src, out)
		}
	}
}

// A layout construct keeps the author's line structure while its spacing is
// normalized, and the branch column stays a level in from the `case`.
func TestLayoutIsRenderedNotCopied(t *testing.T) {
	src := "module M exposing (f)\n\nimport Basics exposing (..)\n\n" +
		"f   x =\n    case x of\n        0 ->   1\n        n -> n\n"
	out, errs := Source(source.NewFile("m.fango", []byte(src)))
	if len(errs) > 0 {
		t.Fatalf("%s: %s", errs[0].Title, errs[0].Body)
	}
	body := "f x =\n    case x of\n        0 -> 1\n        n -> n\n"
	if !strings.HasSuffix(string(out), body) {
		t.Errorf("layout was not rendered as expected:\n%s", out)
	}
}

func TestRaggedLayoutNormalizesAndRoundTrips(t *testing.T) {
	cases := []struct{ input, want string }{
		{"main =\n        x = 1\n      y = x + 2\n       y\n", "main =\n    x = 1\n    y = x + 2\n    y\n"},
		{"main =\n        x = 1\n      -- next value\n      y = x + 2\n       y\n", "main =\n    x = 1\n    -- next value\n    y = x + 2\n    y\n"},
		{"main =\n    foo (\\_ ->\n        foo\n    bar\n    )\n", "main =\n    foo (\\_ ->\n        foo\n        bar\n    )\n"},
		{"main =\n    foo (\\_ ->\n        x = 1\n    x\n    )\n", "main =\n    foo (\\_ ->\n        x = 1\n        x\n    )\n"},
		{"main =\n    foo (\\_ ->\n    value = x\n        bar\n    )\n", "main =\n    foo (\\_ ->\n        value = x\n            bar\n    )\n"},
		{"main = x = 1\n", "main = x = 1\n"},
		{"match x =\n    case x of\n        True -> 1\n      False -> 2\n          _ -> 3\n", "match x =\n    case x of\n        True -> 1\n        False -> 2\n        _ -> 3\n"},
		{"run action =\n    handle action of\n        emit value -> resume value\n      log value -> resume value\n          return value -> value\n", "run action =\n    handle action of\n        emit value -> resume value\n        log value -> resume value\n        return value -> value\n"},
		{"effect Console\n        print : String -> ()\n      read : () -> String\n", "effect Console\n    print : String -> ()\n    read : () -> String\n"},
		{"class Show a\n        show : a -> String\n      debug : a -> String\n", "class Show a\n    show : a -> String\n    debug : a -> String\n"},
		{"instance Show Int\n        show x = \"int\"\n      debug x = \"debug\"\n", "instance Show Int\n    show x = \"int\"\n    debug x = \"debug\"\n"},
		{"deriver Show\n        show x = x\n      debug x = x\n", "deriver Show\n    show x = x\n    debug x = x\n"},
	}
	for _, tc := range cases {
		out, errs := Source(source.NewFile("ragged.fango", []byte(tc.input)))
		if len(errs) > 0 {
			t.Errorf("formatting %q failed: %v", tc.input, errs)
			continue
		}
		if string(out) != tc.want {
			t.Errorf("formatting %q:\n got: %s\nwant: %s", tc.input, out, tc.want)
		}
	}
}

// A comment with an anchor is placed, and the declaration around it is
// normalized like any other.
func TestAnchoredCommentIsPlaced(t *testing.T) {
	src := "module M exposing (f)\n\nimport Basics exposing (..)\n\n" +
		"f   x =\n        -- why\n    x\n"
	out, errs := Source(source.NewFile("m.fango", []byte(src)))
	if len(errs) > 0 {
		t.Fatalf("%s: %s", errs[0].Title, errs[0].Body)
	}
	if !strings.HasSuffix(string(out), "f x =\n    -- why\n    x\n") {
		t.Errorf("anchored comment was not placed:\n%s", out)
	}
}

// A comment with no anchor — here between an operator and its operand — sends
// the declaration to a verbatim copy, so the comment cannot be moved or lost.
func TestUnanchorableCommentCopiesDeclaration(t *testing.T) {
	body := "f   x =\n    x + -- why\n        1\n"
	src := "module M exposing (f)\n\nimport Basics exposing (..)\n\n" + body
	out, errs := Source(source.NewFile("m.fango", []byte(src)))
	if len(errs) > 0 {
		t.Fatalf("%s: %s", errs[0].Title, errs[0].Body)
	}
	if !strings.HasSuffix(string(out), body) {
		t.Errorf("declaration with an unanchorable comment was not copied:\n%s", out)
	}
}

// A multiline composite has no comment anchors yet. Its declaration therefore
// takes the same conservative verbatim fallback as every other unsupported
// comment position.
func TestCommentInBrokenCompositeCopiesDeclaration(t *testing.T) {
	body := "values =\n    [first\n    -- why\n    , second]\n"
	src := "module M exposing (values)\n\nimport Basics exposing (..)\n\n" + body
	out, errs := Source(source.NewFile("m.fango", []byte(src)))
	if len(errs) > 0 {
		t.Fatalf("%s: %s", errs[0].Title, errs[0].Body)
	}
	if !strings.HasSuffix(string(out), body) {
		t.Errorf("declaration with a composite comment was not copied:\n%s", out)
	}
}

func TestResourceMarkerStaysWithDeclaration(t *testing.T) {
	for _, src := range []string{
		"{-# resource #-}\ntype Handle= Handle Int\n",
		"{-# no-prelude #-}\nmodule M exposing (Handle)\n\n{-# resource #-}\n-- resource comment\ntype Handle = { id:Int }\n",
	} {
		out, errs := Source(source.NewFile("resource.fango", []byte(src)))
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		again, errs := Source(source.NewFile("resource.fango", out))
		if len(errs) > 0 || !bytes.Equal(out, again) {
			t.Fatalf("not idempotent: %s; %v", out, errs)
		}
		if strings.Count(string(out), "{-# resource #-}") != 1 {
			t.Fatalf("marker lost or duplicated: %s", out)
		}
	}
}

func TestScopedMarkerStaysWithDeclaration(t *testing.T) {
	for _, src := range []string{
		"{-# scoped s #-}\nrun : (Int ->{s} a) ->{e} a\nrun use=use 0\n",
		"{-# scoped s #-}\n-- callback comment\nrun : (Int ->{s} a) ->{e} a\nrun = native\n",
		"{-# scoped s #-}\nrun : (Int ->{s} a) ->{e} a\nrun use =\n    -- body comment\n    use 0\n",
	} {
		out, errs := Source(source.NewFile("scoped.fango", []byte(src)))
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		again, errs := Source(source.NewFile("scoped.fango", out))
		if len(errs) > 0 || !bytes.Equal(out, again) {
			t.Fatalf("not idempotent: %s; %v", out, errs)
		}
		if strings.Count(string(out), "{-# scoped s #-}") != 1 {
			t.Fatalf("marker lost or duplicated: %s", out)
		}
	}
}
