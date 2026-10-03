// Package parser is a hand-written recursive-descent parser. It enforces the
// offside rule via token columns (layout.go); the lexer is layout-oblivious.
//
// Operator expressions parse into a flat ast.OpChain rather than a tree:
// fixity is declared in source and a file is parsed before the module graph
// exists, so internal/fixity groups the runs afterwards.
package parser

import (
	"strconv"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/fixity"
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
	// exprParenDepth tracks expression delimiters that reset layout: grouping
	// parentheses, braced lambdas, and quotations. Their closing token supplies
	// the boundary.
	exprParenDepth int

	// stmtStart is the index of a token allowed to sit exactly at the
	// innermost layout column: a block statement's opening token (and, in
	// a case branch's first pattern token), or a `then`/`else` at its own
	// `if` chain's anchor column. Everywhere else, a token at the column is
	// a sibling boundary, not expression content.
	stmtStart int

	// usesStaging records that this file built a quote or a splice, so the
	// module loader knows to pull in the bundled `Meta` module.
	usesStaging bool
	usesLists   bool
	usesTuples  bool
	usesRegex   bool

	// stopWith makes the contextual word `with` terminate only the subject
	// of a handle expression. It remains an ordinary identifier elsewhere.
	stopWith int
}

// Parse parses a whole module.
func Parse(toks []token.Token, f *source.File) (*ast.Module, []diag.Error) {
	p := &parser{f: f, toks: toks, stmtStart: -1}
	p.lay.push(ctxDecl, 1)
	m := &ast.Module{}
	p.parsePragmas(m)
	m.Header = p.parseHeader()
	for p.peek().Kind == token.KwImport {
		if im, ok := p.parseImport(); ok {
			m.Imports = append(m.Imports, im)
		}
	}
	for p.peek().Kind != token.EOF {
		declStart := p.pos
		if d := p.parseDecl(); d != nil {
			sp := p.spanOfTokens(declStart, p.pos)
			ast.SetDeclSpan(d, sp)
			if vd, ok := d.(*ast.ValueDecl); ok && len(m.Decls) > 0 {
				prev, _ := m.Decls[len(m.Decls)-1].(*ast.ValueDecl)
				if p.appendEquation(prev, vd) {
					// The group is one declaration, so its extent grows to
					// cover the row just folded into it.
					prev.Sp = prev.Sp.Merge(sp)
					continue
				}
			}
			m.Decls = append(m.Decls, d)
		}
	}
	m.UsesStaging = p.usesStaging
	m.UsesLists = p.usesLists
	m.UsesTuples = p.usesTuples
	m.UsesRegex = p.usesRegex
	return m, p.errs
}

// ParseExprInput parses a single expression covering the whole input — the
// REPL's expression entry point. No column discipline applies.
func ParseExprInput(toks []token.Token, f *source.File) (ast.Expr, []diag.Error) {
	p := &parser{f: f, toks: toks, stmtStart: -1}
	p.lay.push(ctxDecl, 0) // column 0: nothing is ever offside
	e := p.parseExpr()
	if t := p.peek(); t.Kind != token.EOF && len(p.errs) == 0 {
		p.errorAt(t.Span, "SYNTAX PROBLEM", "I parsed a complete expression but then ran into this.")
	}
	return e, p.errs
}

// IsUseInput reports whether a prompt input is a `use` item.
func IsUseInput(toks []token.Token) bool {
	return len(toks) > 0 && toks[0].Kind == token.KwUse
}

// ParseUseInput parses a `use` item typed at the prompt, where the rest of
// the block is the rest of the session: it returns the item's binder patterns,
// if any, and its head.
func ParseUseInput(toks []token.Token, f *source.File) (*ast.UseSugar, []ast.Pattern, ast.Expr, []diag.Error) {
	p := &parser{f: f, toks: toks, stmtStart: -1}
	p.lay.push(ctxDecl, 0)
	w, params, head := p.parseUseHead()
	if t := p.peek(); head != nil && t.Kind != token.EOF && len(p.errs) == 0 {
		p.errorAt(t.Span, "SYNTAX PROBLEM", "I parsed a complete `use` item but then ran into this.")
	}
	return w, params, head, p.errs
}

func isScopedPragma(text string) bool {
	fields := strings.Fields(text)
	return len(fields) > 0 && fields[0] == "scoped"
}

// parsePragmas consumes the `{-# ... #-}` directives that may precede the
// module header. Declaration-local markers are left for parseDecl.
func (p *parser) parsePragmas(m *ast.Module) {
	for p.peek().Kind == token.PRAGMA && p.peek().Text != "resource" && !isScopedPragma(p.peek().Text) {
		t := p.next()
		if !p.applyPragma(m, t) {
			p.errorAt(t.Span, "UNKNOWN PRAGMA", "I don't know the pragma `"+t.Text+"`. Use `no-prelude` above the module header, `resource` before a type declaration, or `scoped row` before an annotated function.")
		}
	}
}

func (p *parser) applyPragma(m *ast.Module, t token.Token) bool {
	if t.Text != "no-prelude" {
		return false
	}
	m.NoPrelude = true
	return true
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
		// An operator is exposed by its `(op)` spelling: `exposing ((++))`.
		if t.Kind == token.LPAREN {
			op, sp, ok := p.parseOpName()
			if !ok {
				return ex, false
			}
			ex.Items = append(ex.Items, ast.ExposeItem{Name: op, Sp: sp})
			if p.peek().Kind != token.COMMA {
				break
			}
			p.next()
			continue
		}
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
	if t.Kind == token.ATTRIBUTE {
		groups := p.parseAttributes()
		var d ast.Decl
		if p.peek().Kind == token.KwType {
			d = p.parseTypeDecl()
		} else {
			d = p.parseDecl()
		}
		if td, ok := d.(*ast.TypeDecl); ok {
			td.Attributes = groups
			td.Sp = t.Span.Merge(td.Sp)
			return td
		}
		p.errorAt(t.Span, "ATTRIBUTE TARGET", "An attribute here must precede a type declaration.")
		return d
	}
	if t.Kind == token.PRAGMA {
		p.next()

		if isScopedPragma(t.Text) {
			fields := strings.Fields(t.Text)
			if len(fields) != 2 {
				p.errorAt(t.Span, "SCOPED CALLBACK", "Use `{-# scoped row #-}` before an annotated function.")
				return nil
			}
			d := p.parseDecl()
			if vd, ok := d.(*ast.ValueDecl); ok && vd.Ann != nil && vd.ScopedRow == "" {
				vd.ScopedRow = fields[1]
				vd.Sp = t.Span.Merge(vd.Sp)
				return vd
			}
			p.errorAt(t.Span, "SCOPED CALLBACK", "A scoped row marker must precede one annotated function.")
			return d
		}
		if t.Text != "resource" {
			p.errorAt(t.Span, "MISPLACED PRAGMA", "Only `resource` and `scoped` pragmas may precede declarations; file pragmas belong above the module header.")
			return nil
		}
		if p.peek().Kind == token.EOF {
			p.errorAt(t.Span, TitleUnexpectedEOF, "After `{-# "+t.Text+" #-}` I expect a type declaration.")
			return nil
		}
		if p.peek().Kind != token.KwType {
			p.errorAt(p.peek().Span, "MISPLACED RESOURCE PRAGMA", "`{-# "+t.Text+" #-}` must immediately precede one type declaration.")
			return nil
		}
		d := p.parseDecl()
		if td, ok := d.(*ast.TypeDecl); ok {
			td.Resource = true
			td.ResourceSpan = t.Span
		}
		return d
	}
	if t.Kind == token.KwType {
		return p.parseTypeDecl()
	}
	if t.Kind == token.KwEffect {
		return p.parseEffectDecl()
	}
	if t.Kind == token.KwInfix || t.Kind == token.KwInfixL || t.Kind == token.KwInfixR {
		return p.parseFixityDecl()
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
	// A declaration is named by a lowercase identifier or, for an operator,
	// by the `(op)` spelling.
	var declName string
	var nameSpan source.Span
	_, startsOpName := p.opNameAt(p.pos)
	switch {
	case t.Kind == token.LIDENT:
		declName, nameSpan = t.Text, t.Span
		p.next()
	case t.Kind == token.LPAREN && startsOpName:
		var ok bool
		if declName, nameSpan, ok = p.parseOpName(); !ok {
			// The `(` sits at column 1, so recovery must consume it or the
			// declaration loop would spin on it.
			p.recoverToTopLevel(true)
			return nil
		}
	default:
		if startsPatternDecl(t.Kind) {
			p.stmtStart = p.pos
			pat := p.parsePattern()
			eq := p.peekInExpr()
			if !p.expect(token.EQ, "I expect `=` after the binding pattern.") {
				p.recoverToTopLevel(false)
				return nil
			}
			body := p.parseBindBody(eq)
			if body == nil {
				p.recoverToTopLevel(false)
				return nil
			}
			return &ast.PatternDecl{Pattern: pat, Body: body}
		}
		p.errorAt(t.Span, "SYNTAX PROBLEM",
			"I was expecting a declaration here, like `name = expression`.")
		p.recoverToTopLevel(true) // the bad token sits at column 1: must consume it
		return nil
	}
	shown := ast.Spelling(declName)

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
			p.errorAt(nameSpan, TitleUnexpectedEOF,
				"I see a type annotation for `"+shown+"` but no definition for it yet.")
			return nil
		}
		repeated, isOpName := p.opNameAt(p.pos)
		if !isOpName {
			repeated = nt.Text
			isOpName = nt.Kind == token.LIDENT
		}
		if !isOpName && nt.Pos().Col == 1 && startsPatternDecl(nt.Kind) {
			p.errorAt(nt.Span, "DESTRUCTURING ANNOTATION",
				"A destructuring binding cannot have a direct type annotation; annotate a named subject first.")
			p.recoverToTopLevel(false)
			return nil
		}
		if !isOpName || repeated != declName || nt.Pos().Col != 1 {
			p.errorAt(nt.Span, "MISSING DEFINITION",
				"The type annotation for `"+shown+"` must sit directly above its\ndefinition, like:\n\n    "+shown+" : Int\n    "+shown+" = 42")
			p.recoverToTopLevel(false)
			return nil
		}
		// Consume the repeated name: one token, or three for `(op)`.
		if nt.Kind == token.LPAREN {
			p.next()
			p.next()
		}
		p.next()
	}

	params := p.parseValueParams(nameSpan)
	eqTok := p.peekInExpr()
	if !p.expect(token.EQ, "I expect `=` after the name in a declaration.") {
		p.recoverToTopLevel(false)
		return nil
	}
	if p.peekInExpr().Kind == token.KwNative {
		n := p.parseNativeBody()
		if ann == nil {
			p.errorAt(nameSpan, "NATIVE DECLARATION", "A native declaration requires a type annotation.")
		}
		if len(params) > 0 {
			p.errorAt(nameSpan, "NATIVE DECLARATION", "A native declaration cannot have source parameters; put its complete function type in the annotation.")
		}
		return &ast.ValueDecl{Name: declName, NameSpan: nameSpan, Params: params, Ann: ann, Native: n}
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
	return &ast.ValueDecl{Name: declName, NameSpan: nameSpan, Params: params, Ann: ann, Body: body}
}

