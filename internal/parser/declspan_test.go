package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/source"
)

func parseSource(t *testing.T, src string) (*ast.Module, *source.File) {
	t.Helper()
	f := source.NewFile("t.fango", []byte(src))
	toks, errs := lexer.Lex(f)
	if len(errs) != 0 {
		t.Fatalf("lex: %v", errs)
	}
	m, perrs := Parse(toks, f)
	if len(perrs) != 0 {
		t.Fatalf("parse: %v", perrs)
	}
	return m, f
}

func declText(f *source.File, d ast.Decl) string {
	sp := ast.DeclSpan(d)
	return string(f.Content[sp.Start:sp.End])
}

func TestDeclSpanCoversDeclaration(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			"value with annotation",
			"answer : Int\nanswer = 42\n",
			[]string{"answer : Int\nanswer = 42"},
		},
		{
			"equation group is one declaration",
			"f : Int -> Int\nf 0 = 1\nf n = n\n",
			[]string{"f : Int -> Int\nf 0 = 1\nf n = n"},
		},
		{
			"blank lines and comments do not split a group",
			"f 0 = 1\n\n-- still the same function\nf n = n\n",
			[]string{"f 0 = 1\n\n-- still the same function\nf n = n"},
		},
		{
			"two declarations",
			"a = 1\nb = 2\n",
			[]string{"a = 1", "b = 2"},
		},
		{
			"type declaration",
			"type Color = Red | Green\n",
			[]string{"type Color = Red | Green"},
		},
		{
			"fixity declaration",
			"infixl 6 (+++)\n",
			[]string{"infixl 6 (+++)"},
		},
		{
			"indented body",
			"main =\n    x = 1\n    x\n",
			[]string{"main =\n    x = 1\n    x"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, f := parseSource(t, c.src)
			if len(m.Decls) != len(c.want) {
				t.Fatalf("got %d decls, want %d", len(m.Decls), len(c.want))
			}
			for i, d := range m.Decls {
				if got := declText(f, d); got != c.want[i] {
					t.Errorf("decl %d span text =\n%q\nwant\n%q", i, got, c.want[i])
				}
			}
		})
	}
}

// Declaration spans must not overlap and must stay inside the file: the
// formatter slices them to copy a declaration verbatim.
func TestDeclSpansAreOrderedAndDisjoint(t *testing.T) {
	roots := []string{
		filepath.Join("..", "..", "stdlib"),
		filepath.Join("..", "..", "examples"),
		filepath.Join("..", "..", "testdata", "parse"),
	}
	seen := 0
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
			toks, lexErrs := lexer.Lex(f)
			if len(lexErrs) > 0 {
				return nil
			}
			m, parseErrs := Parse(toks, f)
			if len(parseErrs) > 0 {
				return nil
			}
			prevEnd := 0
			for i, d := range m.Decls {
				sp := ast.DeclSpan(d)
				if sp.File == nil {
					t.Errorf("%s: decl %d has no span", path, i)
					continue
				}
				if sp.Start < prevEnd {
					t.Errorf("%s: decl %d starts at %d, before the previous decl ended at %d",
						path, i, sp.Start, prevEnd)
				}
				if sp.End > len(content) || sp.Start > sp.End {
					t.Errorf("%s: decl %d span [%d,%d) is out of range for %d bytes",
						path, i, sp.Start, sp.End, len(content))
				}
				prevEnd = sp.End
			}
			seen++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen == 0 {
		t.Fatal("walked no parseable .fango files")
	}
}
