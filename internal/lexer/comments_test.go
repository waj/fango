package lexer

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

// tile reports the first byte range of f that is covered by neither a token
// nor a comment and is not whitespace. Tokens, comments, and whitespace must
// together cover the file: that is what lets the formatter reconstruct source
// text it did not print itself, and what makes "no comment is dropped" a
// checkable property rather than a hoped-for one.
func tile(f *source.File, toks []token.Token, cs []token.Comment) (int, int, bool) {
	type span struct{ start, end int }
	var spans []span
	for _, t := range toks {
		if t.Kind == token.EOF {
			continue
		}
		spans = append(spans, span{t.Span.Start, t.Span.End})
	}
	for _, c := range cs {
		spans = append(spans, span{c.Span.Start, c.Span.End})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	at := 0
	for _, s := range spans {
		if s.start > at {
			for i := at; i < s.start; i++ {
				if !isSpaceByte(f.Content[i]) {
					return at, s.start, false
				}
			}
		}
		if s.end > at {
			at = s.end
		}
	}
	for i := at; i < len(f.Content); i++ {
		if !isSpaceByte(f.Content[i]) {
			return at, len(f.Content), false
		}
	}
	return 0, 0, true
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\n' || b == '\r' || b == '\t'
}

func TestCommentsTileWithTokens(t *testing.T) {
	roots := []string{
		filepath.Join("..", "..", "testdata"),
		filepath.Join("..", "..", "stdlib"),
		filepath.Join("..", "..", "examples"),
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
			toks, cs, lexErrs := LexWithComments(f)
			if len(lexErrs) > 0 {
				// A file with lex errors has bytes the lexer reported and skipped
				// without emitting a token, so it cannot tile. The formatter
				// refuses such files for the same reason.
				return nil
			}
			if from, to, ok := tile(f, toks, cs); !ok {
				t.Errorf("%s: bytes [%d,%d) covered by neither a token nor a comment: %q",
					path, from, to, string(content[from:to]))
			}
			seen++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen == 0 {
		t.Fatal("walked no .fango files")
	}
}

func TestCommentCapture(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"line", "x = 1 -- trailing\n", []string{"-- trailing"}},
		{"own line", "-- leading\nx = 1\n", []string{"-- leading"}},
		{"block", "x = {- mid -} 1\n", []string{"{- mid -}"}},
		{"nested block", "{- outer {- inner -} still -}\nx = 1\n", []string{"{- outer {- inner -} still -}"}},
		{"dashes in string", `x = "a--b"` + "\n", nil},
		{"dashes in char", `x = '-'` + "\n", nil},
		{"brace in string", `x = "{- not a comment"` + "\n", nil},
		{"pragma is not a comment", "{-# no-prelude #-}\nx = 1\n", nil},
		{"several", "-- one\nx = 1 -- two\n{- three -}\n", []string{"-- one", "-- two", "{- three -}"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := source.NewFile("t.fango", []byte(c.src))
			toks, cs, errs := LexWithComments(f)
			if len(errs) != 0 {
				t.Fatalf("lex: %v", errs)
			}
			var got []string
			for _, cm := range cs {
				got = append(got, cm.Text)
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("comments = %q, want %q", got, c.want)
			}
			if from, to, ok := tile(f, toks, cs); !ok {
				t.Errorf("tiling gap [%d,%d): %q", from, to, c.src[from:to])
			}
		})
	}
}

func TestUnclosedBlockCommentStillRecorded(t *testing.T) {
	f := source.NewFile("t.fango", []byte("x = 1\n{- never closed\n"))
	toks, cs, errs := LexWithComments(f)
	if len(errs) != 1 || errs[0].Title != "UNCLOSED COMMENT" {
		t.Fatalf("errors = %#v", errs)
	}
	if len(cs) != 1 || !cs[0].Block {
		t.Fatalf("comments = %#v", cs)
	}
	if from, to, ok := tile(f, toks, cs); !ok {
		t.Errorf("tiling gap [%d,%d)", from, to)
	}
}

func TestLexDropsCommentsButLexWithCommentsKeepsThem(t *testing.T) {
	src := []byte("-- a\nx = 1 {- b -}\n")
	f := source.NewFile("t.fango", src)
	plain, _ := Lex(f)
	withCs, cs, _ := LexWithComments(source.NewFile("t.fango", src))
	if len(cs) != 2 {
		t.Fatalf("want 2 comments, got %d", len(cs))
	}
	if len(plain) != len(withCs) {
		t.Fatalf("Lex and LexWithComments disagree on token count: %d vs %d", len(plain), len(withCs))
	}
	for i := range plain {
		if plain[i].Kind != withCs[i].Kind || plain[i].Text != withCs[i].Text {
			t.Fatalf("token %d differs: %#v vs %#v", i, plain[i], withCs[i])
		}
	}
}
