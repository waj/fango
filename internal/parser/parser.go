// Package parser is a hand-written recursive-descent parser with a Pratt
// loop for operator expressions. It enforces the offside rule via token
// columns (layout.go); the lexer is layout-oblivious.
package parser

import (
	"strconv"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/lexer"
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

	// stmtStart is the index of a token allowed to sit exactly at the
	// innermost layout column: a block statement's opening token (and, in
	// S4, a case branch's first pattern token). Everywhere else, a token
	// at the column is a sibling boundary, not expression content.
	stmtStart int
}

// Parse parses a whole module.
func Parse(toks []token.Token, f *source.File) (*ast.Module, []diag.Error) {
	p := &parser{f: f, toks: toks, stmtStart: -1}
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
	p := &parser{f: f, toks: toks, stmtStart: -1}
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
	if t.Kind == token.KwType {
		return p.parseTypeDecl()
	}
	if t.Kind == token.KwEffect {
		return p.parseEffectDecl()
	}
	if t.Kind != token.LIDENT {
		p.errorAt(t.Span, "SYNTAX PROBLEM",
			"I was expecting a declaration here, like `name = expression`.")
		p.recoverToTopLevel(true) // the bad token sits at column 1: must consume it
		return nil
	}
	name := t
	p.next()

	var ann *ast.TypeAnn
	if p.peekInExpr().Kind == token.COLON {
		colon := p.next()
		te := p.parseTypeExpr()
		if te == nil {
			p.recoverToTopLevel(false)
			return nil
		}
		ann = &ast.TypeAnn{Type: te, Sp: colon.Span.Merge(te.Span())}
		// The annotated definition must sit directly below.
		nt := p.peek()
		if nt.Kind == token.EOF {
			p.errorAt(name.Span, TitleUnexpectedEOF,
				"I see a type annotation for `"+name.Text+"` but no definition for it yet.")
			return nil
		}
		if nt.Kind != token.LIDENT || nt.Text != name.Text || nt.Pos().Col != 1 {
			p.errorAt(nt.Span, "MISSING DEFINITION",
				"The type annotation for `"+name.Text+"` must sit directly above its\ndefinition, like:\n\n    "+name.Text+" : Int\n    "+name.Text+" = 42")
			p.recoverToTopLevel(false)
			return nil
		}
		p.next() // the repeated name
	}

	params := p.parseParams()
	eqTok := p.peekInExpr()
	if !p.expect(token.EQ, "I expect `=` after the name in a declaration.") {
		p.recoverToTopLevel(false)
		return nil
	}
	body := p.parseBindBody(eqTok)
	if body == nil {
		p.recoverToTopLevel(false)
		return nil
	}
	if t := p.peekInExpr(); t.Kind != token.EOF {
		p.errorAt(t.Span, "SYNTAX PROBLEM",
			"The expression seemed complete, but then I ran into this.")
		p.recoverToTopLevel(false)
	}
	return &ast.ValueDecl{Name: name.Text, NameSpan: name.Span, Params: params, Ann: ann, Body: body}
}

// parseEffectDecl parses an effect header followed by an indented block of
// operation signatures. The first operation establishes the block column.
func (p *parser) parseEffectDecl() ast.Decl {
	p.next() // `effect`
	nameT := p.peekInExpr()
	if nameT.Kind != token.UIDENT {
		p.errorAt(nameT.Span, "SYNTAX PROBLEM", "After `effect` I expect a capitalized effect name, like `Console`.")
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
		p.errorAt(nameT.Span, TitleUnexpectedEOF, "This effect declaration needs at least one operation signature.")
		return nil
	}
	if first.Pos().Col <= 1 {
		p.errorAt(first.Span, "SYNTAX PROBLEM", "Effect operations must be indented below the effect name.")
		return nil
	}
	col := first.Pos().Col
	p.lay.push(ctxBlock, col)
	defer p.lay.pop()
	var ops []ast.OpSig
	for {
		p.stmtStart = p.pos
		opT := p.peekInExpr()
		if opT.Kind != token.LIDENT {
			p.errorAt(opT.Span, "SYNTAX PROBLEM", "I expect an operation name here, like `print : String -> ()`.")
			return nil
		}
		p.next()
		if !p.expect(token.COLON, "I expect `:` after the operation name.") {
			return nil
		}
		ty := p.parseTypeExpr()
		if ty == nil {
			return nil
		}
		ops = append(ops, ast.OpSig{Name: opT.Text, NameSpan: opT.Span, Type: ty})
		nt := p.peek()
		if nt.Kind == token.EOF || nt.Pos().Col < col {
			break
		}
		if nt.Pos().Col != col {
			p.errorAt(nt.Span, "SYNTAX PROBLEM", "Effect operation signatures must line up at the same column.")
			return nil
		}
	}
	return &ast.EffectDecl{Name: nameT.Text, NameSpan: nameT.Span, Params: params, Ops: ops}
}