// startsPatternDecl reports whether a token at column 1 can begin a top-level
// destructuring binding rather than a named declaration.
func startsPatternDecl(k token.Kind) bool {
	return k == token.UIDENT || k == token.LBRACKET || k == token.LPAREN
}

func (p *parser) parseDeriverDecl() ast.Decl {
	start := p.next()
	class, _, sp := p.parseQualifiedName()
	d := &ast.DeriverDecl{Class: class, ClassSpan: start.Span.Merge(sp)}
	first := p.peek()
	if first.Kind == token.EOF {
		// The prompt's continuation signal: the block may still arrive.
		p.errorAt(d.ClassSpan, TitleUnexpectedEOF, "This deriver needs method definitions.")
		return nil
	}
	if first.Pos().Col <= 1 {
		p.errorAt(first.Span, "DERIVER METHOD", "A deriver needs indented method definitions.")
		return nil
	}
	col := first.Pos().Col
	p.lay.push(ctxBlock, col)
	defer p.lay.pop()
	for p.peek().Kind != token.EOF && p.peek().Pos().Col > 1 && p.peek().Pos().Col <= col {
		p.stmtStart = p.pos
		name, nameSpan, ok := p.parseMethodName("DERIVER METHOD",
			"I expect a method name here, like `show` or `(==)`.")
		if !ok {
			return nil
		}
		params := p.parseValueParams(nameSpan)
		if !p.expect(token.EQ, "I expect `=` after the deriver method parameters.") {
			return nil
		}
		body := p.parseBindBody(p.toks[p.pos-1])
		if body == nil {
			return nil
		}
		d.Methods = p.groupMethod(d.Methods, &ast.ValueDecl{Name: name, NameSpan: nameSpan, Params: params, Body: body})
	}
	return d
}

func (p *parser) groupMethod(methods []*ast.ValueDecl, next *ast.ValueDecl) []*ast.ValueDecl {
	var prev *ast.ValueDecl
	if len(methods) > 0 {
		prev = methods[len(methods)-1]
	}
	if p.appendEquation(prev, next) {
		return methods
	}
	return append(methods, next)
}

