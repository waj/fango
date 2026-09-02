package lexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/testutil"
)

func TestGoldens(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "lex", "*.fango"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no testdata/lex/*.fango files")
	}
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