// parseTypeDecl parses `type Name p1 … = C1 atoms | C2 atoms | …` (§3.7).
// The RHS is always constructor alternatives; `|` may sit inline or lead a
// continuation line (any indented token continues the declaration).
func (p *parser) parseTypeDecl() ast.Decl {
	p.next() // `type`
	nameT := p.peekInExpr()
	if nameT.Kind != token.UIDENT {
		p.errorAt(nameT.Span, "SYNTAX PROBLEM",
			"After `type` I expect a capitalized type name, like:\n\n    type Shape = Circle Float | Rect Float Float")
		p.recoverToTopLevel(false)
		return nil
	}
	p.next()
	params := p.parseParams()
	if !p.expect(token.EQ, "I expect `=` after the type name, then the constructors.") {
		p.recoverToTopLevel(false)
		return nil
	}
	var ctors []ast.CtorDef
	for {
		c, ok := p.parseCtorDef()
		if !ok {
			p.recoverToTopLevel(false)
			return nil
		}
		ctors = append(ctors, c)
		if p.peekInExpr().Kind != token.PIPE {
			break
		}
		p.next()
	}
	if t := p.peekInExpr(); t.Kind != token.EOF {
		p.errorAt(t.Span, "SYNTAX PROBLEM",
			"I expect `|` between constructor alternatives.")
		p.recoverToTopLevel(false)
		return nil
	}
	return &ast.TypeDecl{Name: nameT.Text, NameSpan: nameT.Span, Params: params, Ctors: ctors}
}

// parseCtorDef parses one constructor alternative: a capitalized name
// followed by zero or more type atoms (applications need parens: `Cons a (List a)`).
func (p *parser) parseCtorDef() (ast.CtorDef, bool) {
	t := p.peekInExpr()
	if t.Kind != token.UIDENT {
		switch {
		case t.Kind == token.EOF && p.peek().Kind == token.EOF:
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"I got to the end of the input while reading a constructor.")
		case t.Kind == token.EOF:
			// The declaration ended (offside) while a constructor was still
			// expected — e.g. a trailing `|`. Point at the declaration, not
			// at whatever happens to start the next one.
			p.errorAt(p.prevSpan(), "SYNTAX PROBLEM",
				"This `type` declaration ended while I was still expecting a\nconstructor, like `Circle Float`.")
		default:
			p.errorAt(t.Span, "SYNTAX PROBLEM",
				"I expect a capitalized constructor name here, like `Circle Float`.")
		}
		return ast.CtorDef{}, false
	}
	p.next()
	var args []ast.TypeExpr
	for isTypeAtomStart(p.peekInExpr().Kind) {
		a := p.parseTypeAtom()
		if a == nil {
			return ast.CtorDef{}, false
		}
		args = append(args, a)
	}
	return ast.CtorDef{Name: t.Text, NameSpan: t.Span, Args: args}, true
}

// parseParams consumes zero or more parameter identifiers.
func (p *parser) parseParams() []ast.Param {
	var params []ast.Param
	for p.peekInExpr().Kind == token.LIDENT {
		t := p.next()
		params = append(params, ast.Param{Name: t.Text, Sp: t.Span})
	}
	return params
}

// parseBindBody dispatches on where a binding's body starts (§3.6): on the
// `=`'s line → inline expression; on a later line, deeper than the current
// layout column → a block at that column. Shared by top-level declarations,
// block bindings, local functions, and lambda bodies.
func (p *parser) parseBindBody(eqTok token.Token) ast.Expr {
	t := p.peek()
	switch {
	case t.Kind == token.EOF:
		p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
			"I got to the end of the input while still expecting an expression.")
		return nil
	case t.Pos().Line == eqTok.Pos().Line:
		return p.parseExpr(1)
	case p.lay.checkOffside(t.Pos()) == offContinue:
		return p.parseBlock(t.Pos().Col)
	default:
		p.errorAt(p.prevSpan(), "SYNTAX PROBLEM",
			"This binding has no expression — the next line does not belong\nto it.")
		return nil
	}
}

