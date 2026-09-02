// Package lexer is a hand-written byte scanner. It lexes the whole file up
// front (the parser's layout predicates want token columns on arbitrary
// lookahead) and is layout-oblivious: the offside rule lives in the parser.
package lexer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

type lexer struct {
	f    *source.File
	pos  int
	toks []token.Token
	errs []diag.Error
}

// Lex scans the entire file. The returned slice always ends with an EOF
// token, even when there are errors.
func Lex(f *source.File) ([]token.Token, []diag.Error) {
	l := &lexer{f: f}
	l.run()
	return l.toks, l.errs
}

func (l *lexer) run() {
	for {
		l.skipSpaceAndComments()
		if l.pos >= len(l.f.Content) {
			l.emit(token.EOF, l.pos, l.pos)
			return
		}
		start := l.pos
		c := l.f.Content[l.pos]
		switch {
		case isDigit(c):
			l.lexNumber(start)
		case c == '"':
			l.lexString(start)
		case isLower(c):
			l.lexIdent(start, false)
		case isUpper(c):
			l.lexIdent(start, true)
		case c == '_':
			l.lexUnderscore(start)
		default:
			l.lexOperator(start)
		}
	}
}

func (l *lexer) skipSpaceAndComments() {
	for l.pos < len(l.f.Content) {
		c := l.f.Content[l.pos]
		switch {
		case c == ' ' || c == '\n' || c == '\r':
			l.pos++
		case c == '\t':
			sp := source.Span{File: l.f, Start: l.pos, End: l.pos + 1}
			l.errs = append(l.errs, diag.Errorf(sp, "TAB CHARACTER",
				"I found a tab character. fango indentation is column-sensitive, so\ntabs are not allowed — use spaces."))
			l.pos++
		case c == '-' && l.peekAt(1) == '-':
			for l.pos < len(l.f.Content) && l.f.Content[l.pos] != '\n' {
				l.pos++
			}
		case c == '{' && l.peekAt(1) == '-':
			l.skipBlockComment()
		default:
			return
		}
	}
}

func (l *lexer) skipBlockComment() {
	start := l.pos
	depth := 0
	for l.pos < len(l.f.Content) {
		if l.f.Content[l.pos] == '{' && l.peekAt(1) == '-' {
			depth++
			l.pos += 2
		} else if l.f.Content[l.pos] == '-' && l.peekAt(1) == '}' {
			depth--
			l.pos += 2
			if depth == 0 {
				return
			}
		} else {
			l.pos++
		}
	}
	sp := source.Span{File: l.f, Start: start, End: start + 2}
	l.errs = append(l.errs, diag.Errorf(sp, "UNCLOSED COMMENT",
		"I got to the end of the file while looking for the `-}` that closes\nthis comment."))
}

// lexNumber scans an INT, promoting to FLOAT on `digits '.' digits [exp]`
// or `digits exp` (Elm: `1e3` is a Float; `.5` and `1.` are not literals).
func (l *lexer) lexNumber(start int) {
	l.scanDigits()
	isFloat := false
	if l.peekAt(0) == '.' && isDigit(l.peekAt(1)) {
		isFloat = true
		l.pos++ // '.'
		l.scanDigits()
	}
	if l.scanExponent() {
		isFloat = true
	}
	text := string(l.f.Content[start:l.pos])
	sp := source.Span{File: l.f, Start: start, End: l.pos}
	if isFloat {
		if v, err := strconv.ParseFloat(text, 64); err != nil || v > 1.7976931348623157e308 {
			l.errs = append(l.errs, diag.Errorf(sp, "NUMBER TOO BIG",
				"This number is too large for a 64-bit Float:\n\n    %s", text))
		}
		l.emit(token.FLOAT, start, l.pos)
		return
	}
	if _, err := strconv.ParseInt(text, 10, 64); err != nil {
		l.errs = append(l.errs, diag.Errorf(sp, "NUMBER TOO BIG",
			"This integer does not fit in 64 bits:\n\n    %s", text))
	}
	l.emit(token.INT, start, l.pos)
}

func (l *lexer) scanDigits() {
	for l.pos < len(l.f.Content) && isDigit(l.f.Content[l.pos]) {
		l.pos++
	}
}

// scanExponent consumes `[eE][+-]?digits` if fully present.
func (l *lexer) scanExponent() bool {
	c := l.peekAt(0)
	if c != 'e' && c != 'E' {
		return false
	}
	next := l.peekAt(1)
	if isDigit(next) {
		l.pos += 2
	} else if (next == '+' || next == '-') && isDigit(l.peekAt(2)) {
		l.pos += 3
	} else {
		return false // `1e` alone: leave the e for the identifier lexer
	}
	l.scanDigits()
	return true
}

