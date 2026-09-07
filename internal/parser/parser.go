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
	// a case branch's first pattern token), or a `then`/`else` at its own
	// `if` chain's anchor column. Everywhere else, a token at the column is
	// a sibling boundary, not expression content.
	stmtStart int

	// usesStaging records that this file built a quote or a splice, so the
	// module loader knows to pull in the bundled `Meta` module.
	usesStaging bool
}

// Parse parses a whole module.
func Parse(toks []token.Token, f *source.File) (*ast.Module, []diag.Error) {
	p := &parser{f: f, toks: toks, stmtStart: -1}
	p.lay.push(ctxDecl, 1)
	m := &ast.Module{}
	m.Header = p.parseHeader()
	for p.peek().Kind == token.KwImport {
		if im, ok := p.parseImport(); ok {
			m.Imports = append(m.Imports, im)
		}
	}
	for p.peek().Kind != token.EOF {
		if d := p.parseDecl(); d != nil {
			m.Decls = append(m.Decls, d)
		}
	}
	m.UsesStaging = p.usesStaging
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
	if name, sp, ok := p.parseModuleName(); ok {
		h.Name, h.NameSpan = name, sp
	} else {
		t := p.peek()
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
	ex, ok := p.parseExposingBody()
	h.Exposing = ex
	if !ok {
		p.recoverToTopLevel(false)
	}
	return h
}

func (p *parser) parseModuleName() (string, source.Span, bool) {
	t := p.peek()
	if t.Kind != token.UIDENT {
		return "", t.Span, false
	}
	p.next()
	name, sp := t.Text, t.Span
	for p.peek().Kind == token.DOT {
		p.next()
		part := p.peek()
		if part.Kind != token.UIDENT {
			return "", part.Span, false
		}
		p.next()
		name, sp = name+"."+part.Text, sp.Merge(part.Span)
	}
	return name, sp, true
}

// parseQualifiedName consumes `A.B.name` or `A.B.Name`. The caller has
// already established that the first token is capitalized.
func (p *parser) parseQualifiedName() (string, token.Kind, source.Span) {
	first := p.next()
	return p.parseQualifiedNameAfterFirst(first)
}

func (p *parser) parseQualifiedNameAfterFirst(first token.Token) (string, token.Kind, source.Span) {
	name, final, sp := first.Text, first.Kind, first.Span
	for p.peek().Kind == token.DOT {
		p.next()
		part := p.peek()
		if part.Kind != token.UIDENT && part.Kind != token.LIDENT {
			p.errorAt(part.Span, "SYNTAX PROBLEM", "I expect a name after `.`.")
			return name, final, sp
		}
		p.next()
		name, final, sp = name+"."+part.Text, part.Kind, sp.Merge(part.Span)
		if final == token.LIDENT {
			break
		}
	}
	return name, final, sp
}

func (p *parser) parseExposingBody() (ast.Exposing, bool) {
	var ex ast.Exposing
	if p.peek().Kind == token.DOTDOT {
		p.next()
		ex.All = true
		return ex, p.expect(token.RPAREN, "I expect a closing `)` for the `exposing` list.")
	}
	if p.peek().Kind == token.RPAREN {
		p.next()
		return ex, true
	}
	for {
		t := p.peek()
		if t.Kind != token.LIDENT && t.Kind != token.UIDENT {
			p.errorAt(t.Span, "SYNTAX PROBLEM", "I expect a name in the `exposing` list.")
			return ex, false
		}
		p.next()
		item := ast.ExposeItem{Name: t.Text, Sp: t.Span}
		if t.Kind == token.UIDENT && p.peek().Kind == token.LPAREN {
			p.next()
			if !p.expect(token.DOTDOT, "Only `(..)` is supported after a type or effect name.") ||
				!p.expect(token.RPAREN, "I expect `)` after `..`.") {
				return ex, false
			}
			item.All = true
		}
		ex.Items = append(ex.Items, item)
		if p.peek().Kind != token.COMMA {
			break
		}
		p.next()
	}
	return ex, p.expect(token.RPAREN, "I expect a closing `)` for the `exposing` list.")
}

func (p *parser) parseImport() (ast.Import, bool) {
	p.next()
	name, sp, ok := p.parseModuleName()
	if !ok {
		p.errorAt(p.peek().Span, "SYNTAX PROBLEM", "After `import` I expect a module name like `Geometry.Point`.")
		p.recoverToTopLevel(false)
		return ast.Import{}, false
	}
	im := ast.Import{Module: name, ModuleSpan: sp}
	if p.peek().Kind == token.KwAs {
		p.next()
		a := p.peek()
		if a.Kind != token.UIDENT {
			p.errorAt(a.Span, "SYNTAX PROBLEM", "An import alias must be one capitalized identifier.")
			return im, false
		}
		p.next()
		im.Alias, im.AliasSpan = a.Text, a.Span
	}
	if p.peek().Kind == token.KwExposing {
		p.next()
		if !p.expect(token.LPAREN, "I expect a parenthesized list after `exposing`.") {
			return im, false
		}
		ex, ok := p.parseExposingBody()
		im.Exposing = &ex
		if !ok {
			return im, false
		}
	}
	return im, true
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
	if t.Kind == token.KwInfix {
		return p.parseInfixDecl()
	}
	if t.Kind == token.KwClass {
		return p.parseClassDecl()
	}
	if t.Kind == token.KwInstance {
		return p.parseInstanceDecl()
	}
	if t.Kind == token.KwDeriver {
		return p.parseDeriverDecl()
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
		preds := p.parseContext()
		te := p.parseTypeExpr()
		if te == nil {
			p.recoverToTopLevel(false)
			return nil
		}
		ann = &ast.TypeAnn{Type: te, Preds: preds, Sp: colon.Span.Merge(te.Span())}
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

	params := p.parseValueParams()
	eqTok := p.peekInExpr()
	if !p.expect(token.EQ, "I expect `=` after the name in a declaration.") {
		p.recoverToTopLevel(false)
		return nil
	}
	if p.peekInExpr().Kind == token.KwNative {
		n := p.parseNativeBody()
		if ann == nil {
			p.errorAt(name.Span, "NATIVE DECLARATION", "A native declaration requires a type annotation.")
		}
		if len(params) > 0 {
			p.errorAt(name.Span, "NATIVE DECLARATION", "A native declaration cannot have source parameters; put its complete function type in the annotation.")
		}
		return &ast.ValueDecl{Name: name.Text, NameSpan: name.Span, Params: params, Ann: ann, Native: n}
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

func (p *parser) parseDeriverDecl() ast.Decl {
	start := p.next()
	class, _, sp := p.parseQualifiedName()
	d := &ast.DeriverDecl{Class: class, ClassSpan: start.Span.Merge(sp)}
	first := p.peek()
	if first.Kind == token.EOF || first.Pos().Col <= 1 {
		p.errorAt(first.Span, "DERIVER METHOD", "A deriver needs indented method definitions.")
		return nil
	}
	col := first.Pos().Col
	p.lay.push(ctxBlock, col)
	defer p.lay.pop()
	for p.peek().Kind != token.EOF && p.peek().Pos().Col >= col {
		name := p.peek()
		p.next()
		params := p.parseValueParams()
		if !p.expect(token.EQ, "I expect `=` after the deriver method parameters.") {
			return nil
		}
		body := p.parseBindBody(p.peek())
		if body == nil {
			return nil
		}
		d.Methods = append(d.Methods, &ast.ValueDecl{Name: name.Text, NameSpan: name.Span, Params: params, Body: body})
	}
	return d
}

func (p *parser) parseNativeBody() *ast.NativeBody {
	kw := p.next()
	n := &ast.NativeBody{Sp: kw.Span}
	if p.peekInExpr().Kind == token.STRING {
		t := p.next()
		s := lexer.Unescape(t.Text)
		n.Template = &s
		n.Sp = n.Sp.Merge(t.Span)
	}
	if t := p.peekInExpr(); t.Kind != token.EOF {
		p.errorAt(t.Span, "SYNTAX PROBLEM", "A native declaration ends after `native` or its template string.")
	}
	return n
}

// parseInfixDecl parses `infix (op) = value`. A rejected binding recovers to
// the next declaration rather than leaving the parser mid-line, where the
// unconsumed operator would also be reported as a stray declaration.
func (p *parser) parseInfixDecl() ast.Decl {
	p.next()
	if !p.expect(token.LPAREN, "I expect `(` after `infix`.") {
		p.recoverToTopLevel(false)
		return nil
	}
	op := p.peekInExpr()
	if prec, _ := binOp(op.Kind); prec == 0 {
		p.errorAt(op.Span, "NATIVE DECLARATION", "I expect one of fango's fixed binary operators here.")
		p.recoverToTopLevel(false)
		return nil
	}
	if shortCircuit(op.Kind) {
		p.errorAt(op.Span, "NATIVE DECLARATION",
			"("+op.Text+") is short-circuiting syntax elaborated to an `if`, not a\ncall, so it cannot be bound to a value.")
		p.recoverToTopLevel(false)
		return nil
	}
	p.next()
	if !p.expect(token.RPAREN, "I expect `)` after the operator.") || !p.expect(token.EQ, "I expect `=` after the operator binding.") {
		p.recoverToTopLevel(false)
		return nil
	}
	target := p.peekInExpr()
	if target.Kind != token.LIDENT {
		p.errorAt(target.Span, "NATIVE DECLARATION", "An infix binding must name a native value.")
		p.recoverToTopLevel(false)
		return nil
	}
	p.next()
	return &ast.InfixDecl{Op: op.Text, Target: target.Text, OpSpan: op.Span, TargetSpan: target.Span}
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
		var native *ast.NativeBody
		if p.peekInExpr().Kind == token.EQ {
			p.next()
			if p.peekInExpr().Kind != token.KwNative {
				p.errorAt(p.peekInExpr().Span, "SYNTAX PROBLEM", "An effect operation implementation must use `native`.")
				return nil
			}
			native = p.parseNativeBody()
		}
		ops = append(ops, ast.OpSig{Name: opT.Text, NameSpan: opT.Span, Type: ty, Native: native})
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

// parseTypeDecl parses `type Name p1 … = C1 atoms | C2 atoms | …` (doc/reference.md, "Algebraic data types and matching").
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
	var recordFields []ast.RecordFieldDef
	if p.peekInExpr().Kind == token.LBRACE {
		var ok bool
		recordFields, ok = p.parseRecordTypeFields()
		if !ok {
			p.recoverToTopLevel(false)
			return nil
		}
	} else {
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
	}
	var deriving []ast.TName
	if p.peekInExpr().Kind == token.KwDeriving {
		p.next()
		if !p.expect(token.LPAREN, "I expect `(Eq, Show)` after `deriving`.") {
			return nil
		}
		for {
			if p.peekInExpr().Kind != token.UIDENT {
				p.errorAt(p.peek().Span, "DERIVING", "I expect a class name.")
				return nil
			}
			name, _, sp := p.parseQualifiedName()
			deriving = append(deriving, ast.TName{Name: name, Sp: sp})
			if p.peekInExpr().Kind != token.COMMA {
				break
			}
			p.next()
		}
		if !p.expect(token.RPAREN, "I expect `)` after the derived classes.") {
			return nil
		}
	}
	if t := p.peekInExpr(); t.Kind != token.EOF {
		p.errorAt(t.Span, "SYNTAX PROBLEM",
			"I expect `|` between constructor alternatives.")
		p.recoverToTopLevel(false)
		return nil
	}
	return &ast.TypeDecl{Name: nameT.Text, NameSpan: nameT.Span, Params: params, Ctors: ctors, RecordFields: recordFields, Deriving: deriving}
}

func (p *parser) parseRecordTypeFields() ([]ast.RecordFieldDef, bool) {
	p.next() // {
	if p.peek().Kind == token.RBRACE {
		p.errorAt(p.peek().Span, "RECORD FIELDS", "A record type needs at least one field.")
		return nil, false
	}
	var fields []ast.RecordFieldDef
	for {
		name := p.peekInExpr()
		if name.Kind != token.LIDENT {
			p.errorAt(name.Span, "SYNTAX PROBLEM", "I expect a lowercase record field name.")
			return nil, false
		}
		p.next()
		if !p.expect(token.COLON, "I expect `:` after the record field name.") {
			return nil, false
		}
		ty := p.parseTypeExpr()
		if ty == nil {
			return nil, false
		}
		fields = append(fields, ast.RecordFieldDef{Name: name.Text, NameSpan: name.Span, Type: ty})
		if p.peek().Kind != token.COMMA {
			break
		}
		p.next()
	}
	if p.peek().Kind != token.RBRACE {
		p.errorAt(p.peek().Span, "SYNTAX PROBLEM", "I expect `}` to close this record type.")
		return nil, false
	}
	p.next()
	return fields, true
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
	for p.peekInExpr().Kind == token.LIDENT || p.peekInExpr().Kind == token.UNDERSCORE {
		t := p.next()
		params = append(params, ast.Param{Name: t.Text, Sp: t.Span})
	}
	return params
}

// parseValueParams also accepts the nullary spelling `()`, represented by
// the same single discarded Unit parameter as the compatible `f _` form.
// It must be the declaration's sole syntactic parameter group.
func (p *parser) parseValueParams() []ast.Param {
	if p.peekInExpr().Kind == token.LPAREN && p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == token.RPAREN {
		lp := p.next()
		rp := p.next()
		if k := p.peekInExpr().Kind; k == token.LIDENT || k == token.UNDERSCORE || k == token.LPAREN {
			p.errorAt(p.peekInExpr().Span, "NULLARY PARAMETER LIST",
				"An empty Unit parameter list must be the only parameter group in a definition.")
			for p.peekInExpr().Kind != token.EQ && p.peekInExpr().Kind != token.EOF && p.peekInExpr().Pos().Line == lp.Pos().Line {
				p.next()
			}
		}
		return []ast.Param{{Name: "()", Sp: lp.Span.Merge(rp.Span)}}
	}
	return p.parseParams()
}

// parseBindBody dispatches on where a binding's body starts (doc/design.md, "Language semantics"): on the
// `=`'s line → inline expression; on a later line, deeper than the current
// layout column → a block at that column. Shared by top-level declarations,
// block bindings, local functions, and lambda bodies.
func (p *parser) parseBindBody(eqTok token.Token) ast.Expr {
	return p.parseBodyAfter(eqTok, "This binding has no expression — the next line does not belong\nto it.")
}

// parseBodyAfter is the shared inline-or-indented-block body rule for the
// tokens that introduce a body: `=`, `->`, `then`, and `else`. missing is the
// diagnostic for a following line that is offside, so a construct with no
// binding to name can describe itself instead.
func (p *parser) parseBodyAfter(introTok token.Token, missing string) ast.Expr {
	t := p.peek()
	switch {
	case t.Kind == token.EOF:
		p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
			"I got to the end of the input while still expecting an expression.")
		return nil
	case t.Pos().Line == introTok.Pos().Line:
		return p.parseExpr(1)
	case p.lay.checkOffside(t.Pos()) == offContinue:
		return p.parseBlock(t.Pos().Col)
	default:
		p.errorAt(p.prevSpan(), "SYNTAX PROBLEM", missing)
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
	case token.LPAREN:
		if inBounds(i+2) && p.toks[i+2].Kind == token.RPAREN && inBounds(i+3) && p.toks[i+3].Kind == token.EQ {
			return stmtLocalFn
		}
	}
	return stmtResult
}

// closesBlock reports whether a token at a block's own column ends the block
// instead of starting another statement. An `if` branch body indented level
// with its own `then`/`else` puts that keyword at the body block's column,
// and neither keyword can begin a statement.
func closesBlock(k token.Kind) bool {
	return k == token.KwThen || k == token.KwElse
}

// parseBlock parses a statement block at the given column: `name = expr`
// bindings (optionally annotated) followed by exactly one result expression.
// Zero-binding blocks collapse to the plain result expression.
func (p *parser) parseBlock(col int) ast.Expr {
	p.lay.push(ctxBlock, col)
	defer p.lay.pop()

	var binds []ast.LocalBind
	var items []ast.BlockItem
	hasExprStmt := false
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
			params := p.parseValueParams()
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
			items = append(items, ast.BlockItem{BindIndex: len(binds) - 1})
			pendingAnn = nil

		case stmtAnn:
			nameT := p.next()
			colon := p.next()
			if pendingAnn != nil {
				p.errorAt(nameT.Span, "MISSING DEFINITION",
					"The type annotation for `"+pendingAnnName.Text+"` must sit directly\nabove its binding.")
				return nil
			}
			preds := p.parseContext()
			te := p.parseTypeExpr()
			if te == nil {
				return nil
			}
			pendingAnn = &ast.TypeAnn{Type: te, Preds: preds, Sp: colon.Span.Merge(te.Span())}
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
			if nt := p.peek(); nt.Kind != token.EOF && nt.Pos().Col == col && !closesBlock(nt.Kind) {
				items = append(items, ast.BlockItem{BindIndex: -1, Expr: result})
				hasExprStmt = true
				continue
			}
			if len(binds) == 0 && !hasExprStmt {
				return result
			}
			if !hasExprStmt {
				items = nil
			}
			return &ast.Block{Binds: binds, Items: items, Result: result}
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
			if len(eff.Labels) == 0 && eff.Tail == "" {
				p.errorAt(eff.Sp, "REDUNDANT EFFECT ROW",
					"A pure arrow is written `->`; remove the empty effect row `{}`.")
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
		// A lone lowercase name, or one after a comma, is the compact row-tail
		// spelling: `{e}` / `{Console, e}`.
		if t.Kind == token.LIDENT {
			p.next()
			r.Tail, r.TailSp = t.Text, t.Span
			break
		}
		if t.Kind != token.UIDENT {
			p.errorAt(t.Span, "SYNTAX PROBLEM", "I expect a capitalized effect name in this row.")
			return nil
		}
		p.next()
		name, final, sp := p.parseQualifiedNameAfterFirst(t)
		if final != token.UIDENT {
			p.errorAt(sp, "SYNTAX PROBLEM", "An effect name must end in a capitalized identifier.")
			return nil
		}
		label := ast.EffLabelExpr{Name: name, NameSp: sp}
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
// cannot (no higher kinds, doc/design.md, "Go backend and runtime").
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
		name, final, sp := p.parseQualifiedName()
		if final != token.UIDENT {
			p.errorAt(sp, "SYNTAX PROBLEM", "A type name must end in a capitalized identifier.")
			return nil
		}
		return &ast.TName{Name: name, Sp: sp}
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

// binOp is the operator table, Elm's precedences: `||` 2 right-assoc;
// `&&` 3 right-assoc; comparisons 4 non-associative; `++` 5 right-assoc;
// `+ -` 6 left; `* /` 7 left.
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
	case token.ANDAND:
		return 3, assocRight
	case token.OROR:
		return 2, assocRight
	default:
		return 0, assocLeft
	}
}

// shortCircuit reports the operators that elaborate to an `if` rather than to
// a called value, so they cannot be bound by an `infix` declaration.
func shortCircuit(k token.Kind) bool {
	return k == token.ANDAND || k == token.OROR
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
// then any number of argument atoms. An immediately attached empty `()` is
// folded into its atom first, so `f x()` is `f (x ())`, while `f x ()`
// remains `(f x) ()`. `if` may head an expression but is not an atom, so
// `print if …` needs parens (as in Elm).
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
	fn := p.parsePostfixAtom()
	if fn == nil {
		return nil
	}
	for {
		switch p.peekInExpr().Kind {
		case token.INT, token.FLOAT, token.STRING, token.CHAR, token.LIDENT, token.UIDENT, token.LPAREN, token.LBRACE, token.KwResume, token.KwQuote, token.DOLLARPAREN:
			arg := p.parsePostfixAtom()
			if arg == nil {
				return nil
			}
			fn = &ast.App{Fn: fn, Arg: arg}
		default:
			return fn
		}
	}
}

// parsePostfixAtom consumes immediately adjacent empty Unit-call suffixes.
// Token spans, rather than token adjacency alone, distinguish `f()` from
// `f ()` and from `f{- comment -}()`.
func (p *parser) parsePostfixAtom() ast.Expr {
	expr := p.parseAtom()
	if expr == nil {
		return nil
	}
	for p.pos > 0 && p.pos < len(p.toks) {
		if p.peekInExpr().Kind == token.DOT {
			p.next()
			field := p.peekInExpr()
			if field.Kind != token.LIDENT {
				p.errorAt(field.Span, "SYNTAX PROBLEM", "I expect a lowercase field name after `.`.")
				return nil
			}
			p.next()
			expr = &ast.RecordGet{Record: expr, Field: field.Text, FieldSpan: field.Span}
			continue
		}
		if p.pos+1 >= len(p.toks) {
			return expr
		}
		lp := p.peekInExpr()
		if lp.Kind != token.LPAREN || p.toks[p.pos+1].Kind != token.RPAREN || p.toks[p.pos-1].Span.End != lp.Span.Start {
			return expr
		}
		p.next()
		rp := p.next()
		expr = &ast.App{Fn: expr, Arg: &ast.UnitLit{Sp: lp.Span.Merge(rp.Span)}}
	}
	return expr
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
		if op.Kind != token.LIDENT && op.Kind != token.UIDENT {
			p.errorAt(op.Span, "SYNTAX PROBLEM", "I expect a handler clause like `print value -> expression`.")
			return nil
		}
		var opName string
		var opSpan source.Span
		if op.Kind == token.UIDENT {
			var final token.Kind
			opName, final, opSpan = p.parseQualifiedName()
			if final != token.LIDENT {
				p.errorAt(opSpan, "SYNTAX PROBLEM", "A qualified handler operation must end in a lowercase name.")
				return nil
			}
		} else {
			p.next()
			opName, opSpan = op.Text, op.Span
		}
		params := p.parseClauseParams()
		arrow := p.peekInExpr()
		if !p.expect(token.ARROW, "I expect `->` after the handler clause parameters.") {
			return nil
		}
		clauseBody := p.parseBindBody(arrow)
		if clauseBody == nil {
			return nil
		}
		if opName == "return" {
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
			result.Clauses = append(result.Clauses, ast.HandleClause{Op: opName, OpSpan: opSpan, Params: params, Body: clauseBody})
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

func (p *parser) parseClauseParams() []ast.Param {
	var params []ast.Param
	for {
		if p.peekInExpr().Kind == token.LIDENT || p.peekInExpr().Kind == token.UNDERSCORE {
			t := p.next()
			params = append(params, ast.Param{Name: t.Text, Sp: t.Span})
			continue
		}
		if p.peekInExpr().Kind == token.LPAREN && p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == token.RPAREN {
			lp := p.next()
			rp := p.next()
			params = append(params, ast.Param{Name: "()", Sp: lp.Span.Merge(rp.Span)})
			continue
		}
		return params
	}
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
// doc/reference.md, "Modules, imports, and source layout"): a token at exactly that column starts a new branch, left of it ends
// the case. Branch bodies are statement blocks (doc/design.md, "Language semantics") or inline expressions.
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
		name, final, sp := p.parseQualifiedName()
		if final != token.UIDENT {
			p.errorAt(sp, "SYNTAX PROBLEM", "A constructor pattern must end in a capitalized name.")
			return nil
		}
		if p.peekInExpr().Kind == token.LBRACE {
			fields, end, ok := p.parseRecordPatternFields()
			if !ok {
				return nil
			}
			return &ast.PRecord{Name: name, NameSpan: sp, Fields: fields, Sp: sp.Merge(end)}
		}
		var args []ast.Pattern
		for isPatternAtomStart(p.peekInExpr().Kind) {
			a := p.parsePatternAtom()
			if a == nil {
				return nil
			}
			args = append(args, a)
		}
		return &ast.PCtor{Name: name, NameSpan: sp, Args: args}
	}
	return p.parsePatternAtom()
}

func isPatternAtomStart(k token.Kind) bool {
	switch k {
	case token.UNDERSCORE, token.CARET, token.LIDENT, token.UIDENT,
		token.INT, token.FLOAT, token.STRING, token.CHAR, token.LPAREN:
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
		name, final, sp := p.parseQualifiedName()
		if final != token.UIDENT {
			p.errorAt(sp, "SYNTAX PROBLEM", "A constructor pattern must end in a capitalized name.")
			return nil
		}
		if p.peekInExpr().Kind == token.LBRACE {
			fields, end, ok := p.parseRecordPatternFields()
			if !ok {
				return nil
			}
			return &ast.PRecord{Name: name, NameSpan: sp, Fields: fields, Sp: sp.Merge(end)}
		}
		return &ast.PCtor{Name: name, NameSpan: sp}
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
	case token.CHAR:
		p.next()
		return &ast.PChar{Value: lexer.UnescapeChar(t.Text), Sp: t.Span}
	case token.CARET:
		caret := p.next()
		n := p.peekInExpr()
		if n.Kind == token.LIDENT {
			p.next()
			return &ast.PPin{Name: n.Text, NameSpan: n.Span, Sp: caret.Span.Merge(n.Span)}
		}
		if n.Kind == token.UIDENT {
			name, final, sp := p.parseQualifiedName()
			if final == token.LIDENT {
				return &ast.PPin{Name: name, NameSpan: sp, Sp: caret.Span.Merge(sp)}
			}
		}
		p.errorAt(n.Span, "SYNTAX PROBLEM", "A pinned pattern needs an existing lowercase value name after `^`.")
		return nil
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

func (p *parser) parseRecordPatternFields() ([]ast.RecordPatternField, source.Span, bool) {
	p.next()
	if p.peek().Kind == token.RBRACE {
		rb := p.next()
		return nil, rb.Span, true
	}
	var fields []ast.RecordPatternField
	for {
		name := p.peekInExpr()
		if name.Kind != token.LIDENT {
			p.errorAt(name.Span, "SYNTAX PROBLEM", "I expect a lowercase record field name.")
			return nil, source.Span{}, false
		}
		p.next()
		if !p.expect(token.EQ, "I expect `=` after the record field name in this pattern.") {
			return nil, source.Span{}, false
		}
		pat := p.parsePattern()
		if pat == nil {
			return nil, source.Span{}, false
		}
		fields = append(fields, ast.RecordPatternField{Name: name.Text, NameSpan: name.Span, Pattern: pat})
		if p.peek().Kind != token.COMMA {
			break
		}
		p.next()
	}
	if p.peek().Kind != token.RBRACE {
		p.errorAt(p.peek().Span, "SYNTAX PROBLEM", "I expect `}` to close this record pattern.")
		return nil, source.Span{}, false
	}
	rb := p.next()
	return fields, rb.Span, true
}

// parseIf parses `if condition then a else b`. The `if` token's column
// anchors the whole chain (doc/reference.md, "Modules, imports, and source layout"): `then` and `else` may sit
// exactly there even when that is the innermost layout column, and an
// `else if` on the `else`'s line keeps the same anchor, so every arm of a
// chain aligns under one `if`. Branches are inline expressions or indented
// statement blocks (doc/design.md, "Language semantics").
func (p *parser) parseIf() ast.Expr {
	return p.parseIfChain(p.peek().Pos().Col)
}

func (p *parser) parseIfChain(anchor int) ast.Expr {
	ifTok := p.next()
	cond := p.parseExpr(1)
	if cond == nil {
		return nil
	}
	p.allowAtAnchor(anchor, token.KwThen)
	thenTok := p.peekInExpr()
	if !p.expect(token.KwThen, "I expect `then` after an `if` condition.") {
		return nil
	}
	thenE := p.parseBodyAfter(thenTok, "This `then` has no expression — the next line does not belong\nto it.")
	if thenE == nil {
		return nil
	}
	p.allowAtAnchor(anchor, token.KwElse)
	elseTok := p.peekInExpr()
	if !p.expect(token.KwElse, "I expect `else` after the `then` branch — every `if` needs one.") {
		return nil
	}
	var elseE ast.Expr
	// `else if` on one line is one chain, not a nested `if` re-anchored at
	// the inner `if`: the arms that follow still align with the outer one.
	if t := p.peekInExpr(); t.Kind == token.KwIf && t.Pos().Line == elseTok.Pos().Line {
		elseE = p.parseIfChain(anchor)
	} else {
		elseE = p.parseBodyAfter(elseTok, "This `else` has no expression — the next line does not belong\nto it.")
	}
	if elseE == nil {
		return nil
	}
	return &ast.If{Cond: cond, Then: thenE, Else: elseE, Sp: ifTok.Span}
}

// allowAtAnchor exempts one `then`/`else` token sitting exactly at its `if`
// chain's anchor column, which peekInExpr would otherwise read as a sibling
// boundary. Neither keyword can start a statement or a branch, so the
// exemption cannot swallow a following construct.
func (p *parser) allowAtAnchor(anchor int, k token.Kind) {
	if t := p.peek(); t.Kind == k && t.Pos().Col == anchor {
		p.stmtStart = p.pos
	}
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
	case token.CHAR:
		p.next()
		return &ast.CharLit{Value: lexer.UnescapeChar(t.Text), Sp: t.Span}
	case token.LIDENT:
		p.next()
		return &ast.Var{Name: t.Text, Sp: t.Span}
	case token.UIDENT:
		name, final, sp := p.parseQualifiedName()
		if final == token.UIDENT && p.peekInExpr().Kind == token.LBRACE {
			fields, end, ok := p.parseRecordExprFields()
			if !ok {
				return nil
			}
			return &ast.RecordLit{Name: name, NameSpan: sp, Fields: fields, Sp: sp.Merge(end)}
		}
		if final == token.LIDENT {
			return &ast.Var{Name: name, Sp: sp}
		}
		return &ast.Ctor{Name: name, Sp: sp}
	case token.LBRACE:
		lb := p.next()
		record := p.parseExpr(1)
		if record == nil || !p.expect(token.PIPE, "I expect `|` after the record being updated.") {
			return nil
		}
		fields, end, ok := p.parseRecordExprFieldsAfterOpen()
		if !ok {
			return nil
		}
		return &ast.RecordUpdate{Record: record, Fields: fields, Sp: lb.Span.Merge(end)}
	case token.KwResume:
		p.next()
		return &ast.Resume{Sp: t.Span}
	case token.KwQuote:
		// `quote` takes exactly one atom, so `quote (f x)` needs its parens
		// the way every other argument position does. Nothing about the
		// quoted text is parsed differently — it is ordinary fango syntax.
		p.next()
		p.usesStaging = true
		body := p.parsePostfixAtom()
		if body == nil {
			return nil
		}
		return &ast.Quote{Body: body, Sp: t.Span.Merge(body.Span())}
	case token.KwTypeOf:
		p.usesStaging = true
		p.next()
		ty := p.parseTypeExpr()
		if ty == nil {
			return nil
		}
		return &ast.TypeOf{Ty: ty, Sp: t.Span.Merge(ty.Span())}
	case token.DOLLARPAREN:
		p.next()
		p.usesStaging = true
		operand := p.parseExpr(1)
		if operand == nil {
			return nil
		}
		if inner := p.peekInExpr(); inner.Kind == token.RPAREN {
			p.next()
		} else if inner.Kind == token.EOF && p.peek().Kind == token.EOF {
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"I got to the end of the input while looking for the `)` that closes this splice.")
			return nil
		} else {
			p.errorAt(inner.Span, "SYNTAX PROBLEM", "I was expecting the `)` that closes this splice.")
			return nil
		}
		return &ast.Splice{Operand: operand, Sp: t.Span.Merge(p.prevSpan())}
	case token.LPAREN:
		lp := p.next()
		if p.peekInExpr().Kind == token.RPAREN {
			rp := p.next()
			return &ast.UnitLit{Sp: lp.Span.Merge(rp.Span)}
		}
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

func (p *parser) parseRecordExprFields() ([]ast.RecordExprField, source.Span, bool) {
	p.next() // {
	return p.parseRecordExprFieldsAfterOpen()
}

func (p *parser) parseRecordExprFieldsAfterOpen() ([]ast.RecordExprField, source.Span, bool) {
	if p.peek().Kind == token.RBRACE {
		rb := p.next()
		return nil, rb.Span, true
	}
	var fields []ast.RecordExprField
	for {
		name := p.peekInExpr()
		if name.Kind != token.LIDENT {
			p.errorAt(name.Span, "SYNTAX PROBLEM", "I expect a lowercase record field name.")
			return nil, source.Span{}, false
		}
		p.next()
		if !p.expect(token.EQ, "I expect `=` after the record field name.") {
			return nil, source.Span{}, false
		}
		value := p.parseExpr(1)
		if value == nil {
			return nil, source.Span{}, false
		}
		fields = append(fields, ast.RecordExprField{Name: name.Text, NameSpan: name.Span, Value: value})
		if p.peek().Kind != token.COMMA {
			break
		}
		p.next()
	}
	if p.peek().Kind != token.RBRACE {
		p.errorAt(p.peek().Span, "SYNTAX PROBLEM", "I expect `}` to close these record fields.")
		return nil, source.Span{}, false
	}
	p.next()
	return fields, p.prevSpan(), true
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