type stmtKind int

const (
	stmtResult stmtKind = iota
	stmtBind
	stmtAnn
	stmtLocalFn
)

// classifyStmt inspects the statement starting at the current token. The
// lookahead is bounded by the offside rule — it never scans past a token at
// or left of the block column, or `y =` + result + next binding would
// misread as a local function definition.
func (p *parser) classifyStmt(col int) stmtKind {
	inBounds := func(i int) bool {
		t := p.toks[i]
		return t.Kind != token.EOF && t.Span.StartPos().Col > col
	}
	i := p.pos
	if p.toks[i].Kind != token.LIDENT {
		return stmtResult
	}
	if !inBounds(i + 1) {
		return stmtResult
	}
	switch p.toks[i+1].Kind {
	case token.EQ:
		return stmtBind
	case token.COLON:
		return stmtAnn
	case token.LIDENT:
		j := i + 1
		for inBounds(j) && p.toks[j].Kind == token.LIDENT {
			j++
		}
		if inBounds(j) && p.toks[j].Kind == token.EQ {
			return stmtLocalFn
		}
	}
	return stmtResult
}

// parseBlock parses a statement block at the given column: `name = expr`
// bindings (optionally annotated) followed by exactly one result expression.
// Zero-binding blocks collapse to the plain result expression.
func (p *parser) parseBlock(col int) ast.Expr {
	p.lay.push(ctxBlock, col)
	defer p.lay.pop()

	var binds []ast.LocalBind
	var pendingAnn *ast.TypeAnn
	var pendingAnnName token.Token

	for {
		t := p.peek()
		if t.Kind == token.EOF {
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"This block has no result expression yet. A block is zero or more\n`name = …` bindings followed by one final expression.")
			return nil
		}
		if t.Pos().Col < col {
			if pendingAnn != nil {
				p.errorAt(t.Span, "MISSING DEFINITION",
					"The type annotation for `"+pendingAnnName.Text+"` must sit directly\nabove its binding.")
				return nil
			}
			p.errorAt(t.Span, "SYNTAX PROBLEM",
				"This block ended without a result expression. A block is zero or\nmore `name = …` bindings followed by one final expression.")
			return nil
		}

		switch p.classifyStmt(col) {
		case stmtBind, stmtLocalFn:
			nameT := p.next()
			params := p.parseParams()
			eqT := p.peekInExpr()
			if !p.expect(token.EQ, "I expect `=` after the binding name.") {
				return nil
			}
			if pendingAnn != nil && pendingAnnName.Text != nameT.Text {
				p.errorAt(nameT.Span, "MISSING DEFINITION",
					"The type annotation for `"+pendingAnnName.Text+"` must sit directly\nabove its binding, but this binds `"+nameT.Text+"`.")
				return nil
			}
			rhs := p.parseBindBody(eqT)
			if rhs == nil {
				return nil
			}
			binds = append(binds, ast.LocalBind{
				Name: nameT.Text, NameSpan: nameT.Span, Params: params, Ann: pendingAnn, Body: rhs,
			})
			pendingAnn = nil

		case stmtAnn:
			nameT := p.next()
			colon := p.next()
			if pendingAnn != nil {
				p.errorAt(nameT.Span, "MISSING DEFINITION",
					"The type annotation for `"+pendingAnnName.Text+"` must sit directly\nabove its binding.")
				return nil
			}
			te := p.parseTypeExpr()
			if te == nil {
				return nil
			}
			pendingAnn = &ast.TypeAnn{Type: te, Sp: colon.Span.Merge(te.Span())}
			pendingAnnName = nameT

		case stmtResult:
			if pendingAnn != nil {
				p.errorAt(t.Span, "MISSING DEFINITION",
					"The type annotation for `"+pendingAnnName.Text+"` must sit directly\nabove its binding.")
				return nil
			}
			p.stmtStart = p.pos // the opener may sit exactly at the block column
			result := p.parseExpr(1)
			if result == nil {
				return nil
			}
			if nt := p.peek(); nt.Kind != token.EOF && nt.Pos().Col == col {
				p.errorAt(nt.Span, "SYNTAX PROBLEM",
					"The result expression must be the last statement in a block.")
				return nil
			}
			if len(binds) == 0 {
				return result
			}
			return &ast.Block{Binds: binds, Result: result}
		}
	}
}

