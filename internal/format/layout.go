package format

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
)

// The layout constructs are the ones whose meaning is carried by columns:
// a block's statements align, a `case` or `handle` aligns its branches, and an
// `if` anchors its `then` and `else` at its own column. Rendering them is
// therefore a matter of choosing indents rather than of fitting text to a
// width, and the indent a construct hands its children is always deeper than
// its own — which is what keeps the output parsing as the input did.

// renderExpr writes e continuing the current line. Lines after the first start
// at ind or deeper. It reports false when it cannot render e faithfully, and
// the enclosing declaration is then copied verbatim.
func (p *printer) renderExpr(e ast.Expr, ind int) bool {
	if !brokeWithin(e.Span()) {
		s, ok := exprInline(e)
		if !ok {
			return false
		}
		p.emit(s)
		return true
	}
	switch e := e.(type) {
	case *ast.Block:
		return p.renderBlock(e, ind)
	case *ast.Case:
		return p.renderCase(e, ind)
	case *ast.Handle:
		return p.renderHandle(e, ind)
	case *ast.If:
		return p.renderIf(e, ind)
	case *ast.Lambda:
		return p.renderLambda(e, ind)
	}
	// A break inside an application, an operator run, or a record literal is
	// not reproduced yet.
	return false
}

// renderBlock writes statement lines followed by the result, all aligned at
// ind — the column the offside rule reads to decide where the block ends.
func (p *printer) renderBlock(b *ast.Block, ind int) bool {
	for _, item := range blockItems(b) {
		if item.BindIndex < 0 {
			p.start(ind)
			if !p.renderExpr(item.Expr, ind) {
				return false
			}
			continue
		}
		if !p.renderBind(b.Binds[item.BindIndex], ind) {
			return false
		}
	}
	p.start(ind)
	return p.renderExpr(b.Result, ind)
}

// blockItems presents the two shapes a block can take — an ordered item list,
// or the older binding-only list — as one ordered list.
func blockItems(b *ast.Block) []ast.BlockItem {
	if b.Items != nil {
		return b.Items
	}
	items := make([]ast.BlockItem, len(b.Binds))
	for i := range b.Binds {
		items[i] = ast.BlockItem{BindIndex: i}
	}
	return items
}

// renderBind writes a local binding: an annotation line when there is one,
// then one line per equation.
func (p *printer) renderBind(lb ast.LocalBind, ind int) bool {
	if lb.Ann != nil {
		p.line(ind, declName(lb.Name)+" : "+annotationText(lb.Ann))
	}
	if lb.Pattern != nil {
		pat, ok := patternInline(lb.Pattern)
		if !ok {
			return false
		}
		p.start(ind)
		p.emit(pat)
		return p.renderAssigned(lb.Body, ind)
	}
	for _, eq := range localEquations(lb) {
		head, ok := equationHead(lb.Name, eq)
		if !ok {
			return false
		}
		p.start(ind)
		p.emit(head)
		if !p.renderAssigned(eq.Body, ind) {
			return false
		}
	}
	return true
}

// localEquations presents a local binding's single-row and grouped shapes as
// one list, the way equations does for a declaration.
func localEquations(lb ast.LocalBind) []ast.Equation {
	if len(lb.Equations) > 0 {
		return lb.Equations
	}
	return []ast.Equation{{Params: lb.Params, Body: lb.Body, NameSpan: lb.NameSpan}}
}

// renderAssigned writes ` = body`, moving the body to its own deeper line when
// that is where the author put it.
func (p *printer) renderAssigned(body ast.Expr, ind int) bool {
	if body == nil {
		return false
	}
	if bodyOnOwnLine(body.Span().File, body.Span().Start) {
		p.emit(" =")
		p.start(ind + Indent)
		return p.renderExpr(body, ind+Indent)
	}
	p.emit(" = ")
	return p.renderExpr(body, ind)
}

// renderCase writes `case scrutinee of` and one branch per line, aligned one
// level in. A branch body that the author moved below its arrow goes a further
// level in, which is what keeps it from reading as the next branch.
func (p *printer) renderCase(c *ast.Case, ind int) bool {
	scrutinee, ok := exprInline(c.Scrutinee)
	if !ok {
		return false
	}
	p.emit("case " + scrutinee + " of")
	branchInd := ind + Indent
	for _, br := range c.Branches {
		pat, patOK := patternInline(br.Pattern)
		if !patOK {
			return false
		}
		p.start(branchInd)
		p.emit(pat)
		if !p.renderArrow(br.Body, branchInd) {
			return false
		}
	}
	return true
}

// renderArrow writes ` -> body`, with the same rule renderAssigned uses.
func (p *printer) renderArrow(body ast.Expr, ind int) bool {
	if body == nil {
		return false
	}
	if bodyOnOwnLine(body.Span().File, body.Span().Start) {
		p.emit(" ->")
		p.start(ind + Indent)
		return p.renderExpr(body, ind+Indent)
	}
	p.emit(" -> ")
	return p.renderExpr(body, ind)
}

