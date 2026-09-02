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
