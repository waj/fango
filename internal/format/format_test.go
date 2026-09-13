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

// A declaration holding a comment is copied verbatim, which is what keeps the
// comment from moving before the printer can anchor it.
func TestDeclarationWithCommentIsCopiedVerbatim(t *testing.T) {
	src := "module M exposing (f)\n\nimport Basics exposing (..)\n\n" +
		"f   x =\n    -- why\n    x\n"
	out, errs := Source(source.NewFile("m.fango", []byte(src)))
	if len(errs) > 0 {
		t.Fatalf("%s: %s", errs[0].Title, errs[0].Body)
	}
	if !strings.HasSuffix(string(out), "f   x =\n    -- why\n    x\n") {
		t.Errorf("declaration with a comment was not copied verbatim:\n%s", out)
	}
}