// parseTypeExpr parses the surface type grammar: applications and
// right-assoc `->`.
func (p *parser) parseTypeExpr() ast.TypeExpr {
	atom := p.parseTypeApp()
	if atom == nil {
		return nil
	}
	if p.peekInExpr().Kind == token.ARROW {
		p.next()
		var eff *ast.EffRow
		if p.peekInExpr().Kind == token.LBRACE {
			eff = p.parseEffRow()
			if eff == nil {
				return nil
			}
		}
		ret := p.parseTypeExpr()
		if ret == nil {
			return nil
		}
		return &ast.TFunExpr{Arg: atom, Eff: eff, Ret: ret}
	}
	return atom
}

func (p *parser) parseEffRow() *ast.EffRow {
	lb := p.next()
	r := &ast.EffRow{}
	if p.peekInExpr().Kind == token.RBRACE {
		rp := p.next()
		r.Sp = lb.Span.Merge(rp.Span)
		return r
	}
	for {
		t := p.peekInExpr()
		if t.Kind == token.PIPE {
			p.next()
			tail := p.peekInExpr()
			if tail.Kind != token.LIDENT {
				p.errorAt(tail.Span, "SYNTAX PROBLEM", "After `|` I expect a lowercase row variable, like `e`.")
				return nil
			}
			p.next()
			r.Tail, r.TailSp = tail.Text, tail.Span
			break
		}
		if t.Kind != token.UIDENT {
			p.errorAt(t.Span, "SYNTAX PROBLEM", "I expect a capitalized effect name in this row.")
			return nil
		}
		p.next()
		label := ast.EffLabelExpr{Name: t.Text, NameSp: t.Span}
		for isTypeAtomStart(p.peekInExpr().Kind) {
			arg := p.parseTypeAtom()
			if arg == nil {
				return nil
			}
			label.Args = append(label.Args, arg)
		}
		r.Labels = append(r.Labels, label)
		switch p.peekInExpr().Kind {
		case token.COMMA:
			p.next()
			continue
		case token.PIPE:
			continue
		case token.RBRACE:
		default:
			p.errorAt(p.peek().Span, "SYNTAX PROBLEM", "I expect `,`, `|`, or `}` after this effect label.")
			return nil
		}
		break
	}
	if !p.expect(token.RBRACE, "I expect `}` to close this effect row.") {
		return nil
	}
	r.Sp = lb.Span.Merge(p.prevSpan())
	return r
}

// parseTypeApp parses type application (`Maybe Int`): a named head followed
// by argument atoms. Only uppercase names head applications — type variables
// cannot (no higher kinds, §8.4).
func (p *parser) parseTypeApp() ast.TypeExpr {
	atom := p.parseTypeAtom()
	if atom == nil {
		return nil
	}
	head, ok := atom.(*ast.TName)
	if !ok || !isTypeAtomStart(p.peekInExpr().Kind) {
		return atom
	}
	var args []ast.TypeExpr
	for isTypeAtomStart(p.peekInExpr().Kind) {
		a := p.parseTypeAtom()
		if a == nil {
			return nil
		}
		args = append(args, a)
	}
	return &ast.TApp{Name: head.Name, NameSp: head.Sp, Args: args}
}

func isTypeAtomStart(k token.Kind) bool {
	return k == token.UIDENT || k == token.LIDENT || k == token.LPAREN
}

func (p *parser) parseTypeAtom() ast.TypeExpr {
	t := p.peekInExpr()
	switch t.Kind {
	case token.UIDENT:
		p.next()
		return &ast.TName{Name: t.Text, Sp: t.Span}
	case token.LIDENT:
		p.next()
		return &ast.TVarName{Name: t.Text, Sp: t.Span}
	case token.LPAREN:
		p.next()
		if p.peekInExpr().Kind == token.RPAREN {
			rp := p.next()
			return &ast.TName{Name: "()", Sp: t.Span.Merge(rp.Span)}
		}
		inner := p.parseTypeExpr()
		if inner == nil {
			return nil
		}
		if !p.expect(token.RPAREN, "I was expecting a closing `)` in this type.") {
			return nil
		}
		return inner
	case token.EOF:
		if p.peek().Kind == token.EOF {
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"I got to the end of the input while reading a type.")
		} else {
			p.errorAt(p.prevSpan(), "SYNTAX PROBLEM", "This type annotation is unfinished.")
		}
		return nil
	default:
		p.errorAt(t.Span, "SYNTAX PROBLEM",
			"I was expecting a type here, like `Int` or `Int -> Float`.")
		return nil
	}
}