// lexString scans a single-line string literal. The token Text keeps the
// raw source including quotes; Unescape decodes it. An unclosed string is
// its own error title — deliberately not the parser's unexpected-EOF, so
// the REPL errors immediately instead of prompting for a continuation.
func (l *lexer) lexString(start int) {
	l.pos++ // opening quote
	for l.pos < len(l.f.Content) {
		switch c := l.f.Content[l.pos]; c {
		case '"':
			l.pos++
			l.emit(token.STRING, start, l.pos)
			return
		case '\n':
			sp := source.Span{File: l.f, Start: start, End: l.pos}
			l.errs = append(l.errs, diag.Errorf(sp, "UNCLOSED STRING",
				"This string never gets a closing double quote on its line.\nStrings cannot span lines."))
			l.emit(token.STRING, start, l.pos)
			return
		case '\\':
			switch l.peekAt(1) {
			case '\\', '"', 'n', 't', 'r':
				l.pos += 2
			default:
				sp := source.Span{File: l.f, Start: l.pos, End: l.pos + 2}
				l.errs = append(l.errs, diag.Errorf(sp, "UNKNOWN ESCAPE",
					"I do not recognize this escape sequence. Valid escapes are:\n\n    \\\\  \\\"  \\n  \\t  \\r"))
				l.pos += 2
			}
		default:
			l.pos++
		}
	}
	sp := source.Span{File: l.f, Start: start, End: l.pos}
	l.errs = append(l.errs, diag.Errorf(sp, "UNCLOSED STRING",
		"I got to the end of the file while looking for the closing double\nquote of this string."))
	l.emit(token.STRING, start, l.pos)
}

// Unescape decodes a raw STRING token text (including its quotes) into the
// string value. The lexer has already validated the escapes.
func Unescape(raw string) string {
	raw = strings.TrimPrefix(raw, `"`)
	raw = strings.TrimSuffix(raw, `"`)
	if !strings.Contains(raw, `\`) {
		return raw
	}
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' || i+1 == len(raw) {
			b.WriteByte(raw[i])
			continue
		}
		i++
		switch raw[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		default: // \\ and \"
			b.WriteByte(raw[i])
		}
	}
	return b.String()
}

func (l *lexer) lexIdent(start int, upper bool) {
	for l.pos < len(l.f.Content) && isIdentChar(l.f.Content[l.pos]) {
		l.pos++
	}
	text := string(l.f.Content[start:l.pos])
	if kw, ok := token.Keywords[text]; ok {
		l.emit(kw, start, l.pos)
	} else if upper {
		l.emit(token.UIDENT, start, l.pos)
	} else {
		l.emit(token.LIDENT, start, l.pos)
	}
}

// lexUnderscore scans `_` (the wildcard pattern). A leading underscore on a
// name is rejected here, Elm-style, so the parser never sees one.
func (l *lexer) lexUnderscore(start int) {
	l.pos++
	if l.pos < len(l.f.Content) && isIdentChar(l.f.Content[l.pos]) {
		for l.pos < len(l.f.Content) && isIdentChar(l.f.Content[l.pos]) {
			l.pos++
		}
		sp := source.Span{File: l.f, Start: start, End: l.pos}
		l.errs = append(l.errs, diag.Errorf(sp, "NAMING PROBLEM",
			"Names cannot start with an underscore:\n\n    %s\n\nA lone `_` is the wildcard pattern; names must start with a letter.",
			string(l.f.Content[start:l.pos])))
		return
	}
	l.emit(token.UNDERSCORE, start, l.pos)
}

var twoCharOps = []struct {
	text string
	kind token.Kind
}{
	{"++", token.PLUSPLUS}, {"==", token.EQEQ}, {"/=", token.SLASHEQ},
	{"<=", token.LTEQ}, {">=", token.GTEQ}, {"->", token.ARROW},
}

var oneCharOps = map[byte]token.Kind{
	'=': token.EQ, '+': token.PLUS, '-': token.MINUS, '*': token.STAR,
	'/': token.SLASH, '(': token.LPAREN, ')': token.RPAREN, ',': token.COMMA,
	'<': token.LT, '>': token.GT, ':': token.COLON, '|': token.PIPE,
	'{': token.LBRACE, '}': token.RBRACE, '\\': token.BACKSLASH,
}

func (l *lexer) lexOperator(start int) {
	rest := l.f.Content[l.pos:]
	for _, op := range twoCharOps {
		if len(rest) >= 2 && string(rest[:2]) == op.text {
			l.pos += 2
			l.emit(op.kind, start, l.pos)
			return
		}
	}
	c := l.f.Content[l.pos]
	if kind, ok := oneCharOps[c]; ok {
		l.pos++
		l.emit(kind, start, l.pos)
		return
	}
	l.pos++
	sp := source.Span{File: l.f, Start: start, End: l.pos}
	l.errs = append(l.errs, diag.Errorf(sp, "UNEXPECTED CHARACTER",
		"I do not recognize this character:\n\n    %s", strconv.QuoteRune(rune(c))))
}

func (l *lexer) emit(k token.Kind, start, end int) {
	l.toks = append(l.toks, token.Token{
		Kind: k,
		Text: string(l.f.Content[start:end]),
		Span: source.Span{File: l.f, Start: start, End: end},
	})
}

func (l *lexer) peekAt(n int) byte {
	if l.pos+n < len(l.f.Content) {
		return l.f.Content[l.pos+n]
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isLower(c byte) bool { return c >= 'a' && c <= 'z' }
func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }
func isIdentChar(c byte) bool {
	return isLower(c) || isUpper(c) || isDigit(c) || c == '_'
}

// DumpTokens renders tokens in the golden-test format: one token per line,
// "line:col KIND text".
func DumpTokens(toks []token.Token) string {
	var b strings.Builder
	for _, t := range toks {
		p := t.Pos()
		if t.Kind == token.EOF {
			fmt.Fprintf(&b, "%d:%d EOF\n", p.Line, p.Col)
		} else {
			fmt.Fprintf(&b, "%d:%d %s %s\n", p.Line, p.Col, t.Kind, t.Text)
		}
	}
	return b.String()
}
