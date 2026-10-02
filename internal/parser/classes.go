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

// parseClassDecl parses a class header and its indented block. A line is a
// method signature (`name : type`) or an equation giving a declared method's
// default (`name params = body`), which must follow that method's signature.
func (p *parser) parseClassDecl() ast.Decl {
	p.next() // `class`
	nameT := p.peekInExpr()
	if nameT.Kind != token.UIDENT {
		p.errorAt(nameT.Span, "SYNTAX PROBLEM", "After `class` I expect a capitalized class name, like `Show`.")
		p.recoverToTopLevel(false)
		return nil
	}
	p.next()
	var params []ast.Param
	for t := p.peekInExpr(); t.Kind == token.LIDENT && t.Pos().Line == nameT.Pos().Line; t = p.peekInExpr() {
		p.next()
		params = append(params, ast.Param{Name: t.Text, Sp: t.Span})
	}
	first := p.peek()
	if first.Kind == token.EOF {
		p.errorAt(nameT.Span, TitleUnexpectedEOF, "This class declaration needs at least one method signature.")
		return nil
	}
	if first.Pos().Col <= 1 {
		p.errorAt(first.Span, "SYNTAX PROBLEM", "Class methods must be indented below the class name.")
		return nil
	}
	if len(params) != 1 {
		p.errorAt(nameT.Span, "CLASS PARAMETER", "A class requires exactly one type parameter.")
		p.recoverToTopLevel(false)
		return nil
	}
	d := &ast.ClassDecl{Name: nameT.Text, NameSpan: nameT.Span, Param: params[0]}
	declared := map[string]bool{}
	// Equations group only when adjacent: a signature between two rows
	// separates them into distinct defaults.
	afterDefault := false
	col := first.Pos().Col
	p.lay.push(ctxBlock, col)
	defer p.lay.pop()
	for p.peek().Kind != token.EOF && p.peek().Pos().Col > 1 && p.peek().Pos().Col <= col {
		p.stmtStart = p.pos
		name, nameSpan, ok := p.parseMethodName("SYNTAX PROBLEM",
			"I expect a method signature here, like `show : a -> String`.")
		if !ok {
			p.recoverToTopLevel(false)
			return nil
		}
		if p.peekInExpr().Kind == token.COLON {
			p.next()
			ty := p.parseTypeExpr()
			if ty == nil {
				p.recoverToTopLevel(false)
				return nil
			}
			if p.peekInExpr().Kind == token.EQ {
				p.errorAt(p.peekInExpr().Span, "CLASS METHOD", "A default implementation goes on its own line below the signature, like an instance method.")
				p.recoverToTopLevel(false)
				return nil
			}
			declared[name] = true
			afterDefault = false
			d.Methods = append(d.Methods, ast.OpSig{Name: name, NameSpan: nameSpan, Type: ty})
		} else {
			params := p.parseValueParams(nameSpan)
			eq := p.peekInExpr()
			if !p.expect(token.EQ, "I expect `:` and a type for a method signature, or parameters and `=` for a default implementation.") {
				p.recoverToTopLevel(false)
				return nil
			}
			body := p.parseBindBody(eq)
			if body == nil {
				p.recoverToTopLevel(false)
				return nil
			}
			if !declared[name] {
				p.errorAt(nameSpan, "CLASS METHOD", "A default implementation of `"+ast.Spelling(name)+"` must follow its signature in this class.")
			}
			next := &ast.ValueDecl{Name: name, NameSpan: nameSpan, Params: params, Body: body}
			if afterDefault {
				d.Defaults = p.groupMethod(d.Defaults, next)
			} else {
				d.Defaults = append(d.Defaults, next)
			}
			afterDefault = true
		}
		if nt := p.peek(); nt.Kind != token.EOF && nt.Pos().Col > col {
			p.errorAt(nt.Span, "SYNTAX PROBLEM", "This line is indented past the first method and reads as a continuation.")
			p.recoverToTopLevel(false)
			return nil
		}
	}
	return d
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
	for p.peek().Kind != token.EOF && p.peek().Pos().Col > 1 && p.peek().Pos().Col <= col {
		p.stmtStart = p.pos
		n := p.peekInExpr()
		if n.Kind != token.LIDENT && n.Kind != token.LPAREN {
			p.errorAt(n.Span, "INSTANCE METHOD", "I expect a method definition.")
			p.recoverToTopLevel(false)
			return nil
		}
		name, nameSpan, ok := p.parseMethodName("INSTANCE METHOD",
			"I expect a method name here, like `show` or `(==)`.")
		if !ok {
			p.recoverToTopLevel(false)
			return nil
		}
		params := p.parseValueParams(nameSpan)
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
		d.Methods = p.groupMethod(d.Methods, &ast.ValueDecl{Name: name, NameSpan: nameSpan, Params: params, Body: body})
	}
	return d
}
