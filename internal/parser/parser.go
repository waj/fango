// Package parser is a hand-written recursive-descent parser with a Pratt
// loop for operator expressions. It enforces the offside rule via token
// columns (layout.go); the lexer is layout-oblivious.
package parser

import (
	"strconv"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

// TitleUnexpectedEOF marks "ran out of input mid-construct" diagnostics.
// The REPL uses it as its multi-line continuation signal.
const TitleUnexpectedEOF = "UNFINISHED PROGRAM"

type parser struct {
	f    *source.File
	toks []token.Token
	pos  int
	errs []diag.Error
	lay  layout
}

// Parse parses a whole module.
func Parse(toks []token.Token, f *source.File) (*ast.Module, []diag.Error) {
	p := &parser{f: f, toks: toks}
	p.lay.push(ctxDecl, 1)
	m := &ast.Module{}
	m.Header = p.parseHeader()
	for p.peek().Kind != token.EOF {
		if d := p.parseDecl(); d != nil {
			m.Decls = append(m.Decls, d)
		}
	}
	return m, p.errs
}

// ParseExprInput parses a single expression covering the whole input — the
// REPL's expression entry point. No column discipline applies.
func ParseExprInput(toks []token.Token, f *source.File) (ast.Expr, []diag.Error) {
	p := &parser{f: f, toks: toks}
	p.lay.push(ctxDecl, 0) // column 0: nothing is ever offside
	e := p.parseExpr(1)
	if t := p.peek(); t.Kind != token.EOF && len(p.errs) == 0 {
		p.errorAt(t.Span, "SYNTAX PROBLEM", "I parsed a complete expression but then ran into this.")
	}
	return e, p.errs
}

func (p *parser) parseHeader() *ast.ModuleHeader {
	if p.peek().Kind != token.KwModule {
		return nil
	}
	p.next()
	h := &ast.ModuleHeader{}
	if t := p.peek(); t.Kind == token.UIDENT {
		h.Name = t.Text
		p.next()
	} else {
		p.errorAt(t.Span, "SYNTAX PROBLEM", "After `module` I expect a capitalized module name.")
		p.recoverToTopLevel(false)
		return h
	}
	if !p.expect(token.KwExposing, "I expect `exposing` after the module name.") {
		p.recoverToTopLevel(false)
		return h
	}
	if !p.expect(token.LPAREN, "I expect a parenthesized list after `exposing`.") {
		p.recoverToTopLevel(false)
		return h
	}
	for {
		t := p.peek()
		if t.Kind == token.LIDENT || t.Kind == token.UIDENT {
			h.Exposing = append(h.Exposing, t.Text)
			p.next()
		} else {
			p.errorAt(t.Span, "SYNTAX PROBLEM", "I expect a name in the `exposing` list.")
			p.recoverToTopLevel(false)
			return h
		}
		if p.peek().Kind == token.COMMA {
			p.next()
			continue
		}
		break
	}
	if !p.expect(token.RPAREN, "I expect a closing `)` for the `exposing` list.") {
		p.recoverToTopLevel(false)
	}
	return h
}

func (p *parser) parseDecl() ast.Decl {
	t := p.peek()
	if t.Pos().Col != 1 {
		p.errorAt(t.Span, "SYNTAX PROBLEM",
			"I was expecting a new declaration, which must start at column 1,\nbut this is indented.")
		p.recoverToTopLevel(false)
		return nil
	}
	if t.Kind != token.LIDENT {
		p.errorAt(t.Span, "SYNTAX PROBLEM",
			"I was expecting a declaration here, like `name = expression`.")
		p.recoverToTopLevel(true) // the bad token sits at column 1: must consume it
		return nil
	}
	name := t
	p.next()
	if !p.expect(token.EQ, "I expect `=` after the name in a declaration.") {
		p.recoverToTopLevel(false)
		return nil
	}
	body := p.parseExpr(1)
	if body == nil {
		p.recoverToTopLevel(false)
		return nil
	}
	if t := p.peekInExpr(); t.Kind != token.EOF {
		p.errorAt(t.Span, "SYNTAX PROBLEM",
			"The expression seemed complete, but then I ran into this.")
		p.recoverToTopLevel(false)
	}
	return &ast.ValueDecl{Name: name.Text, NameSpan: name.Span, Body: body}
}

func binPrec(k token.Kind) int {
	switch k {
	case token.PLUS, token.MINUS:
		return 6
	case token.STAR: // SLASH joins in S1: `/` is Float-only and S0 has no Float
		return 7
	default:
		return 0
	}
}

func (p *parser) parseExpr(minPrec int) ast.Expr {
	left := p.parsePrimary()
	if left == nil {
		return nil
	}
	for {
		t := p.peekInExpr()
		prec := binPrec(t.Kind)
		if prec == 0 || prec < minPrec {
			return left
		}
		p.next()
		right := p.parseExpr(prec + 1) // all S0 operators are left-associative
		if right == nil {
			return nil
		}
		left = &ast.BinOp{Op: t.Text, OpSpan: t.Span, L: left, R: right}
	}
}

func (p *parser) parsePrimary() ast.Expr {
	t := p.peekInExpr()
	switch t.Kind {
	case token.INT:
		p.next()
		v, err := strconv.ParseInt(t.Text, 10, 64)
		if err != nil {
			// The lexer already reported the overflow; keep parsing.
			v = 0
		}
		return &ast.IntLit{Value: v, Sp: t.Span}
	case token.LIDENT:
		p.next()
		return &ast.Var{Name: t.Text, Sp: t.Span}
	case token.LPAREN:
		p.next()
		e := p.parseExpr(1)
		if e == nil {
			return nil
		}
		if inner := p.peekInExpr(); inner.Kind == token.RPAREN {
			p.next()
		} else if inner.Kind == token.EOF && p.peek().Kind == token.EOF {
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"I got to the end of the input while looking for a closing `)`.")
			return nil
		} else {
			p.errorAt(inner.Span, "SYNTAX PROBLEM", "I was expecting a closing `)` here.")
			return nil
		}
		return e
	case token.EOF:
		if p.peek().Kind == token.EOF {
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"I got to the end of the input while still expecting an expression.")
		} else {
			p.errorAt(p.prevSpan(), "SYNTAX PROBLEM",
				"This expression is unfinished — the next line starts a new\ndeclaration, so something is missing here.")
		}
		return nil
	default:
		p.errorAt(t.Span, "SYNTAX PROBLEM", "I was expecting an expression here.")
		return nil
	}
}

