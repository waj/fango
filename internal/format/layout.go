package format

import (
	"bytes"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

// The layout constructs are the ones whose meaning is carried by columns:
// a block's statements align, a `case` or `handle` aligns its branches, and an
// `if` anchors its `then` and `else` at its own column. Rendering them is
// therefore a matter of choosing indents rather than of fitting text to a
// width. Layout children indent below their owner.

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
	case *ast.Ctor:
		// An empty bracket list lowers to a constructor rather than an
		// application. It has no members to lay out, so its compact spelling is
		// the only block form.
		if e.Sugared && e.Name == "List.Nil" {
			p.emit("[]")
			return true
		}
		return false
	case *ast.Block:
		// A wrapped explicit block keeps its source line structure verbatim
		// until the structural renderer can place semicolons and comments as
		// anchors of their own. Returning false activates the formatter's
		// declaration-level safe fallback; one-line explicit blocks are handled
		// by exprInline above.
		if len(e.Semicolons) > 0 {
			return false
		}
		return p.renderBlock(e, ind)
	case *ast.Case:
		return p.renderCase(e, ind)
	case *ast.Handle:
		return p.renderHandle(e, ind)
	case *ast.If:
		return p.renderIf(e, ind)
	case *ast.Lambda:
		return p.renderLambda(e, ind)
	case *ast.Quote:
		return p.renderQuote(e, ind)
	case *ast.OpChain:
		return p.renderOpChain(e, ind)
	case *ast.App:
		if lambda, ok := withLambda(e); ok {
			return p.renderWith(e.Fn, lambda, ind)
		}
		if elems, tail, ok := asList(e); ok {
			return p.renderList(elems, tail, ind)
		}
		if elems, ok := asTuple(e); ok {
			return p.renderTuple(elems, ind)
		}
		return p.renderApp(e, ind)
	case *ast.RecordLit:
		return p.renderRecordLit(e, ind)
	case *ast.RecordUpdate:
		return p.renderRecordUpdate(e, ind)
	}
	// A break inside an application, an operator run, or a record literal is
	// not reproduced yet.
	return false
}

// renderQuote preserves the body's line breaks and gives a multiline close
// the same alignment as other expression delimiters. The AST retains both
// backticks, including when grouping parentheses inside the body were omitted.
func (p *printer) renderQuote(e *ast.Quote, ind int) bool {
	open, close, ok := p.delimiterTokens(e.Sp, token.LQUOTE, token.RQUOTE)
	if !ok {
		return false
	}
	f := e.Sp.File
	base := ind
	p.emit("`")
	if brokeBetween(f, p.toks[open].Span.End, e.Body.Span().Start) {
		if !p.placeBefore(e.Body.Span().Start, ind+Indent) {
			return false
		}
		p.start(ind + Indent)
	}
	if !p.renderExpr(e.Body, ind+Indent) {
		return false
	}
	if !p.placeBefore(p.toks[close].Span.Start, ind+Indent) {
		return false
	}
	if brokeBetween(f, e.Body.Span().End, p.toks[close].Span.Start) {
		p.start(base)
	}
	p.emit("`")
	return true
}