type assocKind int

const (
	assocLeft assocKind = iota
	assocRight
	assocNon
)

// binOp is the operator table, Elm's precedences: `++` 5 right-assoc;
// comparisons 4 non-associative; `+ -` 6 left; `* /` 7 left.
func binOp(k token.Kind) (int, assocKind) {
	switch k {
	case token.PLUS, token.MINUS:
		return 6, assocLeft
	case token.STAR, token.SLASH:
		return 7, assocLeft
	case token.PLUSPLUS:
		return 5, assocRight
	case token.EQEQ, token.SLASHEQ, token.LT, token.GT, token.LTEQ, token.GTEQ:
		return 4, assocNon
	default:
		return 0, assocLeft
	}
}

func (p *parser) parseExpr(minPrec int) ast.Expr {
	left := p.parseUnary()
	if left == nil {
		return nil
	}
	for {
		t := p.peekInExpr()
		prec, assoc := binOp(t.Kind)
		if prec == 0 || prec < minPrec {
			return left
		}
		p.next()
		rhsMin := prec + 1
		if assoc == assocRight {
			rhsMin = prec
		}
		right := p.parseExpr(rhsMin)
		if right == nil {
			return nil
		}
		left = &ast.BinOp{Op: t.Text, OpSpan: t.Span, L: left, R: right}
		if assoc == assocNon {
			if nextPrec, _ := binOp(p.peekInExpr().Kind); nextPrec == prec {
				p.errorAt(p.peekInExpr().Span, "SYNTAX PROBLEM",
					"I cannot parse chained comparisons like `a < b < c` — comparisons\ndo not associate. Add parentheses to say what you mean.")
				return nil
			}
		}
	}
}

// parseUnary handles prefix minus. Any MINUS reaching here is in prefix
// position (the Pratt loop consumes infix minus after a complete operand),
// binding tighter than every binary operator, looser than application.
func (p *parser) parseUnary() ast.Expr {
	if t := p.peekInExpr(); t.Kind == token.MINUS {
		p.next()
		operand := p.parseUnary()
		if operand == nil {
			return nil
		}
		return &ast.Neg{Operand: operand, Sp: t.Span.Merge(operand.Span())}
	}
	return p.parseApply()
}

// parseApply parses juxtaposition application, left-associative: one head,
// then any number of argument atoms. `if` may head an expression but is
// not an atom, so `print if …` needs parens (as in Elm).
func (p *parser) parseApply() ast.Expr {
	switch p.peekInExpr().Kind {
	case token.KwIf:
		return p.parseIf()
	case token.BACKSLASH:
		return p.parseLambda()
	case token.KwCase:
		return p.parseCase()
	case token.KwHandle:
		return p.parseHandle()
	}
	fn := p.parseAtom()
	if fn == nil {
		return nil
	}
	for {
		switch p.peekInExpr().Kind {
		case token.INT, token.FLOAT, token.STRING, token.LIDENT, token.UIDENT, token.LPAREN, token.KwResume:
			arg := p.parseAtom()
			if arg == nil {
				return nil
			}
			fn = &ast.App{Fn: fn, Arg: arg}
		default:
			return fn
		}
	}
}

