package lexer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

func TestRegexAndSlashOperators(t *testing.T) {
	for _, tc := range []struct {
		source string
		kinds  []token.Kind
	}{
		{`f /abc/`, []token.Kind{token.LIDENT, token.REGEX}},
		{`x /y`, []token.Kind{token.LIDENT, token.OP, token.LIDENT}},
		{`x/y/z`, []token.Kind{token.LIDENT, token.OP, token.LIDENT, token.OP, token.LIDENT}},
		{`x / y / z`, []token.Kind{token.LIDENT, token.OP, token.LIDENT, token.OP, token.LIDENT}},
		{`x /y/ z`, []token.Kind{token.LIDENT, token.REGEX, token.LIDENT}},
		{`(/), (/=), (//)`, []token.Kind{token.LPAREN, token.OP, token.RPAREN, token.COMMA, token.LPAREN, token.OP, token.RPAREN, token.COMMA, token.LPAREN, token.REGEX, token.RPAREN}},
		{`x /= y && z /= w`, []token.Kind{token.LIDENT, token.OP, token.LIDENT, token.OP, token.LIDENT, token.OP, token.LIDENT}},
		{`[/a/,//]`, []token.Kind{token.LBRACKET, token.REGEX, token.COMMA, token.REGEX, token.RBRACKET}},
		{`f /a\/b/ /\\/ /[\/]/ /(?i)abc/`, []token.Kind{token.LIDENT, token.REGEX, token.REGEX, token.REGEX, token.REGEX}},
		{"f /abc\nx / y", []token.Kind{token.LIDENT, token.OP, token.LIDENT, token.LIDENT, token.OP, token.LIDENT}},
		{`f /--{-"` + "`" + `}/ -- comment`, []token.Kind{token.LIDENT, token.REGEX}},
	} {
		t.Run(tc.source, func(t *testing.T) {
			tokens, comments, errs := LexWithComments(source.NewFile("<test>", []byte(tc.source)))
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			var got []token.Kind
			for _, tok := range tokens[:len(tokens)-1] {
				got = append(got, tok.Kind)
			}
			if !reflect.DeepEqual(got, tc.kinds) {
				t.Fatalf("got %v, want %v", got, tc.kinds)
			}
			if strings.HasSuffix(tc.source, "-- comment") && len(comments) != 1 {
				t.Fatalf("comments = %v", comments)
			}
		})
	}
}

func TestRegexPatternEscapes(t *testing.T) {
	for _, tc := range []struct{ source, pattern string }{
		{`//`, ``}, {`/a\/b/`, `a/b`}, {`/\d+\n/`, `\d+\n`},
		{`/\\/`, `\\`}, {`/\\\/x/`, `\\/x`}, {`/[\/]/`, `[/]`},
	} {
		if got := RegexPattern(tc.source); got != tc.pattern {
			t.Errorf("%s: got %q, want %q", tc.source, got, tc.pattern)
		}
	}
}
