// Package lexer is a hand-written byte scanner. It lexes the whole file up
// front (the parser's layout predicates want token columns on arbitrary
// lookahead) and is layout-oblivious: the offside rule lives in the parser.
package lexer

import (
	"fmt"
	"strconv"

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
			l.lexInt(start)
		case isLower(c):
			l.lexIdent(start, false)
		case isUpper(c):
			l.lexIdent(start, true)
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

func (l *lexer) lexInt(start int) {
	for l.pos < len(l.f.Content) && isDigit(l.f.Content[l.pos]) {
		l.pos++
	}
	text := string(l.f.Content[start:l.pos])
	if _, err := strconv.ParseInt(text, 10, 64); err != nil {
		sp := source.Span{File: l.f, Start: start, End: l.pos}
		l.errs = append(l.errs, diag.Errorf(sp, "NUMBER TOO BIG",
			"This integer does not fit in 64 bits:\n\n    %s", text))
	}
	l.emit(token.INT, start, l.pos)
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
	out := ""
	for _, t := range toks {
		p := t.Pos()
		if t.Kind == token.EOF {
			out += fmt.Sprintf("%d:%d EOF\n", p.Line, p.Col)
		} else {
			out += fmt.Sprintf("%d:%d %s %s\n", p.Line, p.Col, t.Kind, t.Text)
		}
	}
	return out
}