func (p *parser) parseHandle() ast.Expr {
	h := p.next()
	body := p.parseExpr(1)
	if body == nil {
		return nil
	}
	if !p.expect(token.KwOf, "I expect `of` after the expression being handled.") {
		return nil
	}
	first := p.peekInExpr()
	if first.Kind == token.EOF {
		p.errorAt(p.prevSpan(), TitleUnexpectedEOF, "I expect at least one handler clause after `of`.")
		return nil
	}
	p.lay.push(ctxCase, first.Pos().Col)
	defer p.lay.pop()
	result := &ast.Handle{Body: body, Sp: h.Span}
	for {
		p.stmtStart = p.pos
		op := p.peekInExpr()
		if op.Kind != token.LIDENT {
			p.errorAt(op.Span, "SYNTAX PROBLEM", "I expect a handler clause like `print value -> expression`.")
			return nil
		}
		p.next()
		params := p.parseParams()
		arrow := p.peekInExpr()
		if !p.expect(token.ARROW, "I expect `->` after the handler clause parameters.") {
			return nil
		}
		clauseBody := p.parseBindBody(arrow)
		if clauseBody == nil {
			return nil
		}
		if op.Text == "return" {
			if len(params) != 1 {
				p.errorAt(op.Span, "SYNTAX PROBLEM", "A `return` clause needs exactly one parameter.")
				return nil
			}
			if result.Return != nil {
				p.errorAt(op.Span, "SYNTAX PROBLEM", "A handler can have only one `return` clause.")
				return nil
			}
			result.Return = &ast.ReturnClause{Param: params[0], Body: clauseBody, Sp: op.Span}
		} else {
			result.Clauses = append(result.Clauses, ast.HandleClause{Op: op.Text, OpSpan: op.Span, Params: params, Body: clauseBody})
		}
		nt := p.peek()
		if nt.Kind == token.EOF || !p.lay.atBranchCol(nt.Pos()) {
			break
		}
	}
	if len(result.Clauses) == 0 {
		p.errorAt(h.Span, "SYNTAX PROBLEM", "A handler needs at least one operation clause.")
		return nil
	}
	return result
}

// parseLambda parses `\x -> body` / `\x y -> body`. Like `if`, a lambda
// heads an expression but is not an atom: `f (\x -> x)` needs parens, and
// the body extends maximally right (or opens an indented block).
func (p *parser) parseLambda() ast.Expr {
	bs := p.next() // the backslash
	params := p.parseParams()
	if len(params) == 0 {
		p.errorAt(p.peek().Span, "SYNTAX PROBLEM",
			"A lambda needs at least one parameter, like `\\x -> x + 1`.")
		return nil
	}
	arrow := p.peekInExpr()
	if !p.expect(token.ARROW, "I expect `->` after the lambda parameters.") {
		return nil
	}
	body := p.parseBindBody(arrow)
	if body == nil {
		return nil
	}
	return &ast.Lambda{Params: params, Body: body, Sp: bs.Span}
}

// parseCase parses `case scrutinee of` and its branches. The column of the
// first pattern token after `of` defines branch alignment (layout rule 2,
// §5): a token at exactly that column starts a new branch, left of it ends
// the case. Branch bodies are statement blocks (§3.6) or inline expressions.
func (p *parser) parseCase() ast.Expr {
	caseTok := p.next()
	scrut := p.parseExpr(1)
	if scrut == nil {
		return nil
	}
	if !p.expect(token.KwOf, "I expect `of` after the expression in a `case`.") {
		return nil
	}
	first := p.peekInExpr()
	if first.Kind == token.EOF {
		if p.peek().Kind == token.EOF {
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"I expect at least one branch after `of`, like `True -> 1`.")
		} else {
			p.errorAt(p.peek().Span, "SYNTAX PROBLEM",
				"The branches of this `case` must be indented past the enclosing\nalignment column.")
		}
		return nil
	}
	p.lay.push(ctxCase, first.Pos().Col)
	defer p.lay.pop()

	var branches []ast.CaseBranch
	for {
		p.stmtStart = p.pos // the pattern may sit exactly at the branch column
		pat := p.parsePattern()
		if pat == nil {
			return nil
		}
		arrowT := p.peekInExpr()
		if !p.expect(token.ARROW, "I expect `->` after a case branch's pattern.") {
			return nil
		}
		body := p.parseBindBody(arrowT)
		if body == nil {
			return nil
		}
		branches = append(branches, ast.CaseBranch{Pattern: pat, Body: body})
		if nt := p.peek(); nt.Kind == token.EOF || !p.lay.atBranchCol(nt.Pos()) {
			return &ast.Case{Scrutinee: scrut, Branches: branches, Sp: caseTok.Span}
		}
	}
}

// parsePattern parses a branch-level pattern: a constructor applied to
// argument atoms, or a single atom. Nested applications need parens.
func (p *parser) parsePattern() ast.Pattern {
	t := p.peekInExpr()
	if t.Kind == token.UIDENT {
		p.next()
		var args []ast.Pattern
		for isPatternAtomStart(p.peekInExpr().Kind) {
			a := p.parsePatternAtom()
			if a == nil {
				return nil
			}
			args = append(args, a)
		}
		return &ast.PCtor{Name: t.Text, NameSpan: t.Span, Args: args}
	}
	return p.parsePatternAtom()
}

