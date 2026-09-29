package lexer

import (
	"reflect"
	"testing"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

func TestBacktickDelimiterContexts(t *testing.T) {
	for _, tc := range []struct {
		input string
		kinds []token.Kind
	}{
		{"quote", []token.Kind{token.LIDENT}},
		{"`1` `2`", []token.Kind{token.LQUOTE, token.INT, token.RQUOTE, token.LQUOTE, token.INT, token.RQUOTE}},
		{"`$(`1`)`", []token.Kind{token.LQUOTE, token.DOLLARPAREN, token.LQUOTE, token.INT, token.RQUOTE, token.RPAREN, token.RQUOTE}},
		{"`(`1`)`", []token.Kind{token.LQUOTE, token.LPAREN, token.LQUOTE, token.INT, token.RQUOTE, token.RPAREN, token.RQUOTE}},
		{"`[1]`", []token.Kind{token.LQUOTE, token.LBRACKET, token.INT, token.RBRACKET, token.RQUOTE}},
		{"#[Tag `[1]`] `2`", []token.Kind{token.ATTRIBUTE, token.UIDENT, token.LQUOTE, token.LBRACKET, token.INT, token.RBRACKET, token.RQUOTE, token.RBRACKET, token.LQUOTE, token.INT, token.RQUOTE}},
		{"`\"`\"`", []token.Kind{token.LQUOTE, token.STRING, token.RQUOTE}},
		{"`'`'`", []token.Kind{token.LQUOTE, token.CHAR, token.RQUOTE}},
		{"`{- ` {- ` -} -} 1 -- `\n`", []token.Kind{token.LQUOTE, token.INT, token.RQUOTE}},
	} {
		t.Run(tc.input, func(t *testing.T) {
			toks, errs := Lex(source.NewFile("<test>", []byte(tc.input)))
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			var got []token.Kind
			for _, tok := range toks[:len(toks)-1] {
				got = append(got, tok.Kind)
			}
			if !reflect.DeepEqual(got, tc.kinds) {
				t.Fatalf("token kinds = %v, want %v", got, tc.kinds)
			}
		})
	}
}