// renderBlock writes statement lines followed by the result, all aligned at
// ind — the column the offside rule reads to decide where the block ends.
func (p *printer) renderBlock(b *ast.Block, ind int) bool {
	for _, item := range blockItems(b) {
		if item.BindIndex < 0 {
			if !p.placeBefore(item.Expr.Span().Start, ind) {
				return false
			}
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
	if b.MissingResultAt.File != nil {
		return true
	}
	if !p.placeBefore(b.Result.Span().Start, ind) {
		return false
	}
	p.start(ind)
	return p.renderExpr(b.Result, ind)
}

// withLambda recognizes the expansion of a `with` block item: a head applied
// to the callback the parser built from the rest of the block.
func withLambda(e *ast.App) (*ast.Lambda, bool) {
	lambda, ok := e.Arg.(*ast.Lambda)
	return lambda, ok && lambda.With != nil
}

// renderWith writes a `with` item and then the rest of its block at the same
// column, which is where the parser found them.
func (p *printer) renderWith(head ast.Expr, lambda *ast.Lambda, ind int) bool {
	if lambda.With.Semi.File != nil {
		return false
	}
	p.emit("with ")
	if lambda.With.Arrow.File != nil {
		params, ok := patternsInline(lambda.Params)
		if !ok {
			return false
		}
		p.emit(params + " <- ")
	}
	if !p.renderExpr(head, ind) {
		return false
	}
	if rest, ok := lambda.Body.(*ast.Block); ok {
		if len(rest.Semicolons) > 0 {
			return false
		}
		return p.renderBlock(rest, ind)
	}
	if !p.placeBefore(lambda.Body.Span().Start, ind) {
		return false
	}
	p.start(ind)
	return p.renderExpr(lambda.Body, ind)
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
	if !p.placeBefore(bindStart(lb), ind) {
		return false
	}
	if lb.Ann != nil {
		p.line(ind, declName(lb.Name)+" : "+annotationText(lb.Ann))
	}
	if lb.Pattern != nil {
		p.start(ind)
		if !p.renderPattern(lb.Pattern, ind) {
			return false
		}
		return p.renderAssigned(lb.Body, ind)
	}
	for _, eq := range localEquations(lb) {
		p.start(ind)
		if !p.renderEquationHead(lb.Name, eq, ind) {
			return false
		}
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
	ind = p.lineIndent(ind)
	if bodyOnOwnLine(body.Span().File, body.Span().Start) {
		p.emit(" =")
		if !p.placeBefore(body.Span().Start, ind+Indent) {
			return false
		}
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
		if !p.placeBefore(br.Pattern.Span().Start, branchInd) {
			return false
		}
		p.start(branchInd)
		if !p.renderPattern(br.Pattern, branchInd) {
			return false
		}
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
	ind = p.lineIndent(ind)
	if bodyOnOwnLine(body.Span().File, body.Span().Start) {
		p.emit(" ->")
		if !p.placeBefore(body.Span().Start, ind+Indent) {
			return false
		}
		p.start(ind + Indent)
		return p.renderExpr(body, ind+Indent)
	}
	p.emit(" -> ")
	return p.renderExpr(body, ind)
}

// renderHandle writes `handle subject [with name = initial] on` and its
// clauses, which align exactly as a case's branches do. A subject that spans
// lines, or that the author put below `handle`, takes its own lines a level
// in, and `with` and `on` return to the `handle`'s column. That column must be
// where a reader finds the `handle`, so a block-form `handle` that would
// follow other text starts its own line a level in from that text instead.
// Opening parentheses alone are not such text: the `handle` after them still
// leads its line, and its own column is the anchor.
func (p *printer) renderHandle(h *ast.Handle, ind int) bool {
	state := ""
	if h.State != nil {
		initial, ok := exprInline(h.State.Initial)
		if !ok {
			return false
		}
		state = "with " + h.State.Name + " = " + initial + " "
	}
	if bodyOnOwnLine(h.Sp.File, h.Body.Span().Start) || brokeWithin(h.Body.Span()) {
		if lead := strings.TrimRight(string(p.cur), " "); p.open && strings.Trim(lead, "(") == "" {
			ind = p.column()
		} else if p.open {
			ind = p.ind + Indent
			p.start(ind)
		}
		p.emit("handle")
		if !p.placeBefore(h.Body.Span().Start, ind+Indent) {
			return false
		}
		p.start(ind + Indent)
		if !p.renderExpr(h.Body, ind+Indent) {
			return false
		}
		p.start(ind)
		p.emit(state + "on")
	} else {
		body, ok := exprInline(h.Body)
		if !ok {
			return false
		}
		p.emit("handle " + body + " " + state + "on")
	}

	clauseInd := ind + Indent
	for _, cl := range h.Clauses {
		rows := cl.Equations
		if len(rows) == 0 {
			rows = []ast.Equation{{Params: cl.Params, Body: cl.Body, NameSpan: cl.OpSpan}}
		}
		if !p.renderClauseRows(cl.Op, rows, clauseInd) {
			return false
		}
	}
	if h.Return != nil {
		rows := h.Return.Equations
		if len(rows) == 0 {
			rows = []ast.Equation{{Params: []ast.Pattern{h.Return.Param}, Body: h.Return.Body, NameSpan: h.Return.Sp}}
		}
		if !p.renderClauseRows("return", rows, clauseInd) {
			return false
		}
	}
	return true
}

// renderClauseRows writes the rows of one handler clause group, each as
// `op patterns -> body` at the clause indentation. Adjacent rows for one
// operation, or for `return`, are grouped by the parser into Equations; a
// lone clause arrives as a single row.
func (p *printer) renderClauseRows(op string, rows []ast.Equation, ind int) bool {
	for _, row := range rows {
		if !p.placeBefore(row.NameSpan.Start, ind) {
			return false
		}
		p.start(ind)
		p.emit(op)
		if !p.renderPatternArgs(row.Params, ind) {
			return false
		}
		if !p.renderArrow(row.Body, ind) {
			return false
		}
	}
	return true
}

// renderIf writes the broken form: the condition and `then` on one line, each
// arm a level in, and `else` back at the `if`'s own column, which is where the
// layout rule anchors it. An `if` that follows other text on its line, such as
// a binding's right-hand side, anchors a level in from that line instead: at
// the line's own indent, `else` would read as the enclosing block's next
// statement. An `else if` continues the same chain rather than staircasing
// rightward.
func (p *printer) renderIf(e *ast.If, ind int) bool {
	if p.open && len(p.cur) > 0 && p.keywordStartsLine(e) {
		ind = p.ind + Indent
	}
	return p.renderIfAt(e, ind)
}

// keywordStartsLine reports whether renderIfAt puts a `then` or an `else` of
// this `if`, or of the `else if` chain it continues, at the start of a line.
func (p *printer) keywordStartsLine(e *ast.If) bool {
	f := e.Sp.File
	if keywordOnOwnLine(f, e.Cond.Span().End) || p.elseStartsLine(e) {
		return true
	}
	if inner, isIf := e.Else.(*ast.If); isIf && !bodyOnOwnLine(f, inner.Sp.Start) && brokeWithin(inner.Span()) {
		return p.keywordStartsLine(inner)
	}
	return false
}

// elseStartsLine reports whether the author put this `if`'s `else` keyword at
// the start of a line. An `else` that ends the `then` line keeps its place
// even when its body follows on the next lines. The keyword is found among
// the tokens, since an arm's span can end before a closing parenthesis.
func (p *printer) elseStartsLine(e *ast.If) bool {
	for _, t := range p.toks {
		if t.Span.Start < e.Then.Span().End {
			continue
		}
		if t.Span.Start >= e.Else.Span().Start {
			break
		}
		if t.Kind == token.KwElse {
			return startsLine(e.Sp.File, t.Span.Start)
		}
	}
	return e.Then.Span().EndPos().Line != e.Else.Span().StartPos().Line
}

// startsLine reports whether only spaces precede pos on its line.
func startsLine(f *source.File, pos int) bool {
	for i := pos - 1; i >= 0; i-- {
		switch f.Content[i] {
		case ' ', '\t':
		case '\n':
			return true
		default:
			return false
		}
	}
	return true
}

func (p *printer) renderIfAt(e *ast.If, ind int) bool {
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

	// `else` goes back to the `if`'s own column when the author put the keyword
	// at the start of a line, which is where the layout rule anchors it.
	if p.elseStartsLine(e) {
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
		return p.renderIfAt(inner, ind)
	}
	return p.renderArm(e.Else, ind)
}

// renderArm writes one arm of an `if`, a level in when the author moved it
// below its keyword and on the same line when not.
func (p *printer) renderArm(arm ast.Expr, ind int) bool {
	if bodyOnOwnLine(arm.Span().File, arm.Span().Start) {
		if !p.placeBefore(arm.Span().Start, ind+Indent) {
			return false
		}
		p.start(ind + Indent)
		return p.renderExpr(arm, ind+Indent)
	}
	p.emit(" ")
	return p.renderExpr(arm, ind)
}

func (p *printer) renderLambda(e *ast.Lambda, ind int) bool {
	return p.renderLambdaAt(e, ind, p.lineIndent(ind))
}

func (p *printer) renderLambdaAt(e *ast.Lambda, ind, closeIndent int) bool {
	p.emit("{")
	unit := false
	if len(e.Params) == 1 {
		_, unit = e.Params[0].(*ast.PUnit)
	}
	if !unit {
		p.emit(" ")
		for i, param := range e.Params {
			if i > 0 {
				p.emit(" ")
			}
			if !p.renderPatternArg(param, ind) {
				return false
			}
		}
		p.emit(" ->")
	}
	// A break solely before `}` does not make the lambda multiline. When the
	// body itself spans lines, start it below the arrow (or opening brace for
	// the Unit form) so the lambda has one consistent layout.
	bodyWasBelow := bodyOnOwnLine(e.Body.Span().File, e.Body.Span().Start)
	bodyBroken := bodyWasBelow || brokeWithin(e.Body.Span())
	if bodyBroken {
		if !p.placeBefore(e.Body.Span().Start, ind+Indent) {
			return false
		}
		p.start(ind + Indent)
	} else {
		p.emit(" ")
	}
	if !p.renderExpr(e.Body, ind+Indent) {
		return false
	}
	// A multiline lambda closes at the indentation of its opening line.
	// Consecutive closers stay together when their openers share that line.
	if bodyBroken {
		open, close, ok := p.delimiterTokens(e.Span(), token.LBRACE, token.RBRACE)
		if !ok || (!bodyWasBelow && brokeWithin(e.Body.Span())) || !p.closeSharesLine(open, close) {
			p.start(closeIndent)
		}
	} else {
		p.emit(" ")
	}
	p.emit("}")
	return true
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

// renderOpChain writes a run of infix operators across the lines the author
// used. Where the glyph sits is the author's too: a run broken before its
// operator keeps the operator leading, one broken after keeps it trailing.
// Fixity is not consulted — it is not available here, and it does not answer
// where a glyph goes.
func (p *printer) renderOpChain(e *ast.OpChain, ind int) bool {
	f := e.Span().File
	contInd := ind + Indent
	for i, operand := range e.Operands {
		if i > 0 {
			op := e.Ops[i-1]
			if brokeBetween(f, e.Operands[i-1].Span().End, op.Sp.Start) {
				p.start(contInd)
			} else {
				p.emit(" ")
			}
			p.emit(op.Op)
			if brokeBetween(f, op.Sp.End, operand.Span().Start) {
				p.start(contInd)
			} else {
				p.emit(" ")
			}
		}
		if brokeWithin(operand.Span()) {
			return false
		}
		s, ok := exprOperandInline(operand)
		if !ok {
			return false
		}
		p.emit(s)
	}
	return true
}

// renderApp writes an application whose arguments the author spread over
// several lines, each argument staying on the line it was written on.
func (p *printer) renderApp(e *ast.App, ind int) bool {
	fn, args := spine(e)
	if r, isResume := fn.(*ast.Resume); isResume && r.NextState != nil {
		return false
	}
	head, ok := exprAtomInline(fn)
	if !ok {
		return false
	}
	p.emit(head)

	f := e.Span().File
	contInd := ind + Indent
	prevEnd := fn.Span().End
	for _, a := range args {
		// An argument with its own line structure is rendered rather than
		// inlined, so a nest stays a nest. A multiline parenthesis closes
		// on its own line; closes share a line when their openers do.
		if brokeWithin(a.Span()) {
			if atomic(a) {
				onOwnLine := brokeBetween(f, prevEnd, a.Span().Start)
				argInd := ind
				if onOwnLine {
					p.start(contInd)
					argInd = contInd
				} else {
					p.emit(" ")
				}
				if !p.renderExpr(a, argInd) {
					return false
				}
			} else {
				open, close, hasClose := p.argumentClose(prevEnd, a.Span().Start)
				openStart := a.Span().Start
				if hasClose {
					openStart = p.toks[open].Span.Start
				}
				if brokeBetween(f, prevEnd, openStart) {
					p.start(contInd)
				} else {
					p.emit(" ")
				}
				openIndent := p.lineIndent(ind)
				p.emit("(")
				if hasClose && brokeBetween(f, p.toks[open].Span.End, a.Span().Start) {
					p.start(contInd)
				}
				if !p.renderExpr(a, contInd) {
					return false
				}
				if !hasClose || (p.toks[open].Pos().Line < p.toks[close].Pos().Line &&
					!p.closeSharesLine(open, close)) {
					p.start(openIndent)
				}
				p.emit(")")
				if hasClose {
					prevEnd = p.toks[close].Span.End
					continue
				}
			}
			prevEnd = a.Span().End
			continue
		}
		if brokeBetween(f, prevEnd, a.Span().Start) {
			p.start(contInd)
		} else {
			p.emit(" ")
		}
		s, argOK := exprAtomInline(a)
		if !argOK {
			return false
		}
		p.emit(s)
		prevEnd = a.Span().End
	}
	return true
}

// argumentClose follows the source opening parenthesis for an application
// argument. Expression spans omit grouping parentheses, so the matching token
// supplies both the source break and the end of the argument for the next one.
func (p *printer) argumentClose(prevEnd, argStart int) (int, int, bool) {
	open := -1
	for i, t := range p.toks {
		if t.Span.Start >= argStart {
			break
		}
		if t.Kind == token.LPAREN && t.Span.Start >= prevEnd {
			open = i
		}
	}
	if open < 0 {
		return 0, 0, false
	}
	depth := 0
	for i := open; i < len(p.toks); i++ {
		switch p.toks[i].Kind {
		case token.LPAREN:
			depth++
		case token.RPAREN:
			depth--
			if depth == 0 {
				return open, i, true
			}
		}
	}
	return 0, 0, false
}

// delimiterTokens finds the delimiters retained in an expression's span.
func (p *printer) delimiterTokens(sp source.Span, left, right token.Kind) (int, int, bool) {
	open, close := -1, -1
	for i, t := range p.toks {
		if t.Span.Start == sp.Start && t.Kind == left {
			open = i
		}
		if t.Span.End == sp.End && t.Kind == right {
			close = i
		}
		if t.Span.Start >= sp.End {
			break
		}
	}
	return open, close, open >= 0 && close > open
}

// closeSharesLine keeps consecutive parenthesis and brace closes together
// only when their openers were written on the same source line.
func (p *printer) closeSharesLine(open, close int) bool {
	if close == 0 || len(p.cur) == 0 {
		return false
	}
	previous := p.toks[close-1].Kind
	if previous != token.RPAREN && previous != token.RBRACE {
		return false
	}
	for _, b := range p.cur {
		if b != ')' && b != '}' {
			return false
		}
	}
	var expected []token.Kind
	for i := close - 1; i >= 0; i-- {
		switch p.toks[i].Kind {
		case token.RPAREN:
			expected = append(expected, token.LPAREN)
		case token.RBRACE:
			expected = append(expected, token.LBRACE)
		case token.RBRACKET:
			expected = append(expected, token.LBRACKET)
		case token.LPAREN, token.LBRACE, token.LBRACKET:
			if len(expected) == 0 || expected[len(expected)-1] != p.toks[i].Kind {
				return false
			}
			expected = expected[:len(expected)-1]
			if len(expected) == 0 {
				return p.toks[i].Pos().Line == p.toks[open].Pos().Line
			}
		}
	}
	return false
}

// renderList writes a broken bracket list in the same leading-comma block
// form as records. Source line breaks choose which adjacent elements share a
// row; the formatter only normalizes their spacing and indentation.
func (p *printer) renderList(elems []ast.Expr, tail ast.Expr, ind int) bool {
	if len(elems) == 0 {
		p.emit("[]")
		return true
	}
	base := p.column()
	p.emit("[ ")
	prevEnd := elems[0].Span().Start
	for i, elem := range elems {
		if i > 0 {
			if brokeBetween(elem.Span().File, prevEnd, elem.Span().Start) {
				p.start(base)
			}
			p.emit(", ")
		}
		if !p.renderContainerValue(elem, base, p.column()) {
			return false
		}
		prevEnd = elem.Span().End
	}
	if tail != nil {
		if brokeBetween(tail.Span().File, prevEnd, tail.Span().Start) {
			p.start(base)
			p.emit("| ")
		} else {
			p.emit(" | ")
		}
		if !p.renderContainerValue(tail, base, p.column()) {
			return false
		}
	}
	p.start(base)
	p.emit("]")
	return true
}

// renderTuple is renderList without its optional tail and square brackets.
func (p *printer) renderTuple(elems []ast.Expr, ind int) bool {
	base := p.column()
	p.emit("( ")
	prevEnd := elems[0].Span().Start
	for i, elem := range elems {
		if i > 0 {
			if brokeBetween(elem.Span().File, prevEnd, elem.Span().Start) {
				p.start(base)
			}
			p.emit(", ")
		}
		if !p.renderContainerValue(elem, base, p.column()) {
			return false
		}
		prevEnd = elem.Span().End
	}
	p.start(base)
	p.emit(")")
	return true
}

// A direct lambda value closes at the start of its list item, tuple item, or
// record field. The surrounding container keeps its own closing indentation.
func (p *printer) renderContainerValue(e ast.Expr, ind, valueIndent int) bool {
	if lambda, ok := e.(*ast.Lambda); ok && brokeWithin(e.Span()) {
		return p.renderLambdaAt(lambda, ind, valueIndent)
	}
	return p.renderExpr(e, ind)
}

// brokeBetween reports whether the author put a newline between two offsets.
func brokeBetween(f *source.File, from, to int) bool {
	if f == nil || from < 0 || to > len(f.Content) || from > to {
		return false
	}
	return bytes.ContainsRune(f.Content[from:to], '\n')
}

// renderRecordLit writes a record literal the author spread over several
// lines, in the leading-comma block form the standard library already uses.
func (p *printer) renderRecordLit(e *ast.RecordLit, ind int) bool {
	if e.Name != "" {
		// The fields go one level in from the line the constructor name sits
		// on, which is not the same as the caller's continuation indent when
		// the name was written mid-line.
		lineInd := p.ind
		p.emit(e.Name + " ")
		return p.recordFieldBlock(e.Fields, "{ ", lineInd+Indent)
	}
	return p.recordFieldBlock(e.Fields, "{ ", ind)
}

// renderRecordUpdate writes `{ receiver | field = value, … }` across lines.
// The `|` sits one level in rather than at the brace, because at the brace
// column the offside rule reads it as ending the construct.
func (p *printer) renderRecordUpdate(e *ast.RecordUpdate, ind int) bool {
	recv, ok := exprAtomInline(e.Record)
	if !ok || brokeWithin(e.Record.Span()) {
		return false
	}
	p.emit("{ " + recv)
	for i, f := range e.Fields {
		value, valueOK := exprInline(f.Value)
		if !valueOK || brokeWithin(f.Value.Span()) {
			return false
		}
		lead := ", "
		if i == 0 {
			lead = "| "
		}
		p.start(ind + Indent)
		p.emit(lead + f.Name + " = " + value)
	}
	p.start(ind)
	p.emit("}")
	return true
}

// recordFieldBlock writes one field per line, opened by lead and closed by a
// brace back at the block's own column.
func (p *printer) recordFieldBlock(fields []ast.RecordExprField, lead string, ind int) bool {
	for i, f := range fields {
		open := ", "
		if i == 0 {
			open = lead
		}
		p.start(ind)
		p.emit(open)
		valueIndent := p.column()
		p.emit(f.Name + " = ")
		if !p.renderContainerValue(f.Value, ind, valueIndent) {
			return false
		}
	}
	p.start(ind)
	p.emit("}")
	return true
}

// bindStart is where a local binding begins in source, which is its annotation
// line when it has one.
func bindStart(lb ast.LocalBind) int {
	if lb.Ann != nil && lb.Ann.Sp.File != nil {
		return lb.NameSpan.Start
	}
	if lb.Pattern != nil {
		return lb.Pattern.Span().Start
	}
	return lb.NameSpan.Start
}