// appendEquation folds next into prev's equation group when the two are
// contiguous rows of one definition, reporting whether it did. Only definitions
// with arguments form groups: a repeated zero-argument value is a duplicate
// definition, and one annotation governs a whole group, so a row carrying its
// own annotation starts a new definition instead.
func (p *parser) appendEquation(prev, next *ast.ValueDecl) bool {
	if prev == nil || next.Ann != nil || next.Native != nil || prev.Native != nil {
		return false
	}
	if prev.Name != next.Name || len(prev.Params) == 0 || len(next.Params) == 0 {
		return false
	}
	if len(prev.Params) != len(next.Params) {
		p.errorAt(next.NameSpan, "INCONSISTENT ARITY", "Adjacent equations for `"+ast.Spelling(next.Name)+"` must have the same number of arguments.")
		return false
	}
	if len(prev.Equations) == 0 {
		prev.Equations = []ast.Equation{{Params: prev.Params, Body: prev.Body, NameSpan: prev.NameSpan}}
	}
	prev.Equations = append(prev.Equations, ast.Equation{Params: next.Params, Body: next.Body, NameSpan: next.NameSpan})
	return true
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

// parseOpName consumes the `(op)` spelling that names an operator wherever a
// declaration names a value: top-level definitions and annotations, class
// and effect signatures, instance and deriver methods, fixity declarations,
// and exposing lists. The span covers the whole `(op)`, so a diagnostic
// underlines the form the author wrote.
// Token positions are read raw rather than through peekInExpr: a `(op)` in a
// naming position sits at its construct's own layout column, where
// peekInExpr would synthesize EOF. Whether that column is the right one is
// the caller's check.
func (p *parser) parseOpName() (string, source.Span, bool) {
	lp := p.at(p.pos)
	op := p.at(p.pos + 1)
	if lp.Kind != token.LPAREN || op.Kind != token.OP || p.at(p.pos+2).Kind != token.RPAREN {
		p.errorAt(lp.Span, "SYNTAX PROBLEM", "I expect an operator in parentheses here, like `(+)`.")
		return "", lp.Span, false
	}
	if !p.bindableOp(op) {
		return "", op.Span, false
	}
	p.next()
	p.next()
	rp := p.next()
	return op.Text, lp.Span.Merge(rp.Span), true
}

// parseMethodName consumes the name of a class signature, an instance
// method, or a deriver method: a lowercase identifier, or `(op)` when the
// class method is an operator. A class that declares `(+)` needs its
// instances and derivers to spell the method the same way.
func (p *parser) parseMethodName(title, expected string) (string, source.Span, bool) {
	t := p.at(p.pos)
	switch t.Kind {
	case token.LIDENT:
		p.next()
		return t.Text, t.Span, true
	case token.LPAREN:
		return p.parseOpName()
	}
	p.errorAt(t.Span, title, expected)
	return "", t.Span, false
}

// opNameAt reports the `(op)` starting at token i without consuming it. The
// annotation adjacency check needs the next declaration's name before it
// decides to consume anything.
func (p *parser) opNameAt(i int) (string, bool) {
	if p.at(i).Kind == token.LPAREN && p.at(i+1).Kind == token.OP && p.at(i+2).Kind == token.RPAREN {
		return p.at(i + 1).Text, true
	}
	return "", false
}

// at returns the token at an absolute index, or EOF past the end.
func (p *parser) at(i int) token.Token {
	if i < 0 || i >= len(p.toks) {
		return p.toks[len(p.toks)-1]
	}
	return p.toks[i]
}

// bindableOp rejects the operators that cannot name a value: `&&` and `||`
// are elaborated to an `if` rather than called, so there is nothing to name.
// Reserved lexemes never reach here — they have their own token kinds and so
// never appear as OP.
func (p *parser) bindableOp(op token.Token) bool {
	if fixity.IsShortCircuit(op.Text) {
		p.errorAt(op.Span, "RESERVED OPERATOR",
			"("+op.Text+") is short-circuiting syntax elaborated to an `if`, not a\ncall, so there is no ("+op.Text+") value to name.")
		return false
	}
	return true
}

// parseFixityDecl parses `infixl 6 (+)`, declaring one operator's precedence
// and associativity. A rejected declaration recovers to the next top-level
// declaration rather than leaving the parser mid-line, where the unconsumed
// operator would also be reported as a stray declaration.
func (p *parser) parseFixityDecl() ast.Decl {
	kw := p.next()
	assoc := ast.AssocNone
	switch kw.Kind {
	case token.KwInfixL:
		assoc = ast.AssocLeft
	case token.KwInfixR:
		assoc = ast.AssocRight
	}
	level := p.peekInExpr()
	prec, ok := fixityLevel(level)
	if !ok {
		p.errorAt(level.Span, "FIXITY DECLARATION",
			"After `"+kw.Text+"` I expect a precedence level from "+itoa(fixity.MinPrec)+" through "+itoa(fixity.MaxPrec)+",\nthen the operator, like `"+kw.Text+" 6 (+)`.")
		p.recoverToTopLevel(false)
		return nil
	}
	p.next()
	op, opSpan, ok := p.parseOpName()
	if !ok {
		p.recoverToTopLevel(false)
		return nil
	}
	return &ast.FixityDecl{Op: op, Assoc: assoc, Prec: prec, OpSpan: opSpan, Sp: kw.Span.Merge(opSpan)}
}

// fixityLevel reads a precedence level, which must be a plain integer
// literal within the declared range.
func fixityLevel(t token.Token) (int, bool) {
	if t.Kind != token.INT {
		return 0, false
	}
	n, err := strconv.Atoi(t.Text)
	if err != nil || n < fixity.MinPrec || n > fixity.MaxPrec {
		return 0, false
	}
	return n, true
}

func itoa(n int) string { return strconv.Itoa(n) }

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
	if first.Kind == token.EOF || first.Pos().Col <= 1 {
		return &ast.EffectDecl{Name: nameT.Text, NameSpan: nameT.Span, Params: params}
	}
	col := first.Pos().Col
	p.lay.push(ctxBlock, col)
	defer p.lay.pop()
	var ops []ast.OpSig
	for {
		p.stmtStart = p.pos
		abort := false
		if p.peekInExpr().Kind == token.KwAbort {
			abort = true
			p.next()
		}
		opName, opSpan, ok := p.parseMethodName("SYNTAX PROBLEM",
			"I expect an operation name here, like `print : String -> ()`.")
		if !ok {
			return nil
		}
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
		ops = append(ops, ast.OpSig{Name: opName, NameSpan: opSpan, Type: ty, Native: native, Abort: abort})
		nt := p.peek()
		if nt.Kind == token.EOF || nt.Pos().Col <= 1 {
			break
		}
		if nt.Pos().Col > col {
			p.errorAt(nt.Span, "SYNTAX PROBLEM", "This line is indented past the first operation signature and reads as a continuation.")
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
		field := ast.RecordFieldDef{Attributes: p.parseAttributes()}
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
		field.Name, field.NameSpan, field.Type = name.Text, name.Span, ty
		field.Attributes = append(field.Attributes, p.parseAttributes()...)
		fields = append(fields, field)
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

// parseAttributes uses normal expression tokens and delimiter-aware parsing.
func (p *parser) parseAttributes() []ast.AttributeGroup {
	var groups []ast.AttributeGroup
	for p.peek().Kind == token.ATTRIBUTE {
		start := p.next()
		p.usesStaging = true
		p.exprParenDepth++
		p.lay.push(ctxParen, 0)
		group := ast.AttributeGroup{}
		if p.peek().Kind == token.RBRACKET {
			p.errorAt(p.peek().Span, "ATTRIBUTE", "An attribute group needs at least one expression.")
		} else {
			for {
				e := p.parseExpr()
				if e == nil {
					break
				}
				group.Exprs = append(group.Exprs, e)
				if p.peek().Kind != token.COMMA {
					break
				}
				p.next()
				if p.peek().Kind == token.RBRACKET {
					break
				}
			}
		}
		end := p.peek().Span
		p.expectRaw(token.RBRACKET, "I expect `]` to close this attribute group.")
		p.lay.pop()
		p.exprParenDepth--
		group.Sp = start.Span.Merge(end)
		groups = append(groups, group)
	}
	return groups
}

// parseCtorDef parses one constructor alternative: a capitalized name
// followed by zero or more type atoms (applications need parens: `Cons a (List a)`).
func (p *parser) parseCtorDef() (ast.CtorDef, bool) {
	attrs := p.parseAttributes()
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
	var fieldAttrs [][]ast.AttributeGroup
	for isTypeAtomStart(p.peekInExpr().Kind) || p.peekInExpr().Kind == token.ATTRIBUTE {
		fieldAttrs = append(fieldAttrs, p.parseAttributes())
		a := p.parseTypeAtom()
		if a == nil {
			return ast.CtorDef{}, false
		}
		args = append(args, a)
	}
	return ast.CtorDef{Name: t.Text, NameSpan: t.Span, Args: args, Attributes: attrs, FieldAttributes: fieldAttrs}, true
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
func (p *parser) parseValueParams(nameSpan source.Span) []ast.Pattern {
	if p.peekInExpr().Kind == token.LPAREN && p.pos+1 < len(p.toks) && p.toks[p.pos+1].Kind == token.RPAREN {
		lp := p.next()
		rp := p.next()
		// Attached f() remains the sole-argument Unit-function spelling. With
		// whitespace, () is an ordinary Unit pattern and more arguments follow.
		if nameSpan.End == lp.Span.Start {
			if isPatternAtomStart(p.peekInExpr().Kind) {
				p.errorAt(p.peekInExpr().Span, "NULLARY PARAMETER LIST",
					"An empty Unit parameter list must be the only parameter group in a definition.")
				for p.peekInExpr().Kind != token.EQ && p.peekInExpr().Kind != token.EOF && p.peekInExpr().Pos().Line == lp.Pos().Line {
					p.next()
				}
			}
			return []ast.Pattern{&ast.PUnit{Sp: lp.Span.Merge(rp.Span)}}
		}
		params := []ast.Pattern{&ast.PUnit{Sp: lp.Span.Merge(rp.Span)}}
		for isPatternAtomStart(p.peekInExpr().Kind) {
			params = append(params, p.parsePatternAtom())
		}
		return params
	}
	var params []ast.Pattern
	for isPatternAtomStart(p.peekInExpr().Kind) {
		params = append(params, p.parsePatternAtom())
	}
	return params
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
		return p.parseInlineBlock()
	case p.lay.checkOffside(t.Pos()) == offContinue:
		return p.parseBlock(t.Pos().Col)
	case introTok.Kind == token.ARROW && p.exprParenDepth > 0:
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
	stmtPatternBind
	// stmtLocalOp is `(op) params… =` in a block: not a supported binding,
	// but recognized so it gets its own diagnostic rather than the generic
	// "expression seemed complete" one.
	stmtLocalOp
	// stmtUse is `use head` or `use patterns <- head`: the rest of the
	// block becomes the head's final callback argument.
	stmtUse
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
	if p.toks[i].Kind == token.KwUse {
		return stmtUse
	}
	if _, isOpName := p.opNameAt(i); isOpName {
		// `(+) 1 2` is an application; only a following `=` makes it a
		// definition attempt.
		j := i + 3
		for inBounds(j) && (p.toks[j].Kind == token.LIDENT || p.toks[j].Kind == token.UNDERSCORE) {
			j++
		}
		if inBounds(j) && p.toks[j].Kind == token.EQ {
			return stmtLocalOp
		}
		return stmtResult
	}
	if p.toks[i].Kind != token.LIDENT {
		if isPatternAtomStart(p.toks[i].Kind) && hasStatementEqual(p.toks, i, col) {
			return stmtPatternBind
		}
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
	}
	if hasStatementEqual(p.toks, i+1, col) {
		return stmtLocalFn
	}
	return stmtResult
}

func hasStatementEqual(toks []token.Token, start, col int) bool {
	depth := 0
	for i := start; i < len(toks); i++ {
		t := toks[i]
		if t.Kind == token.EOF || (i > start && depth == 0 && t.Pos().Col <= col) {
			return false
		}
		switch t.Kind {
		case token.LPAREN, token.LBRACKET, token.LBRACE, token.DOLLARPAREN, token.ATTRIBUTE, token.LQUOTE:
			depth++
		case token.RPAREN, token.RBRACKET, token.RBRACE, token.RQUOTE:
			if depth > 0 {
				depth--
			}
		case token.EQ:
			if depth == 0 {
				return true
			}
		case token.SEMICOLON:
			if depth == 0 {
				return false
			}
		}
	}
	return false
}

// classifyInlineStmt is classifyStmt with a source-position boundary instead
// of a layout-column boundary. An inline item's continuation may be deeper on
// a later line, but an aligned or outdented token starts the surrounding
// layout's next item. This keeps an ordinary body such as `x = 2` from looking
// ahead into a following indented `y = 3` and misclassifying `2` as a pattern.
func (p *parser) classifyInlineStmt() stmtKind {
	base := p.toks[p.pos].Pos()
	inBounds := func(i int) bool {
		t := p.toks[i]
		if t.Kind == token.EOF || t.Kind == token.SEMICOLON {
			return false
		}
		return t.Pos().Line == base.Line || t.Pos().Col > base.Col
	}
	hasEqual := func(start int) bool {
		depth := 0
		for i := start; i < len(p.toks); i++ {
			t := p.toks[i]
			if !inBounds(i) && i > start && depth == 0 {
				return false
			}
			if depth == 0 {
				switch t.Kind {
				case token.RPAREN, token.RBRACKET, token.RBRACE, token.RQUOTE, token.COMMA,
					token.ARROW, token.KwThen, token.KwElse, token.KwOf,
					token.KwIf, token.KwCase, token.KwHandle:
					return false
				}
				if p.stopWith > 0 && p.handlerStateStart(i) {
					return false
				}
			}
			switch t.Kind {
			case token.LPAREN, token.LBRACKET, token.LBRACE, token.DOLLARPAREN, token.ATTRIBUTE, token.LQUOTE:
				depth++
			case token.RPAREN, token.RBRACKET, token.RBRACE, token.RQUOTE:
				if depth > 0 {
					depth--
				}
			case token.EQ:
				if depth == 0 {
					return true
				}
			case token.SEMICOLON:
				if depth == 0 {
					return false
				}
			}
		}
		return false
	}

	i := p.pos
	if p.toks[i].Kind == token.KwUse {
		return stmtUse
	}
	if _, isOpName := p.opNameAt(i); isOpName {
		j := i + 3
		for inBounds(j) && (p.toks[j].Kind == token.LIDENT || p.toks[j].Kind == token.UNDERSCORE) {
			j++
		}
		if inBounds(j) && p.toks[j].Kind == token.EQ {
			return stmtLocalOp
		}
		return stmtResult
	}
	if p.toks[i].Kind != token.LIDENT {
		if isPatternAtomStart(p.toks[i].Kind) && hasEqual(i) {
			return stmtPatternBind
		}
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
	}
	if hasEqual(i + 1) {
		return stmtLocalFn
	}
	return stmtResult
}

// parseInlineBindBody parses one binding RHS inside a semicolon block. A
// top-level semicolon ends this binding item; nested bodies (a lambda body or
// an if arm, for example) still consume their own separators normally.
func (p *parser) parseInlineBindBody(eqTok token.Token) ast.Expr {
	t := p.peek()
	switch {
	case t.Kind == token.EOF:
		p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
			"I got to the end of the input while still expecting an expression.")
		return nil
	case t.Pos().Line == eqTok.Pos().Line:
		return p.parseExpr()
	case p.lay.checkOffside(t.Pos()) == offContinue:
		return p.parseBlock(t.Pos().Col)
	default:
		p.errorAt(p.prevSpan(), "SYNTAX PROBLEM",
			"This binding has no expression — the next line does not belong\nto it.")
		return nil
	}
}

// parseInlineBlock parses the explicit-separator spelling of a statement
// body. With no semicolon it collapses to the ordinary expression, just as a
// zero-statement layout block does. Every semicolon commits the expression or
// binding to being a non-final item, so a trailing separator cannot imply
// Unit: the block invariant still requires one explicit result expression.
func (p *parser) parseInlineBlock() ast.Expr {
	var binds []ast.LocalBind
	var items []ast.BlockItem
	var semicolons []source.Span
	hasExprStmt := false
	var pendingAnn *ast.TypeAnn
	var pendingAnnName token.Token

	for {
		t := p.peekInExpr()
		if t.Kind == token.EOF {
			title := "SYNTAX PROBLEM"
			if p.peek().Kind == token.EOF {
				title = TitleUnexpectedEOF
			}
			p.errorAt(p.prevSpan(), title,
				"A semicolon must be followed by another statement, and the final\nstatement must be this block's result expression.")
			return nil
		}

		isResult := false
		var result ast.Expr
		switch p.classifyInlineStmt() {
		case stmtLocalOp:
			op, _ := p.opNameAt(p.pos)
			p.errorAt(p.at(p.pos).Span.Merge(p.at(p.pos+2).Span), "OPERATOR DEFINITION",
				"An operator can only be defined at the top level, in a class, or in\nan instance, so its fixity has one home. `("+op+")` here needs a name\ninstead.")
			return nil

		case stmtBind, stmtLocalFn:
			nameT := p.next()
			params := p.parseValueParams(nameT.Span)
			eqT := p.peekInExpr()
			if !p.expect(token.EQ, "I expect `=` after the binding name.") {
				return nil
			}
			if pendingAnn != nil && pendingAnnName.Text != nameT.Text {
				p.errorAt(nameT.Span, "MISSING DEFINITION",
					"The type annotation for `"+pendingAnnName.Text+"` must sit directly\nbefore its binding, but this binds `"+nameT.Text+"`.")
				return nil
			}
			rhs := p.parseInlineBindBody(eqT)
			if rhs == nil {
				return nil
			}
			next := ast.LocalBind{Name: nameT.Text, NameSpan: nameT.Span, Params: params, Ann: pendingAnn, Body: rhs}
			grouped := false
			if len(params) > 0 && pendingAnn == nil && len(binds) > 0 && len(items) > 0 && items[len(items)-1].Expr == nil {
				prev := &binds[len(binds)-1]
				if prev.Name == next.Name && len(prev.Params) > 0 {
					if len(prev.Params) != len(next.Params) {
						p.errorAt(nameT.Span, "INCONSISTENT ARITY", "Adjacent equations for `"+nameT.Text+"` must have the same number of arguments.")
					} else {
						if len(prev.Equations) == 0 {
							prev.Equations = []ast.Equation{{Params: prev.Params, Body: prev.Body, NameSpan: prev.NameSpan}}
						}
						prev.Equations = append(prev.Equations, ast.Equation{Params: next.Params, Body: next.Body, NameSpan: next.NameSpan})
						grouped = true
					}
				}
			}
			if !grouped {
				binds = append(binds, next)
				items = append(items, ast.BlockItem{BindIndex: len(binds) - 1})
			}
			pendingAnn = nil

		case stmtPatternBind:
			if pendingAnn != nil {
				p.errorAt(t.Span, "DESTRUCTURING ANNOTATION", "A destructuring binding cannot have a direct type annotation; annotate a named subject first.")
				return nil
			}
			p.stmtStart = p.pos
			pat := p.parsePattern()
			eqT := p.peekInExpr()
			if !p.expect(token.EQ, "I expect `=` after the binding pattern.") {
				return nil
			}
			rhs := p.parseInlineBindBody(eqT)
			if rhs == nil {
				return nil
			}
			binds = append(binds, ast.LocalBind{Pattern: pat, Body: rhs})
			items = append(items, ast.BlockItem{BindIndex: len(binds) - 1})

		case stmtAnn:
			nameT := p.next()
			colon := p.next()
			if pendingAnn != nil {
				p.errorAt(nameT.Span, "MISSING DEFINITION",
					"The type annotation for `"+pendingAnnName.Text+"` must sit directly before its binding.")
				return nil
			}
			preds := p.parseContext()
			te := p.parseTypeExpr()
			if te == nil {
				return nil
			}
			pendingAnn = &ast.TypeAnn{Type: te, Preds: preds, Sp: colon.Span.Merge(te.Span())}
			pendingAnnName = nameT

		case stmtUse:
			if pendingAnn != nil {
				p.errorAt(t.Span, "MISSING DEFINITION",
					"The type annotation for `"+pendingAnnName.Text+"` must sit directly before its binding.")
				return nil
			}
			p.stmtStart = p.pos
			w, params, head := p.parseUseHead()
			if head == nil {
				return nil
			}
			if p.peek().Kind != token.SEMICOLON {
				title := "SYNTAX PROBLEM"
				if p.peek().Kind == token.EOF {
					title = TitleUnexpectedEOF
				}
				p.errorAt(w.Keyword, title, useNeedsRest)
				return nil
			}
			w.Semi = p.next().Span
			if p.lay.innermost().kind == ctxBlock {
				p.stmtStart = p.pos
			}
			rest := p.parseInlineBlock()
			if rest == nil {
				return nil
			}
			result := useCall(w, params, head, rest)
			if len(binds) == 0 && !hasExprStmt {
				return result
			}
			return &ast.Block{Binds: binds, Items: items, Result: result, Semicolons: semicolons}

		case stmtResult:
			if pendingAnn != nil {
				p.errorAt(t.Span, "MISSING DEFINITION",
					"The type annotation for `"+pendingAnnName.Text+"` must sit directly before its binding.")
				return nil
			}
			p.stmtStart = p.pos
			result = p.parseExpr()
			if result == nil {
				return nil
			}
			isResult = true
		}

		if p.peek().Kind != token.SEMICOLON {
			if !isResult {
				if len(binds) > 0 {
					return p.blockMissingResult(binds, items, p.peek().Span)
				}
				p.errorAt(p.prevSpan(), "SYNTAX PROBLEM",
					"This block ended without a result expression. Add `;` and the expression\nwhose value this block should return.")
				return nil
			}
			if len(binds) == 0 && !hasExprStmt {
				return result
			}
			return &ast.Block{Binds: binds, Items: items, Result: result, Semicolons: semicolons}
		}

		semi := p.next()
		semicolons = append(semicolons, semi.Span)
		if isResult {
			items = append(items, ast.BlockItem{BindIndex: -1, Expr: result})
			hasExprStmt = true
		}
		// A semicolon may join aligned statements inside a layout block. It
		// does not cross a declaration or case-branch boundary.
		if p.lay.innermost().kind == ctxBlock {
			p.stmtStart = p.pos
		}
	}
}

// closesBlock reports whether a token at a block's own column ends the block
// instead of starting another statement. An `if` branch body indented level
// with its own `then`/`else` puts that keyword at the body block's column, as
// a handled block can put its `on`, and none of them can begin a statement.
func closesBlock(k token.Kind) bool {
	return k == token.KwThen || k == token.KwElse || k == token.KwOn || k == token.COMMA || k == token.RPAREN || k == token.RBRACKET || k == token.RBRACE || k == token.RQUOTE
}

func (p *parser) blockMissingResult(binds []ast.LocalBind, items []ast.BlockItem, at source.Span) ast.Expr {
	return &ast.Block{Binds: binds, Items: items, Result: &ast.UnitLit{Sp: p.prevSpan()}, MissingResultAt: at}
}

const useNeedsRest = "A `use` applies its function to the rest of the block as a callback,\nbut nothing follows it here. Add the statements it should run around."

// parseUseHead parses `use head` or `use patterns <- head` and leaves the
// rest of the block to its caller. The binder patterns are tried as in a
// lambda and kept only when `<-` follows them.
func (p *parser) parseUseHead() (*ast.UseSugar, []ast.Pattern, ast.Expr) {
	kw := p.next()
	w := &ast.UseSugar{Keyword: kw.Span}
	q := *p
	q.lay.stack = append([]layoutCtx(nil), p.lay.stack...)
	q.errs = nil
	var params []ast.Pattern
	for isPatternAtomStart(q.peekInExpr().Kind) {
		pat := q.parsePatternAtom()
		if pat == nil {
			break
		}
		params = append(params, pat)
	}
	if t := q.peekInExpr(); len(params) > 0 && len(q.errs) == 0 && t.Kind == token.OP && t.Text == "<-" {
		p.pos = q.pos
		p.usesLists = q.usesLists
		p.usesTuples = q.usesTuples
		w.Arrow = p.next().Span
	} else {
		params = nil
	}
	if p.peekInExpr().Kind == token.EOF {
		title := "SYNTAX PROBLEM"
		if p.peek().Kind == token.EOF {
			title = TitleUnexpectedEOF
		}
		p.errorAt(p.prevSpan(), title, "I expect the function to apply after `use`.")
		return w, nil, nil
	}
	head := p.parseExpr()
	return w, params, head
}

// useCall expands a `use` item: the head applied to a callback whose body
// is the rest of the block.
func useCall(w *ast.UseSugar, params []ast.Pattern, head, rest ast.Expr) ast.Expr {
	sp := w.Keyword
	if w.Arrow.File != nil {
		sp = sp.Merge(w.Arrow)
	}
	if len(params) == 0 {
		params = []ast.Pattern{&ast.PUnit{Sp: w.Keyword}}
	}
	return &ast.App{Fn: head, Arg: &ast.Lambda{Params: params, Body: rest, Sp: sp, Use: w}}
}

// parseBlock parses a statement block at the given column: `name = expr`
// bindings (optionally annotated) followed by exactly one result expression.
// Zero-binding blocks collapse to the plain result expression.
func (p *parser) parseBlock(col int) ast.Expr {
	owner := p.lay.innermost().col
	if col <= owner && p.exprParenDepth > 0 {
		owner = 0 // the enclosing expression delimiter supplies the boundary
	}
	p.lay.push(ctxBlock, col)
	defer p.lay.pop()
	return p.blockItems(col, owner)
}

// blockItems parses the items of the layout block at col, which the caller
// has pushed. A `use` item parses the block's remaining items as a block of
// their own, in the same layout context.
func (p *parser) blockItems(col, owner int) ast.Expr {
	var binds []ast.LocalBind
	var items []ast.BlockItem
	hasExprStmt := false
	var pendingAnn *ast.TypeAnn
	var pendingAnnName token.Token

	for {
		t := p.peek()
		if t.Kind == token.EOF {
			if pendingAnn == nil && (len(binds) > 0 || hasExprStmt) {
				return p.blockMissingResult(binds, items, t.Span)
			}
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"This block has no result expression yet. A block is zero or more\n`name = …` bindings followed by one final expression.")
			return nil
		}
		if p.branchBoundary() || t.Pos().Col <= owner || closesBlock(t.Kind) || p.handlerStateAhead() {
			if pendingAnn != nil {
				p.errorAt(t.Span, "MISSING DEFINITION",
					"The type annotation for `"+pendingAnnName.Text+"` must sit directly\nabove its binding.")
				return nil
			}
			if len(binds) > 0 || hasExprStmt {
				return p.blockMissingResult(binds, items, t.Span)
			}
			p.errorAt(t.Span, "SYNTAX PROBLEM",
				"This block ended without a result expression. A block is zero or\nmore `name = …` bindings followed by one final expression.")
			return nil
		}
		if t.Pos().Col > col {
			p.errorAt(t.Span, "SYNTAX PROBLEM", "This line is indented past the first item of its block and reads as a continuation.")
			return nil
		}

		p.stmtStart = p.pos
		switch p.classifyStmt(t.Pos().Col) {
		case stmtLocalOp:
			op, _ := p.opNameAt(p.pos)
			p.errorAt(p.at(p.pos).Span.Merge(p.at(p.pos+2).Span), "OPERATOR DEFINITION",
				"An operator can only be defined at the top level, in a class, or in\nan instance, so its fixity has one home. `("+op+")` here needs a name\ninstead.")
			return nil
		case stmtBind, stmtLocalFn:
			nameT := p.next()
			params := p.parseValueParams(nameT.Span)
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
			next := ast.LocalBind{
				Name: nameT.Text, NameSpan: nameT.Span, Params: params, Ann: pendingAnn, Body: rhs,
			}
			if len(params) > 0 && pendingAnn == nil && len(binds) > 0 && len(items) > 0 && items[len(items)-1].Expr == nil {
				prev := &binds[len(binds)-1]
				if prev.Name == next.Name && len(prev.Params) > 0 {
					if len(prev.Params) != len(next.Params) {
						p.errorAt(nameT.Span, "INCONSISTENT ARITY", "Adjacent equations for `"+nameT.Text+"` must have the same number of arguments.")
					} else {
						if len(prev.Equations) == 0 {
							prev.Equations = []ast.Equation{{Params: prev.Params, Body: prev.Body, NameSpan: prev.NameSpan}}
						}
						prev.Equations = append(prev.Equations, ast.Equation{Params: next.Params, Body: next.Body, NameSpan: next.NameSpan})
						continue
					}
				}
			}
			binds = append(binds, next)
			items = append(items, ast.BlockItem{BindIndex: len(binds) - 1})
			pendingAnn = nil

		case stmtPatternBind:
			if pendingAnn != nil {
				p.errorAt(t.Span, "DESTRUCTURING ANNOTATION", "A destructuring binding cannot have a direct type annotation; annotate a named subject first.")
				return nil
			}
			p.stmtStart = p.pos
			pat := p.parsePattern()
			eqT := p.peekInExpr()
			if !p.expect(token.EQ, "I expect `=` after the binding pattern.") {
				return nil
			}
			rhs := p.parseBindBody(eqT)
			if rhs == nil {
				return nil
			}
			binds = append(binds, ast.LocalBind{Pattern: pat, Body: rhs})
			items = append(items, ast.BlockItem{BindIndex: len(binds) - 1})

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

		case stmtUse:
			if pendingAnn != nil {
				p.errorAt(t.Span, "MISSING DEFINITION",
					"The type annotation for `"+pendingAnnName.Text+"` must sit directly\nabove its binding.")
				return nil
			}
			w, params, head := p.parseUseHead()
			if head == nil {
				return nil
			}
			nt := p.peek()
			if nt.Kind == token.EOF {
				p.errorAt(w.Keyword, TitleUnexpectedEOF, useNeedsRest)
				return nil
			}
			if p.branchBoundary() || nt.Pos().Col <= owner || closesBlock(nt.Kind) || p.handlerStateAhead() {
				p.errorAt(w.Keyword, "SYNTAX PROBLEM", useNeedsRest)
				return nil
			}
			rest := p.blockItems(col, owner)
			if rest == nil {
				return nil
			}
			result := useCall(w, params, head, rest)
			if len(binds) == 0 && !hasExprStmt {
				return result
			}
			if !hasExprStmt {
				items = nil
			}
			return &ast.Block{Binds: binds, Items: items, Result: result}

		case stmtResult:
			if pendingAnn != nil {
				p.errorAt(t.Span, "MISSING DEFINITION",
					"The type annotation for `"+pendingAnnName.Text+"` must sit directly\nabove its binding.")
				return nil
			}
			p.stmtStart = p.pos // the opener may sit exactly at the block column
			result := p.parseExpr()
			if result == nil {
				return nil
			}
			if nt := p.peek(); nt.Kind != token.EOF && nt.Pos().Col > owner && nt.Pos().Col <= col && !p.branchBoundary() && !closesBlock(nt.Kind) && !p.handlerStateAhead() {
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

// A `{` heads a row literal, which is a type atom only in argument position:
// a record type is read straight after a `type` declaration's `=`, before any
// atom is, and an arrow's row is consumed by parseTypeExpr after the `->`.
func isTypeAtomStart(k token.Kind) bool {
	return k == token.UIDENT || k == token.LIDENT || k == token.LPAREN || k == token.LBRACE
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
	case token.LBRACE:
		row := p.parseEffRow()
		if row == nil {
			return nil
		}
		return &ast.TRow{Row: row}
	case token.LPAREN:
		p.next()
		if p.peek().Kind == token.RPAREN {
			rp := p.next()
			return &ast.TName{Name: "()", Sp: t.Span.Merge(rp.Span)}
		}
		inner := p.parseTypeExpr()
		if inner == nil {
			return nil
		}
		if p.peek().Kind == token.COMMA {
			return p.parseTupleTypeRest(t, inner)
		}
		if !p.expectRaw(token.RPAREN, "I was expecting a closing `)` in this type.") {
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

// isOp reports whether t is the operator lexeme text. Operators are one
// token kind carrying their spelling, so the parser matches text where it
// used to match a kind.
func isOp(t token.Token, text string) bool {
	return t.Kind == token.OP && t.Text == text
}

// parseExpr parses an operator expression as the flat run it is written as.
// Grouping belongs to internal/fixity: fixity is declared in source and a
// file is parsed before the module graph exists, so the parser cannot know a
// run's shape while reading it. A run with no operator collapses to its one
// operand, so only real operator expressions allocate a chain.
func (p *parser) parseExpr() ast.Expr {
	first := p.parseUnary()
	if first == nil || p.peekInExpr().Kind != token.OP {
		return first
	}
	chain := &ast.OpChain{Operands: []ast.Expr{first}}
	for p.peekInExpr().Kind == token.OP {
		op := p.next()
		operand := p.parseUnary()
		if operand == nil {
			return nil
		}
		chain.Ops = append(chain.Ops, ast.OpRef{Op: op.Text, Sp: op.Span})
		chain.Operands = append(chain.Operands, operand)
	}
	return chain
}

// parseUnary handles prefix minus. Any `-` reaching here is in prefix
// position — parseExpr's chain loop consumes an infix `-` after a complete
// operand — and binds tighter than every binary operator, looser than
// application. Because each chain operand comes from here, prefix minus
// never enters a chain as an operator and so has no fixity at all.
func (p *parser) parseUnary() ast.Expr {
	if t := p.peekInExpr(); isOp(t, "-") {
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
// then argument atoms. An attached empty `()` is
// folded into its atom first, so `f x()` is `f (x ())`, while `f x ()`
// remains `(f x) ()`. `if` may head an expression but is not an atom, so
// `print if …` needs parens (as in Elm).
func (p *parser) parseApply() ast.Expr {
	switch p.peekInExpr().Kind {
	case token.KwIf:
		return p.parseIf()
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
		case token.REGEX, token.INT, token.FLOAT, token.STRING, token.CHAR, token.LIDENT, token.UIDENT, token.LPAREN, token.LBRACE, token.LBRACKET, token.KwResume, token.LQUOTE, token.DOLLARPAREN, token.ATTYPE:
			arg := p.parsePostfixAtom()
			if arg == nil {
				return nil
			}
			fn = &ast.App{Fn: fn, Arg: arg}
			if app, ok := fn.(*ast.App); ok {
				if r, ok := app.Fn.(*ast.Resume); ok && p.peekInExpr().Kind == token.LIDENT && p.peekInExpr().Text == "with" {
					p.next()
					r.NextState = p.parseExpr()
					if r.NextState == nil {
						p.errorAt(p.prevSpan(), "SYNTAX PROBLEM", "I expect the next handler state after `with`.")
						return nil
					}
					return fn
				}
			}
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

// parseHandle parses `handle subject [with name = initial] on` and its
// clauses. The subject is an inline body on the `handle` line or an indented
// statement block below it, as after `=`. The `handle` token's column anchors
// the head: `with` and `on` may sit exactly there after a block subject, the
// way `then` and `else` may sit at their `if`.
func (p *parser) parseHandle() ast.Expr {
	h := p.next()
	anchor := h.Pos().Col
	p.stopWith++
	body := p.parseBodyAfter(h, "This `handle` has no expression — the next line does not belong\nto it.")
	p.stopWith--
	if body == nil {
		return nil
	}
	var state *ast.HandlerState
	if p.ownsHandlerState(anchor) {
		p.stmtStart = p.pos
		p.next()
		name := p.peekInExpr()
		if name.Kind != token.LIDENT {
			p.errorAt(name.Span, "SYNTAX PROBLEM", "I expect a lowercase state snapshot name after `with`.")
			return nil
		}
		p.next()
		if !p.expect(token.EQ, "I expect `=` after the handler state name.") {
			return nil
		}
		initial := p.parseExpr()
		if initial == nil {
			return nil
		}
		state = &ast.HandlerState{Name: name.Text, NameSpan: name.Span, Initial: initial}
	}
	p.allowAtAnchor(anchor, token.KwOn)
	if !p.expect(token.KwOn, "I expect `on` after the expression being handled.") {
		return nil
	}
	first := p.peek()
	if first.Kind == token.EOF {
		p.errorAt(p.prevSpan(), TitleUnexpectedEOF, "I expect at least one handler clause after `on`.")
		return nil
	}
	owner := h.Pos().Col
	if parent := p.lay.innermost().col; parent < owner {
		owner = parent
	}
	if first.Pos().Col <= owner {
		p.errorAt(first.Span, "SYNTAX PROBLEM", "Handler clauses must be indented past their enclosing layout column.")
		return nil
	}
	p.lay.pushBranch(ctxHandle, first.Pos().Col, owner)
	defer p.lay.pop()
	result := &ast.Handle{Body: body, State: state, Sp: h.Span}
	lastClause := ""
	// An operation signature line, `op : Type`, heads the clause group that
	// follows it. It is held here until that group's first clause arrives.
	var pendingSig *ast.TypeAnn
	var pendingSigOp string
	var pendingSigSpan source.Span
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
		if pendingSig != nil && opName != pendingSigOp {
			p.errorAt(pendingSigSpan, "SYNTAX PROBLEM", "The signature for `"+pendingSigOp+"` must be followed by a clause for `"+pendingSigOp+"`, not `"+opName+"`.")
			return nil
		}
		if p.peekInExpr().Kind == token.COLON {
			colon := p.next()
			if opName == "return" {
				p.errorAt(opSpan, "SYNTAX PROBLEM", "A `return` clause has no signature: it matches the handled result.")
				return nil
			}
			if pendingSig != nil {
				p.errorAt(pendingSigSpan, "SYNTAX PROBLEM", "The signature for `"+opName+"` must be followed by a clause for `"+opName+"`, not another signature.")
				return nil
			}
			preds := p.parseContext()
			te := p.parseTypeExpr()
			if te == nil {
				return nil
			}
			if len(preds) > 0 {
				p.errorAt(preds[0].Sp, "SYNTAX PROBLEM", "An operation signature has no class context: operation-local constraints are not supported.")
				return nil
			}
			pendingSig = &ast.TypeAnn{Type: te, Sp: colon.Span.Merge(te.Span())}
			pendingSigOp, pendingSigSpan = opName, opSpan.Merge(te.Span())
			lastClause = ""
			if p.peek().Kind == token.EOF {
				p.errorAt(p.prevSpan(), TitleUnexpectedEOF, "I expect a clause for `"+opName+"` after its signature.")
				return nil
			}
			if !p.branchBoundaryAt(len(p.lay.stack) - 1) {
				p.errorAt(pendingSigSpan, "SYNTAX PROBLEM", "The signature for `"+opName+"` must be followed by a clause for `"+opName+"`.")
				return nil
			}
			continue
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
			if result.Return != nil && lastClause != "return" {
				p.errorAt(op.Span, "SYNTAX PROBLEM", "A handler can have only one `return` clause.")
				return nil
			}
			if result.Return == nil {
				result.Return = &ast.ReturnClause{Param: params[0], Body: clauseBody, Sp: op.Span}
			} else {
				if len(result.Return.Equations) == 0 {
					result.Return.Equations = []ast.Equation{{Params: []ast.Pattern{result.Return.Param}, Body: result.Return.Body, NameSpan: result.Return.Sp}}
				}
				result.Return.Equations = append(result.Return.Equations, ast.Equation{Params: params, Body: clauseBody, NameSpan: op.Span})
			}
		} else {
			if lastClause == opName && len(result.Clauses) > 0 && result.Clauses[len(result.Clauses)-1].Op == opName {
				prev := &result.Clauses[len(result.Clauses)-1]
				if len(prev.Params) != len(params) {
					p.errorAt(opSpan, "INCONSISTENT ARITY", "Adjacent clauses for `"+opName+"` must have the same number of arguments.")
				} else {
					if len(prev.Equations) == 0 {
						prev.Equations = []ast.Equation{{Params: prev.Params, Body: prev.Body, NameSpan: prev.OpSpan}}
					}
					prev.Equations = append(prev.Equations, ast.Equation{Params: params, Body: clauseBody, NameSpan: opSpan})
				}
			} else {
				cl := ast.HandleClause{Op: opName, OpSpan: opSpan, Params: params, Body: clauseBody}
				if pendingSig != nil {
					cl.Signature, cl.SigSpan = pendingSig, pendingSigSpan
					pendingSig = nil
				}
				result.Clauses = append(result.Clauses, cl)
			}
		}
		lastClause = opName
		nt := p.peek()
		if nt.Kind == token.EOF || !p.branchBoundaryAt(len(p.lay.stack)-1) {
			break
		}
	}
	if len(result.Clauses) == 0 {
		p.errorAt(h.Span, "SYNTAX PROBLEM", "A handler needs at least one operation clause.")
		return nil
	}
	return result
}

func (p *parser) parseClauseParams() []ast.Pattern {
	var params []ast.Pattern
	for isPatternAtomStart(p.peekInExpr().Kind) {
		params = append(params, p.parsePatternAtom())
	}
	return params
}

// parseCase parses `case scrutinee of` and its branches. A branch head and
// arrow on a later line start a sibling anywhere inside the enclosing layout.
// Branch bodies are statement blocks or inline expressions.
func (p *parser) parseCase() ast.Expr {
	caseTok := p.next()
	scrut := p.parseExpr()
	if scrut == nil {
		return nil
	}
	if !p.expect(token.KwOf, "I expect `of` after the expression in a `case`.") {
		return nil
	}
	first := p.peek()
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
	owner := caseTok.Pos().Col
	if parent := p.lay.innermost().col; parent < owner {
		owner = parent
	}
	if first.Pos().Col <= owner {
		p.errorAt(first.Span, "SYNTAX PROBLEM", "Case branches must be indented past their enclosing layout column.")
		return nil
	}
	p.lay.pushBranch(ctxCase, first.Pos().Col, owner)
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
		if nt := p.peek(); nt.Kind == token.EOF || !p.branchBoundaryAt(len(p.lay.stack)-1) {
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
		token.INT, token.FLOAT, token.STRING, token.CHAR, token.LPAREN, token.LBRACKET,
		token.LBRACE:
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
	case token.LBRACE:
		// A pattern has no update form, so `{` here is always the inferred
		// record view; the nominal type comes from what it is matched against.
		fields, end, ok := p.parseRecordPatternFields()
		if !ok {
			return nil
		}
		return &ast.PRecord{NameSpan: t.Span, Fields: fields, Sp: t.Span.Merge(end)}
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
	case token.OP:
		// `-` before a literal is the only operator a pattern can hold.
		if t.Text != "-" {
			p.errorAt(t.Span, "SYNTAX PROBLEM",
				"I was expecting a pattern here, like `Just x`, a literal, or `_`.")
			return nil
		}
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
		lp := p.next()
		if p.peek().Kind == token.RPAREN {
			rp := p.next()
			return &ast.PUnit{Sp: lp.Span.Merge(rp.Span)}
		}
		pat := p.parsePattern()
		if pat == nil {
			return nil
		}
		if p.peek().Kind == token.COMMA {
			return p.parseTuplePatternRest(lp, pat)
		}
		if !p.expectRaw(token.RPAREN, "I was expecting a closing `)` in this pattern.") {
			return nil
		}
		return pat
	case token.LBRACKET:
		return p.parseListPattern()
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

// parseListPattern lowers bracket syntax directly to the standard List
// constructors. Keeping the surface sugar out of later phases lets ordinary
// constructor inference, exhaustiveness, and decision-tree compilation apply.
func (p *parser) parseListPattern() ast.Pattern {
	lb := p.next()
	p.usesLists = true
	if p.peek().Kind == token.RBRACKET {
		rb := p.next()
		sp := lb.Span.Merge(rb.Span)
		return &ast.PCtor{Name: "List.Nil", NameSpan: sp, Sugared: true}
	}

	var elems []ast.Pattern
	var tail ast.Pattern
	for {
		if p.peek().Kind == token.EOF {
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"I got to the end of the input while looking for the `]` that closes this list pattern.")
			return nil
		}
		elem := p.parsePattern()
		if elem == nil {
			return nil
		}
		elems = append(elems, elem)
		switch p.peek().Kind {
		case token.COMMA:
			comma := p.next()
			if next := p.peek(); next.Kind == token.RBRACKET || next.Kind == token.PIPE {
				p.errorAt(comma.Span, "SYNTAX PROBLEM", "A comma in a list pattern must be followed by another pattern.")
				return nil
			}
		case token.PIPE:
			pipe := p.next()
			if p.peek().Kind == token.EOF {
				p.errorAt(pipe.Span, TitleUnexpectedEOF,
					"I got to the end of the input while expecting a tail pattern after `|`.")
				return nil
			}
			if p.peek().Kind == token.RBRACKET {
				p.errorAt(pipe.Span, "SYNTAX PROBLEM", "The `|` in a list pattern must be followed by a tail pattern.")
				return nil
			}
			tail = p.parsePattern()
			if tail == nil {
				return nil
			}
			if p.peek().Kind != token.RBRACKET {
				p.errorAt(p.peek().Span, "SYNTAX PROBLEM", "I expect `]` after the tail of this list pattern.")
				return nil
			}
			goto done
		case token.RBRACKET:
			goto done
		case token.EOF:
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"I got to the end of the input while looking for the `]` that closes this list pattern.")
			return nil
		default:
			p.errorAt(p.peek().Span, "SYNTAX PROBLEM", "I expect `,`, `|`, or `]` after this list pattern element.")
			return nil
		}
	}

done:
	rb := p.next()
	sp := lb.Span.Merge(rb.Span)
	if tail == nil {
		tail = &ast.PCtor{Name: "List.Nil", NameSpan: sp, Sugared: true}
	}
	for i := len(elems) - 1; i >= 0; i-- {
		tail = &ast.PCtor{Name: "List.Cons", NameSpan: sp, Args: []ast.Pattern{elems[i], tail}, Sugared: true}
	}
	return tail
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
	cond := p.parseExpr()
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
// chain's anchor column, or an `on` at its `handle`'s, which peekInExpr would
// otherwise read as a sibling boundary. None of these keywords can start a
// statement or a branch, so the exemption cannot swallow a following construct.
func (p *parser) allowAtAnchor(anchor int, k token.Kind) {
	if t := p.peek(); t.Kind == k && t.Pos().Col == anchor {
		p.stmtStart = p.pos
	}
}

func (p *parser) parseAtom() ast.Expr {
	t := p.peekInExpr()
	switch t.Kind {
	case token.ATTYPE:
		p.next()
		var ty ast.TypeExpr
		if p.peekInExpr().Kind == token.LPAREN {
			p.next()
			ty = p.parseTypeExpr()
			if ty == nil || !p.expect(token.RPAREN, "I expect `)` after the type witness.") {
				return nil
			}
		} else {
			ty = p.parseTypeAtom()
			if ty == nil {
				return nil
			}
		}
		return &ast.Ctor{Name: "Basics.Type", Sugared: true, Witness: ty, Sp: t.Span.Merge(ty.Span())}
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
	case token.REGEX:
		p.next()
		p.usesRegex = true
		return &ast.RegexLit{Value: lexer.RegexPattern(t.Text), Raw: t.Text, Sp: t.Span}
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
		if final == token.UIDENT && p.peekInExpr().Kind == token.LBRACE &&
			(p.at(p.pos+1).Kind == token.RBRACE || p.braceIsRecordLiteral()) {
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
		if p.braceIsRecordLiteral() {
			lb := p.next()
			fields, end, ok := p.parseRecordExprFieldsAfterOpen()
			if !ok {
				return nil
			}
			return &ast.RecordLit{NameSpan: lb.Span, Fields: fields, Sp: lb.Span.Merge(end)}
		}
		if p.at(p.pos+1).Kind == token.RBRACE {
			// Both record literals and lambdas need contents. Keep the
			// established empty-record diagnostic for this spelling.
			p.errorAt(t.Span.Merge(p.at(p.pos+1).Span), "SYNTAX PROBLEM", "A record literal needs at least one field.")
			return nil
		}
		if p.braceHasUpdatePipe() {
			lb := p.next()
			record := p.parseExpr()
			if record == nil || !p.expect(token.PIPE, "I expect `|` after the record being updated.") {
				return nil
			}
			fields, end, ok := p.parseRecordExprFieldsAfterOpen()
			if !ok {
				return nil
			}
			return &ast.RecordUpdate{Record: record, Fields: fields, Sp: lb.Span.Merge(end)}
		}
		return p.parseBracedLambda()
	case token.LBRACKET:
		return p.parseListExpr()
	case token.KwResume:
		p.next()
		return &ast.Resume{Sp: t.Span}
	case token.LQUOTE:
		// A quotation encloses one ordinary expression, with the same fresh
		// layout boundary as grouping parentheses. Its close is not an atom.
		p.next()
		p.usesStaging = true
		p.exprParenDepth++
		defer func() { p.exprParenDepth-- }()
		p.lay.push(ctxParen, 0)
		defer p.lay.pop()
		if p.peek().Kind == token.RQUOTE {
			p.errorAt(t.Span.Merge(p.next().Span), "SYNTAX PROBLEM", "A quotation needs an expression between its backticks.")
			return nil
		}
		body := p.parseExpr()
		if body == nil {
			return nil
		}
		if p.peek().Kind != token.RQUOTE {
			if p.peek().Kind == token.EOF {
				p.errorAt(t.Span, TitleUnexpectedEOF, "I got to the end of the input while looking for the backtick that closes this quotation.")
			} else {
				p.errorAt(p.peek().Span, "SYNTAX PROBLEM", "I expect the backtick that closes this quotation. A quotation contains one expression, not a binding block.")
			}
			return nil
		}
		return &ast.Quote{Body: body, Sp: t.Span.Merge(p.next().Span)}
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
		operand := p.parseExpr()
		if operand == nil {
			return nil
		}
		if inner := p.peek(); inner.Kind == token.RPAREN {
			p.next()
		} else if inner.Kind == token.EOF {
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
		if p.peek().Kind == token.RPAREN {
			rp := p.next()
			return &ast.UnitLit{Sp: lp.Span.Merge(rp.Span)}
		}
		// `(+)` names the operator, so `List.foldl (+) 0 xs` works. Checked
		// before the parenthesized-expression path, or `(-)` would start
		// down parseUnary's prefix-minus branch and find no operand.
		if _, ok := p.opNameAt(p.pos - 1); ok {
			op := p.peekInExpr()
			if !p.bindableOp(op) {
				return nil
			}
			p.next()
			rp := p.next()
			return &ast.Var{Name: op.Text, Sp: lp.Span.Merge(rp.Span)}
		}
		p.exprParenDepth++
		defer func() { p.exprParenDepth-- }()
		p.lay.push(ctxParen, 0)
		defer p.lay.pop()
		e := p.parseExpr()
		if e == nil {
			return nil
		}
		if p.peek().Kind == token.COMMA {
			return p.parseTupleExprRest(lp, e)
		}
		if inner := p.peek(); inner.Kind == token.RPAREN {
			p.next()
		} else if inner.Kind == token.EOF {
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

// atInferredRecord reports whether the just-consumed `{` opens an inferred
// record literal rather than a record update. `{ field = ...` can only be a
// literal: an update names its subject first and that subject is always
// followed by `|`, `.`, or another atom, never by `=`. EQ is exactly `=`, so
// `==` and `=>` carry their own token kinds and cannot be mistaken for one.
func (p *parser) atInferredRecord() bool {
	// peekInExpr, not the raw token: a label sitting at or left of the layout
	// column belongs to the next declaration, and reading it here would let the
	// lookahead cross a boundary the offside rule has already closed.
	return p.peekInExpr().Kind == token.LIDENT && p.at(p.pos+1).Kind == token.EQ
}

// braceIsRecordLiteral recognizes a complete field list. A leading `x =`
// alone is insufficient: `{ x = 1; x }` is a lambda with a local binding.
func (p *parser) braceIsRecordLiteral() bool {
	if p.peekInExpr().Kind != token.LBRACE {
		return false
	}
	// A sibling statement in the braces belongs to a lambda body. Without
	// this check, the speculative record parse can consume its first token as
	// another argument to the preceding field value.
	first := p.at(p.pos + 1)
	depth := 0
	previousLine := first.Pos().Line
	afterComma := false
	for i := p.pos + 1; i < len(p.toks); i++ {
		t := p.toks[i]
		if depth == 0 && t.Kind == token.RBRACE {
			break
		}
		if depth == 0 && t.Pos().Line > previousLine &&
			t.Pos().Col <= first.Pos().Col && t.Kind != token.COMMA && !afterComma {
			return false
		}
		if depth == 0 {
			if t.Kind == token.COMMA {
				afterComma = true
			} else {
				afterComma = false
			}
		}
		if t.Kind == token.LPAREN || t.Kind == token.LBRACKET || t.Kind == token.LBRACE || t.Kind == token.DOLLARPAREN || t.Kind == token.ATTRIBUTE || t.Kind == token.LQUOTE {
			depth++
		} else if t.Kind == token.RPAREN || t.Kind == token.RBRACKET || t.Kind == token.RBRACE || t.Kind == token.RQUOTE {
			depth--
		}
		previousLine = t.Pos().Line
	}
	q := *p
	q.lay.stack = append([]layoutCtx(nil), p.lay.stack...)
	q.errs = nil
	q.next()
	if !q.atInferredRecord() {
		return false
	}
	_, _, ok := q.parseRecordExprFieldsAfterOpen()
	return ok && len(q.errs) == 0
}

// An outer pipe introduces a record update. Pipes inside nested parentheses,
// lists, or braces belong to those expressions instead.
func (p *parser) braceHasUpdatePipe() bool {
	depth := 0
	for i := p.pos + 1; i < len(p.toks); i++ {
		switch p.toks[i].Kind {
		case token.LPAREN, token.LBRACKET, token.LBRACE, token.DOLLARPAREN, token.ATTRIBUTE, token.LQUOTE:
			depth++
		case token.RPAREN, token.RBRACKET, token.RBRACE, token.RQUOTE:
			if depth == 0 {
				return false
			}
			depth--
		case token.PIPE:
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

// parseBracedLambda parses `{ expression }` as a Unit callback and
// `{ patterns -> body }` as a callback with explicit parameters.
func (p *parser) parseBracedLambda() ast.Expr {
	lb := p.next()
	p.exprParenDepth++
	defer func() { p.exprParenDepth-- }()
	p.lay.push(ctxParen, 0)
	defer p.lay.pop()

	q := *p
	q.lay.stack = append([]layoutCtx(nil), p.lay.stack...)
	q.errs = nil
	var params []ast.Pattern
	for isPatternAtomStart(q.peekInExpr().Kind) {
		pat := q.parsePatternAtom()
		if pat == nil {
			break
		}
		params = append(params, pat)
	}
	var body ast.Expr
	if len(params) > 0 && len(q.errs) == 0 && q.peekInExpr().Kind == token.ARROW {
		p.pos = q.pos
		p.usesLists = q.usesLists
		p.usesTuples = q.usesTuples
		arrow := p.next()
		body = p.parseBindBody(arrow)
	} else {
		params = []ast.Pattern{&ast.PUnit{Sp: lb.Span}}
		if p.peek().Pos().Line == lb.Pos().Line {
			body = p.parseInlineBlock()
		} else {
			body = p.parseBlock(p.peek().Pos().Col)
		}
	}
	if body == nil {
		return nil
	}
	if !p.expect(token.RBRACE, "I expect `}` to close this lambda.") {
		return nil
	}
	return &ast.Lambda{Params: params, Body: body, Sp: lb.Span.Merge(p.prevSpan())}
}

func (p *parser) parseRecordExprFields() ([]ast.RecordExprField, source.Span, bool) {
	p.next() // {
	return p.parseRecordExprFieldsAfterOpen()
}

// parseListExpr lowers list syntax to right-nested List.Cons applications.
// Constructor evaluation is left-to-right, so this also gives list elements
// and an explicit tail the source evaluation order without a special runtime.
func (p *parser) parseListExpr() ast.Expr {
	lb := p.next()
	p.usesLists = true
	if p.peek().Kind == token.RBRACKET {
		rb := p.next()
		return &ast.Ctor{Name: "List.Nil", Sp: lb.Span.Merge(rb.Span), Sugared: true}
	}

	var elems []ast.Expr
	var tail ast.Expr
	for {
		if p.peek().Kind == token.EOF {
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"I got to the end of the input while looking for the `]` that closes this list.")
			return nil
		}
		elem := p.parseExpr()
		if elem == nil {
			return nil
		}
		elems = append(elems, elem)
		switch p.peek().Kind {
		case token.COMMA:
			comma := p.next()
			if next := p.peek(); next.Kind == token.RBRACKET || next.Kind == token.PIPE {
				p.errorAt(comma.Span, "SYNTAX PROBLEM", "A comma in a list must be followed by another expression.")
				return nil
			}
		case token.PIPE:
			pipe := p.next()
			if p.peek().Kind == token.EOF {
				p.errorAt(pipe.Span, TitleUnexpectedEOF,
					"I got to the end of the input while expecting a tail expression after `|`.")
				return nil
			}
			if p.peek().Kind == token.RBRACKET {
				p.errorAt(pipe.Span, "SYNTAX PROBLEM", "The `|` in a list must be followed by a tail expression.")
				return nil
			}
			tail = p.parseExpr()
			if tail == nil {
				return nil
			}
			if p.peek().Kind != token.RBRACKET {
				p.errorAt(p.peek().Span, "SYNTAX PROBLEM", "I expect `]` after the tail of this list.")
				return nil
			}
			goto done
		case token.RBRACKET:
			goto done
		case token.EOF:
			p.errorAt(p.prevSpan(), TitleUnexpectedEOF,
				"I got to the end of the input while looking for the `]` that closes this list.")
			return nil
		default:
			p.errorAt(p.peek().Span, "SYNTAX PROBLEM", "I expect `,`, `|`, or `]` after this list element.")
			return nil
		}
	}

done:
	rb := p.next()
	sp := lb.Span.Merge(rb.Span)
	if tail == nil {
		tail = &ast.Ctor{Name: "List.Nil", Sp: sp, Sugared: true}
	}
	for i := len(elems) - 1; i >= 0; i-- {
		cons := &ast.Ctor{Name: "List.Cons", Sp: sp, Sugared: true}
		tail = &ast.App{Fn: &ast.App{Fn: cons, Arg: elems[i]}, Arg: tail}
	}
	return tail
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
		value := p.parseExpr()
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

// handlerStateStart recognizes contextual state syntax without reserving its words.
func (p *parser) handlerStateStart(pos int) bool {
	return pos+2 < len(p.toks) && p.toks[pos].Kind == token.LIDENT && p.toks[pos].Text == "with" && p.toks[pos+1].Kind == token.LIDENT && p.toks[pos+2].Kind == token.EQ
}

// ownsHandlerState reports whether a `with name =` after a handled subject
// belongs to this handler: it continues the subject's last line, sits at the
// `handle` keyword's column, or is indented past the enclosing layout column.
// An outer handler's state, further left, ends this one's subject instead.
func (p *parser) ownsHandlerState(anchor int) bool {
	if !p.handlerStateStart(p.pos) {
		return false
	}
	t := p.peek()
	return t.Pos().Line == p.prevSpan().EndPos().Line || t.Pos().Col == anchor || p.lay.checkOffside(t.Pos()) == offContinue
}

// handlerStateAhead reports whether a handled block's `with name =` starts
// here. Like closesBlock's keywords, it ends the subject block rather than
// binding a local named `with`.
func (p *parser) handlerStateAhead() bool {
	return p.stopWith > 0 && p.handlerStateStart(p.pos)
}

// peek returns the current token, ignoring layout.
func (p *parser) peek() token.Token { return p.toks[p.pos] }

// peekInExpr returns the current token, or a synthetic EOF when the current
// layout context or a later branch head ends the expression.
func (p *parser) peekInExpr() token.Token {
	t := p.toks[p.pos]
	if t.Kind == token.EOF {
		return t
	}
	if p.stopWith > 0 && p.handlerStateStart(p.pos) {
		return token.Token{Kind: token.EOF, Span: t.Span}
	}
	// A ragged sibling may begin left of the first item. Its head still owns
	// all tokens on that physical line, including `=`, `:`, and `->` at or
	// left of the block's original alignment column.
	if p.stmtStart >= 0 && p.pos > p.stmtStart && p.toks[p.stmtStart].Pos().Line == t.Pos().Line {
		return t
	}
	if p.pos != p.stmtStart && (p.branchBoundary() || p.lay.checkOffside(t.Pos()) != offContinue) {
		return token.Token{Kind: token.EOF, Span: t.Span}
	}
	return t
}

// branchBoundary recognizes a later case/handler branch by its head and arrow,
// independently of its exact column. The innermost eligible branch group owns
// the line; an outer group cannot steal a nested group's branch.
func (p *parser) branchBoundary() bool {
	if p.pos >= len(p.toks) {
		return false
	}
	for i := len(p.lay.stack) - 1; i >= 0; i-- {
		ctx := p.lay.stack[i]
		if ctx.kind == ctxParen {
			return false // a parenthesis closes before the outer branch group resumes
		}
		if (ctx.kind == ctxCase || ctx.kind == ctxHandle) && p.peek().Pos().Col > ctx.owner {
			return p.branchBoundaryAt(i)
		}
	}
	return false
}

func (p *parser) branchBoundaryAt(i int) bool {
	if p.pos == 0 || p.pos == p.stmtStart || p.pos >= len(p.toks) {
		return false
	}
	t := p.peek()
	if t.Kind == token.EOF || t.Kind == token.RQUOTE || p.toks[p.pos-1].Pos().Line == t.Pos().Line {
		return false
	}
	ctx := p.lay.stack[i]
	if (ctx.kind != ctxCase && ctx.kind != ctxHandle) || t.Pos().Col <= ctx.owner {
		return false
	}
	if t.Pos().Col == ctx.col {
		return true // preserve multiline heads at the established column
	}
	q := *p
	q.lay = layout{stack: []layoutCtx{{kind: ctxDecl, col: 0}}}
	q.stmtStart = q.pos
	q.errs = nil
	if ctx.kind == ctxCase {
		if q.parsePattern() == nil {
			return false
		}
	} else {
		if t.Kind != token.LIDENT && t.Kind != token.UIDENT {
			return false
		}
		if t.Kind == token.UIDENT {
			_, final, _ := q.parseQualifiedName()
			if final != token.LIDENT {
				return false
			}
		} else {
			q.next()
		}
		// An operation signature, `op : Type`, heads a clause group.
		if c := q.peek(); c.Kind == token.COLON && c.Pos().Line == t.Pos().Line {
			return len(q.errs) == 0
		}
		q.parseClauseParams()
	}
	return len(q.errs) == 0 && q.peek().Kind == token.ARROW && q.peek().Pos().Line == t.Pos().Line
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

// expectRaw accepts punctuation belonging to an already-open delimited form
// even at the surrounding layout column. A comma or a closing parenthesis
// cannot start a declaration, statement, or branch, so this does not weaken
// ordinary offside boundaries.
func (p *parser) expectRaw(k token.Kind, msg string) bool {
	if t := p.peek(); t.Kind == k {
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
// spanOfTokens covers tokens [from, to) — the extent a declaration consumed.
// It is clamped so a declaration that consumed nothing still reports a
// well-formed zero-width span at its first token.
func (p *parser) spanOfTokens(from, to int) source.Span {
	if from >= len(p.toks) {
		from = len(p.toks) - 1
	}
	start := p.toks[from].Span.Start
	end := start
	if to > from {
		end = p.toks[to-1].Span.End
	}
	return source.Span{File: p.f, Start: start, End: end}
}

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

// Tuple syntax lowers to the bundled Tuple types the same way bracket syntax
// lowers to List: the parser writes the qualified constructor directly and
// marks it Sugared, so it resolves without an import.
//
// Arity is capped deliberately rather than by any compiler limit. Each arity
// is one more bundled nominal type, and past three a nominal record with named
// fields reads better than positional ones.
func tupleCtorName(n int) string {
	switch n {
	case 2:
		return "Tuple.Pair"
	case 3:
		return "Tuple.Triple"
	default:
		return ""
	}
}

func (p *parser) tupleArityError(sp source.Span, n int) {
	p.errorAt(sp, "TUPLE TOO BIG",
		"A tuple holds two or three elements; this one holds "+strconv.Itoa(n)+". Declare a record type instead, so the fields have names.")
}

// parseTupleTypeRest continues a tuple type after its first element and the
// comma that proves it is one.
func (p *parser) parseTupleTypeRest(open token.Token, first ast.TypeExpr) ast.TypeExpr {
	args := []ast.TypeExpr{first}
	for p.peek().Kind == token.COMMA {
		p.next()
		next := p.parseTypeExpr()
		if next == nil {
			return nil
		}
		args = append(args, next)
	}
	if !p.expectRaw(token.RPAREN, "I was expecting a closing `)` in this tuple type.") {
		return nil
	}
	sp := open.Span.Merge(p.prevSpan())
	name := tupleCtorName(len(args))
	if name == "" {
		p.tupleArityError(sp, len(args))
		return nil
	}
	p.usesTuples = true
	return &ast.TApp{Name: name, NameSp: sp, Args: args, Sugared: true}
}

// parseTupleExprRest continues a tuple expression after its first element.
// Elements evaluate left to right, which is ordinary constructor application.
func (p *parser) parseTupleExprRest(open token.Token, first ast.Expr) ast.Expr {
	args := []ast.Expr{first}
	for p.peek().Kind == token.COMMA {
		p.next()
		next := p.parseExpr()
		if next == nil {
			return nil
		}
		args = append(args, next)
	}
	if !p.expectRaw(token.RPAREN, "I was expecting a closing `)` in this tuple.") {
		return nil
	}
	sp := open.Span.Merge(p.prevSpan())
	name := tupleCtorName(len(args))
	if name == "" {
		p.tupleArityError(sp, len(args))
		return nil
	}
	p.usesTuples = true
	var out ast.Expr = &ast.Ctor{Name: name, Sp: sp, Sugared: true}
	for _, a := range args {
		out = &ast.App{Fn: out, Arg: a}
	}
	return out
}

// parseTuplePatternRest continues a tuple pattern after its first element.
func (p *parser) parseTuplePatternRest(open token.Token, first ast.Pattern) ast.Pattern {
	args := []ast.Pattern{first}
	for p.peek().Kind == token.COMMA {
		p.next()
		next := p.parsePattern()
		if next == nil {
			return nil
		}
		args = append(args, next)
	}
	if !p.expectRaw(token.RPAREN, "I was expecting a closing `)` in this tuple pattern.") {
		return nil
	}
	sp := open.Span.Merge(p.prevSpan())
	name := tupleCtorName(len(args))
	if name == "" {
		p.tupleArityError(sp, len(args))
		return nil
	}
	p.usesTuples = true
	return &ast.PCtor{Name: name, NameSpan: sp, Args: args, Sugared: true}
}
