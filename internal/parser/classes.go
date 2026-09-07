package parser

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/token"
)

// A context is recognized by its terminating =>, before consuming any type
// syntax. Parenthesized function arguments therefore remain unambiguous.
func (p *parser) parseContext() []ast.PredExpr {
	found := false
	parens := 0
	for i := p.pos; i < len(p.toks); i++ {
		t := p.toks[i]
		if t.Kind == token.EOF || (i > p.pos && t.Pos().Col <= p.lay.innermost().col) {
			break
		}
		if t.Kind == token.DARROW {
			found = true
			break
		}
		if t.Kind == token.EQ || (t.Kind == token.ARROW && parens == 0) {
			break
		}
		if t.Kind == token.LPAREN {
			parens++
		}
		if t.Kind == token.RPAREN {
			parens--
		}
	}
	if !found {
		return nil
	}
	paren := p.peekInExpr().Kind == token.LPAREN
	if paren {
		p.next()
	}
	var out []ast.PredExpr
	for {
		pred, ok := p.parsePredicate()
		if !ok {
			return out
		}
		out = append(out, pred)
		if !paren || p.peekInExpr().Kind != token.COMMA {
			break
		}
		p.next()
	}
	if paren {
		p.expect(token.RPAREN, "I expect `)` after the constraints.")
	}
	p.expect(token.DARROW, "I expect `=>` after the constraints.")
	return out
}

func (p *parser) parsePredicate() (ast.PredExpr, bool) {
	if p.peekInExpr().Kind != token.UIDENT {
		p.errorAt(p.peek().Span, "CLASS CONSTRAINT", "I expect a class name followed by one type argument.")
		return ast.PredExpr{}, false
	}
	name, _, sp := p.parseQualifiedName()
	ty := p.parseTypeAtom()
	if ty == nil {
		return ast.PredExpr{}, false
	}
	return ast.PredExpr{Class: name, Ty: ty, Sp: sp.Merge(ty.Span())}, true
}

func (p *parser) parseClassDecl() ast.Decl {
	// Class signatures use the same layout grammar as effect operations.
	d := p.parseEffectDecl()
	e, ok := d.(*ast.EffectDecl)
	if !ok {
		return nil
	}
	if len(e.Params) != 1 {
		p.errorAt(e.NameSpan, "CLASS PARAMETER", "A class requires exactly one type parameter.")
		return nil
	}
	for _, m := range e.Ops {
		if m.Native != nil {
			p.errorAt(m.NameSpan, "CLASS METHOD", "Class methods declare signatures; implementations belong in instances.")
		}
	}
	return &ast.ClassDecl{Name: e.Name, NameSpan: e.NameSpan, Param: e.Params[0], Methods: e.Ops}
}

func (p *parser) parseInstanceDecl() ast.Decl {
	p.next()
	ctx := p.parseContext()
	head, ok := p.parsePredicate()
	if !ok {
		p.recoverToTopLevel(false)
		return nil
	}
	d := &ast.InstanceDecl{Head: head, Preds: ctx}
	first := p.peek()
	if first.Kind == token.EOF {
		p.errorAt(head.Sp, TitleUnexpectedEOF, "This instance needs method definitions.")
		return nil
	}
	col := first.Pos().Col
	if col <= 1 {
		p.errorAt(first.Span, "INSTANCE METHOD", "Instance methods must be indented.")
		return nil
	}
	p.lay.push(ctxBlock, col)
	defer p.lay.pop()
	for p.peek().Kind != token.EOF && p.peek().Pos().Col >= col {
		p.stmtStart = p.pos
		n := p.peekInExpr()
		if n.Kind != token.LIDENT || n.Pos().Col != col {
			p.errorAt(n.Span, "INSTANCE METHOD", "I expect aligned method definitions.")
			p.recoverToTopLevel(false)
			return nil
		}
		p.next()
		params := p.parseValueParams()
		eq := p.peekInExpr()
		if !p.expect(token.EQ, "I expect `=` after the method parameters.") {
			p.recoverToTopLevel(false)
			return nil
		}
		body := p.parseBindBody(eq)
		if body == nil {
			p.recoverToTopLevel(false)
			return nil
		}
		d.Methods = append(d.Methods, &ast.ValueDecl{Name: n.Text, NameSpan: n.Span, Params: params, Body: body})
	}
	return d
}