// renderHandle writes `handle body [with name = initial] of` and its clauses,
// which align exactly as a case's branches do.
func (p *printer) renderHandle(h *ast.Handle, ind int) bool {
	body, ok := exprInline(h.Body)
	if !ok {
		return false
	}
	head := "handle " + body
	if h.State != nil {
		initial, initOK := exprInline(h.State.Initial)
		if !initOK {
			return false
		}
		head += " with " + h.State.Name + " = " + initial
	}
	p.emit(head + " of")

	clauseInd := ind + Indent
	for _, cl := range h.Clauses {
		params, paramsOK := patternsInline(cl.Params, patternArgInline)
		if !paramsOK {
			return false
		}
		name := cl.Op
		if params != "" {
			name += " " + params
		}
		p.start(clauseInd)
		p.emit(name)
		if !p.renderArrow(cl.Body, clauseInd) {
			return false
		}
	}
	if h.Return != nil {
		pat, patOK := patternInline(h.Return.Param)
		if !patOK {
			return false
		}
		p.start(clauseInd)
		p.emit("return " + pat)
		if !p.renderArrow(h.Return.Body, clauseInd) {
			return false
		}
	}
	return true
}

// renderIf writes the broken form: the condition and `then` on one line, each
// arm a level in, and `else` back at the `if`'s own column, which is where the
// layout rule anchors it. An `else if` continues the same chain rather than
// staircasing rightward.
func (p *printer) renderIf(e *ast.If, ind int) bool {
	cond, ok := exprInline(e.Cond)
	if !ok {
		return false
	}
	f := e.Sp.File
	p.emit("if " + cond)
	// `then` may lead its own line. When it does it is anchored at the `if`'s
	// own column, which is where `else` goes too, so a chain of arms lines up
	// rather than staircasing.
	if keywordOnOwnLine(f, e.Cond.Span().End) {
		p.start(ind)
	} else {
		p.emit(" ")
	}
	p.emit("then")
	if !p.renderArm(e.Then, ind) {
		return false
	}

	// `else` goes back to the `if`'s own column when the author put it on a
	// line of its own, which is where the layout rule anchors it.
	if e.Then.Span().EndPos().Line != e.Else.Span().StartPos().Line {
		p.start(ind)
	} else {
		p.emit(" ")
	}
	p.emit("else")

	// An `else if` continues the same chain rather than staircasing rightward.
	if inner, isIf := e.Else.(*ast.If); isIf && !bodyOnOwnLine(f, inner.Sp.Start) {
		p.emit(" ")
		if !brokeWithin(inner.Span()) {
			s, inlineOK := exprInline(inner)
			if !inlineOK {
				return false
			}
			p.emit(s)
			return true
		}
		return p.renderIf(inner, ind)
	}
	return p.renderArm(e.Else, ind)
}

// renderArm writes one arm of an `if`, a level in when the author moved it
// below its keyword and on the same line when not.
func (p *printer) renderArm(arm ast.Expr, ind int) bool {
	if bodyOnOwnLine(arm.Span().File, arm.Span().Start) {
		p.start(ind + Indent)
		return p.renderExpr(arm, ind+Indent)
	}
	p.emit(" ")
	return p.renderExpr(arm, ind)
}

func (p *printer) renderLambda(e *ast.Lambda, ind int) bool {
	params, ok := patternsInline(e.Params, patternArgInline)
	if !ok {
		return false
	}
	p.emit("\\" + params)
	return p.renderArrow(e.Body, ind)
}

// bodyOnOwnLine reports whether only whitespace and a newline separate the body
// from whatever introduced it. It reads the source rather than comparing spans
// because a declaration's NameSpan points at the name on its annotation line,
// not at the one on its definition line.
func bodyOnOwnLine(f *source.File, bodyStart int) bool {
	for i := bodyStart - 1; i >= 0; i-- {
		switch f.Content[i] {
		case ' ', '\t', '\r':
		case '\n':
			return true
		default:
			return false
		}
	}
	return false
}

// writtenAgainst reports whether the token at offset was written with no space
// before it, which is what makes `f()` bind tighter than `f ()`.
func writtenAgainst(f *source.File, at int) bool {
	if f == nil || at <= 0 || at > len(f.Content) {
		return false
	}
	switch f.Content[at-1] {
	case ' ', '\t', '\n', '\r':
		return false
	}
	return true
}

// keywordOnOwnLine reports whether the next token after offset was written on
// a line of its own. The AST records no span for `then`, so the source is what
// says where the author put it.
func keywordOnOwnLine(f *source.File, from int) bool {
	if f == nil {
		return false
	}
	for i := from; i < len(f.Content); i++ {
		switch f.Content[i] {
		case ' ', '\t', '\r':
		case '\n':
			return true
		default:
			return false
		}
	}
	return false
}
