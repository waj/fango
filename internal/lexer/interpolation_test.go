package lexer

import (
	"testing"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

func TestStringInterpolationTokens(t *testing.T) {
	input := `"hi #{f { x = "}" }}#{"nested #{1}"} end"`
	toks, errs := Lex(source.NewFile("test.fango", []byte(input)))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	var opens, closes int
	last := 0
	for _, tok := range toks {
		if tok.Span.Start < last {
			t.Fatalf("overlapping token: %v", tok)
		}
		last = tok.Span.End
		if tok.Kind == token.INTERPOLATION_BEGIN {
			opens++
		}
		if tok.Kind == token.INTERPOLATION_END {
			closes++
		}
	}
	if opens != 3 || closes != opens {
		t.Fatalf("opens=%d closes=%d\n%s", opens, closes, DumpTokens(toks))
	}
	for _, text := range []string{`"\#{literal}"`, `"plain # text"`} {
		toks, errs := Lex(source.NewFile("test.fango", []byte(text)))
		if len(errs) != 0 || toks[0].Kind != token.STRING {
			t.Fatalf("%s: %v %v", text, toks, errs)
		}
	}
	if got := Unescape(`"\#{literal}"`); got != "#{literal}" {
		t.Fatal(got)
	}
}

func TestStringInterpolationErrors(t *testing.T) {
	for _, input := range []string{`"#{value`, "\"#{value\n}\"", "\"#{1 {- comment\n -}}\"", `"\#x"`} {
		_, errs := Lex(source.NewFile("test.fango", []byte(input)))
		if len(errs) == 0 {
			t.Fatalf("accepted %q", input)
		}
		if errs[0].Title == "UNFINISHED PROGRAM" {
			t.Fatal(errs)
		}
	}
}
