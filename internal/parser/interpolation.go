package parser

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/token"
)

func (p *parser) parseStringInterpolation() ast.Expr {
	open := p.next()
	p.usesInterpolation = true
	result := &ast.StringInterpolation{Sp: open.Span}
	p.exprParenDepth++
	defer func() { p.exprParenDepth-- }()
	p.lay.push(ctxParen, 0)
	defer p.lay.pop()
	for {
		text := p.peek()
		if text.Kind != token.STRING_TEXT {
			p.errorAt(text.Span, "UNCLOSED STRING", "I expect the rest of this interpolated string.")
			return nil
		}
		p.next()
		result.Segments = append(result.Segments, lexer.UnescapeText(text.Text))
		result.SegmentSpans = append(result.SegmentSpans, text.Span)
		if p.peek().Kind == token.STRING_END {
			result.Sp = result.Sp.Merge(p.next().Span)
			return result
		}
		if p.peek().Kind != token.INTERPOLATION_BEGIN {
			p.errorAt(p.peek().Span, "UNCLOSED STRING", "I expect an interpolation expression or the closing quote.")
			return nil
		}
		hole := p.next()
		if p.peek().Kind == token.INTERPOLATION_END {
			p.errorAt(hole.Span.Merge(p.next().Span), "EMPTY INTERPOLATION", "Put an expression between `#{` and `}`.")
			return nil
		}
		expr := p.parseExpr()
		if expr == nil {
			return nil
		}
		if p.peek().Kind != token.INTERPOLATION_END {
			p.errorAt(p.peek().Span, "UNCLOSED INTERPOLATION", "I expect `}` after the interpolation expression.")
			return nil
		}
		p.next()
		result.Exprs = append(result.Exprs, expr)
	}
}
