package lexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/testutil"
)

func TestUnescape(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`"hi"`, "hi"},
		{`""`, ""},
		{`"a\"b"`, `a"b`},
		{`"a\\b"`, `a\b`},
		{`"n\nt\tr\r"`, "n\nt\tr\r"},
	}
	for _, c := range cases {
		if got := Unescape(c.raw); got != c.want {
			t.Errorf("Unescape(%s) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestInvalidUTF8Source(t *testing.T) {
	f := source.NewFile("bad.fango", []byte{'x', ' ', '=', ' ', 0xff})
	_, errs := Lex(f)
	if len(errs) != 1 || errs[0].Title != "INVALID UTF-8" {
		t.Fatalf("Lex invalid UTF-8 errors = %#v", errs)
	}
}

func TestModulePunctuationAndKeywords(t *testing.T) {
	f := source.NewFile("modules.fango", []byte("import Geometry.Point as P exposing (Point(..))"))
	toks, errs := Lex(f)
	if len(errs) != 0 {
		t.Fatalf("lex: %v", errs)
	}
	got := DumpTokens(toks)
	for _, want := range []string{"import import", "DOT .", "as as", "exposing exposing", "DOTDOT .."} {
		if !strings.Contains(got, want) {
			t.Errorf("tokens missing %q:\n%s", want, got)
		}
	}
}

func TestSemicolonIsStatementPunctuation(t *testing.T) {
	f := source.NewFile("sequence.fango", []byte("main = print 1; 2"))
	toks, errs := Lex(f)
	if len(errs) != 0 {
		t.Fatalf("lex: %v", errs)
	}
	if got := DumpTokens(toks); !strings.Contains(got, "SEMICOLON ;") {
		t.Fatalf("tokens missing semicolon:\n%s", got)
	}
}

func TestBackslashLambdaIsRejected(t *testing.T) {
	f := source.NewFile("old.fango", []byte(`value = \x -> x`))
	_, errs := Lex(f)
	if len(errs) == 0 || errs[0].Title != "UNEXPECTED CHARACTER" {
		t.Fatalf("old lambda syntax errors = %#v", errs)
	}
}

func TestGoldens(t *testing.T) {
	files := testutil.GlobFango(t, filepath.Join("..", "..", "testdata", "lex"))
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f := source.NewFile(filepath.Base(path), content)
			toks, errs := Lex(f)
			out := DumpTokens(toks)
			if len(errs) > 0 {
				out += "-- errors --\n" + testutil.DumpErrors(errs)
			}
			testutil.Golden(t, strings.TrimSuffix(path, ".fango")+".tokens", out)
		})
	}
}