// peek returns the current token, ignoring layout.
func (p *parser) peek() token.Token { return p.toks[p.pos] }

// peekInExpr returns the current token, or a synthetic EOF if the offside
// rule says the current construct is over (token at or left of the layout
// column). This is how declaration boundaries end expressions.
func (p *parser) peekInExpr() token.Token {
	t := p.toks[p.pos]
	if t.Kind == token.EOF {
		return t
	}
	if p.lay.checkOffside(t.Pos()) != offContinue {
		return token.Token{Kind: token.EOF, Span: t.Span}
	}
	return t
}

func (p *parser) next() token.Token {
	t := p.toks[p.pos]
	if t.Kind != token.EOF {
		p.pos++
	}
	return t
}

func (p *parser) prevSpan() source.Span {
	if p.pos > 0 {
		return p.toks[p.pos-1].Span
	}
	return p.toks[p.pos].Span
}

func (p *parser) expect(k token.Kind, msg string) bool {
	if t := p.peekInExpr(); t.Kind == k {
		p.next()
		return true
	}
	if p.peek().Kind == token.EOF {
		p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
			"I got to the end of the input too soon. "+msg)
	} else {
		p.errorAt(p.peek().Span, "SYNTAX PROBLEM", msg)
	}
	return false
}

// recoverToTopLevel skips to the next token at column 1 (or EOF) so one
// syntax error does not cascade through the rest of the file. consumeFirst
// forces one token of progress — required when the offending token itself
// sits at column 1, or the parse loop would spin on it forever.
func (p *parser) recoverToTopLevel(consumeFirst bool) {
	if consumeFirst {
		p.next()
	}
	for {
		t := p.peek()
		if t.Kind == token.EOF || t.Pos().Col == 1 {
			return
		}
		p.next()
	}
}

func (p *parser) errorAt(sp source.Span, title, body string) {
	p.errs = append(p.errs, diag.Error{Title: title, Span: sp, Body: body})
}