func isPatternAtomStart(k token.Kind) bool {
	switch k {
	case token.UNDERSCORE, token.LIDENT, token.UIDENT,
		token.INT, token.FLOAT, token.STRING, token.LPAREN:
		return true
	}
	return false
}

func (p *parser) parsePatternAtom() ast.Pattern {
	t := p.peekInExpr()
	switch t.Kind {
	case token.UNDERSCORE:
		p.next()
		return &ast.PWildcard{Sp: t.Span}
	case token.LIDENT:
		p.next()
		return &ast.PVar{Name: t.Text, Sp: t.Span}
	case token.UIDENT:
		p.next()
		return &ast.PCtor{Name: t.Text, NameSpan: t.Span}
	case token.INT:
		p.next()
		v, _ := strconv.ParseInt(t.Text, 10, 64) // overflow reported by the lexer
		return &ast.PInt{Value: v, Sp: t.Span}
	case token.FLOAT:
		p.next()
		v, _ := strconv.ParseFloat(t.Text, 64)
		return &ast.PFloat{Value: v, Sp: t.Span}
	case token.STRING:
		p.next()
		return &ast.PString{Value: lexer.Unescape(t.Text), Sp: t.Span}
	case token.MINUS:
		p.next()
		nt := p.peekInExpr()
		switch nt.Kind {
		case token.INT:
			p.next()
			v, _ := strconv.ParseInt(nt.Text, 10, 64)
			return &ast.PInt{Value: -v, Sp: t.Span.Merge(nt.Span)}
		case token.FLOAT:
			p.next()
			v, _ := strconv.ParseFloat(nt.Text, 64)
			return &ast.PFloat{Value: -v, Sp: t.Span.Merge(nt.Span)}
		}
		p.errorAt(t.Span, "SYNTAX PROBLEM",
			"In a pattern, `-` must be followed directly by a number literal.")
		return nil
	case token.LPAREN:
		p.next()
		pat := p.parsePattern()
		if pat == nil {
			return nil
		}
		if !p.expect(token.RPAREN, "I was expecting a closing `)` in this pattern.") {
			return nil
		}
		return pat
	case token.EOF:
		if p.peek().Kind == token.EOF {
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"I got to the end of the input while expecting a pattern.")
		} else {
			p.errorAt(p.prevSpan(), "SYNTAX PROBLEM",
				"This case branch is unfinished — I was expecting a pattern.")
		}
		return nil
	default:
		p.errorAt(t.Span, "SYNTAX PROBLEM",
			"I was expecting a pattern here, like `Just x`, a literal, or `_`.")
		return nil
	}
}

func (p *parser) parseIf() ast.Expr {
	ifTok := p.next()
	cond := p.parseExpr(1)
	if cond == nil {
		return nil
	}
	if !p.expect(token.KwThen, "I expect `then` after an `if` condition.") {
		return nil
	}
	thenE := p.parseExpr(1)
	if thenE == nil {
		return nil
	}
	if !p.expect(token.KwElse, "I expect `else` after the `then` branch — every `if` needs one.") {
		return nil
	}
	elseE := p.parseExpr(1)
	if elseE == nil {
		return nil
	}
	return &ast.If{Cond: cond, Then: thenE, Else: elseE, Sp: ifTok.Span}
}

func (p *parser) parseAtom() ast.Expr {
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
	case token.FLOAT:
		p.next()
		v, _ := strconv.ParseFloat(t.Text, 64) // range errors reported by the lexer
		return &ast.FloatLit{Value: v, Sp: t.Span}
	case token.STRING:
		p.next()
		return &ast.StringLit{Value: lexer.Unescape(t.Text), Sp: t.Span}
	case token.LIDENT:
		p.next()
		return &ast.Var{Name: t.Text, Sp: t.Span}
	case token.UIDENT:
		p.next()
		return &ast.Ctor{Name: t.Text, Sp: t.Span}
	case token.KwResume:
		p.next()
		return &ast.Resume{Sp: t.Span}
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
	if p.pos != p.stmtStart && p.lay.checkOffside(t.Pos()) != offContinue {
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
