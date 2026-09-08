// Package meta holds the compile-time stage's two shared representations:
// the quote templates the compiler keeps on the side, and the opaque `Code`
// value the compile-time evaluator produces. Both are deliberately below
// inference, elaboration, and evaluation in the package graph so all three
// can name them.
//
// There is no fango-level mirror of internal/ast. A template is the quoting
// module's own resolved AST, retaining its original spans, so a type error in
// generated code points at the line the quote was written on.
package meta

import "github.com/waj/fango/internal/ast"
import "github.com/waj/fango/internal/source"
import "github.com/waj/fango/internal/types"

// Schema reads the declaration table a reflected type belongs to. The checker
// implements it, which keeps this package below inference while still letting
// a TypeRepr answer questions about its own constructors.
type Schema interface {
	ADT(unique int) *types.ADTInfo
}

// TypeRepr is the compiler-owned value produced by typeOf. Identity comes
// from TCon.Unique and structural children; Name is only presentation data.
// Visible is captured at the reflection site: it holds the uniques whose
// schemas that site could have read by hand, so an abstract type stays
// abstract at compile time exactly as it does at run time. A nil Visible
// means every schema is readable — the REPL and headerless files, which have
// no export boundary to respect.
type TypeRepr struct {
	Type    types.Type
	Visible map[int]bool
	Schema  Schema
}

// Con returns the reflected nominal type, or nil for a variable or an arrow.
func (r *TypeRepr) Con() *types.TCon {
	con, _ := r.Type.(*types.TCon)
	return con
}

// ADT returns the reflected type's declaration, or nil when the type is not
// nominal, has no declaration, hides its schema from the reflection site, or
// is not fully applied. A partial application has no readable schema because
// its constructor fields cannot be instantiated: `Meta.head` produces one,
// and it is for comparing identities rather than for reading a shape.
func (r *TypeRepr) ADT() *types.ADTInfo {
	con := r.Con()
	if con == nil || r.Schema == nil {
		return nil
	}
	if r.Visible != nil && !r.Visible[con.Unique] {
		return nil
	}
	adt := r.Schema.ADT(con.Unique)
	if adt == nil || len(con.Args) != len(adt.Params) {
		return nil
	}
	return adt
}

// Derive reflects t with this site's visibility, so walking into a type
// argument or a constructor field never widens what the deriver may read.
func (r *TypeRepr) Derive(t types.Type) *TypeRepr {
	return &TypeRepr{Type: t, Visible: r.Visible, Schema: r.Schema}
}

// Template is one `quote` occurrence: the resolved expression it describes
// plus its holes in source order. Holes are the `$(…)` nodes inside Body;
// splicing replaces each with the code its hole evaluated to.
type Template struct {
	Body  ast.Expr
	Holes []*ast.Splice
}

// Table numbers the templates of one compilation. Indices are assigned in
// checking order, which is source order, so they are deterministic.
type Table struct{ templates []*Template }

func (t *Table) Add(tmpl *Template) int {
	t.templates = append(t.templates, tmpl)
	return len(t.templates) - 1
}

func (t *Table) Get(i int) *Template {
	if i < 0 || i >= len(t.templates) {
		return nil
	}
	return t.templates[i]
}

// Code is the compile-time value of a quote: a template index plus one Code
// per hole, already evaluated. It is opaque to fango — no constructor, no
// projection — and never reaches generated Go.
//
// Direct carries a compiler-built fragment instead of a template: scalar
// lifting, and the traversal skeleton `Meta.match` and `Meta.construct`
// build. Pattern is set instead of Direct while a constructor pattern is
// under construction, and Pending names the record fields a partial
// construction still expects.
type Code struct {
	Template int
	Holes    []*Code
	Direct   ast.Expr
	Pattern  ast.Pattern
	Pending  []string
}

// Expand renders code as surface AST ready to be checked at the splice site.
// Each hole expands first, so a Code built from other Code produces one tree
// with no splices left in it.
func (t *Table) Expand(c *Code) ast.Expr {
	if c == nil {
		return nil
	}
	if c.Direct != nil {
		return copyExpr(c.Direct, nil)
	}
	if c.Pattern != nil {
		return nil // a half-built pattern is not an expression
	}
	tmpl := t.Get(c.Template)
	if tmpl == nil {
		return nil
	}
	subst := map[*ast.Splice]ast.Expr{}
	for i, hole := range tmpl.Holes {
		if i >= len(c.Holes) || c.Holes[i] == nil {
			return nil
		}
		filled := t.Expand(c.Holes[i])
		if filled == nil {
			return nil
		}
		subst[hole] = filled
	}
	return copyExpr(tmpl.Body, subst)
}

// Rewrite copies e, letting f replace whole subtrees. f returns nil to leave
// a node to the ordinary structural copy. Copying is what keeps two splices
// of one template from sharing AST nodes, since inference keys its solved
// types by node pointer.
func Rewrite(e ast.Expr, f func(ast.Expr) ast.Expr) ast.Expr {
	return copyWith(e, f)
}

// CopyPattern duplicates a pattern the compiler built for generated code.
// Two branches of one generated case must not share pattern nodes, because
// inference keys solved types by node pointer.
func CopyPattern(p ast.Pattern) ast.Pattern { return copyPattern(p) }

func copyExpr(e ast.Expr, subst map[*ast.Splice]ast.Expr) ast.Expr {
	return copyWith(e, func(n ast.Expr) ast.Expr {
		if s, ok := n.(*ast.Splice); ok {
			return subst[s]
		}
		return nil
	})
}

// copyWith rebuilds every expression node. Type annotations and patterns'
// immutable parts are copied too: the checker keys solved types by node
// pointer, so a shared node would make one splice's types overwrite
// another's.
func copyWith(e ast.Expr, f func(ast.Expr) ast.Expr) ast.Expr {
	if e == nil {
		return nil
	}
	if out := f(e); out != nil {
		return out
	}
	rec := func(x ast.Expr) ast.Expr { return copyWith(x, f) }
	switch e := e.(type) {
	case *ast.IntLit:
		n := *e
		return &n
	case *ast.FloatLit:
		n := *e
		return &n
	case *ast.StringLit:
		n := *e
		return &n
	case *ast.CharLit:
		n := *e
		return &n
	case *ast.UnitLit:
		n := *e
		return &n
	case *ast.Var:
		n := *e
		return &n
	case *ast.Ctor:
		n := *e
		return &n
	case *ast.Resume:
		n := *e
		return &n
	case *ast.RecordLit:
		n := *e
		n.Fields = copyFields(e.Fields, f)
		return &n
	case *ast.RecordGet:
		n := *e
		n.Record = rec(e.Record)
		n.Records = append([]string(nil), e.Records...)
		return &n
	case *ast.RecordUpdate:
		n := *e
		n.Record = rec(e.Record)
		n.Fields = copyFields(e.Fields, f)
		return &n
	case *ast.App:
		return &ast.App{Fn: rec(e.Fn), Arg: rec(e.Arg)}
	case *ast.Neg:
		n := *e
		n.Operand = rec(e.Operand)
		return &n
	case *ast.BinOp:
		n := *e
		n.L, n.R = rec(e.L), rec(e.R)
		return &n
	case *ast.If:
		n := *e
		n.Cond, n.Then, n.Else = rec(e.Cond), rec(e.Then), rec(e.Else)
		return &n
	case *ast.Lambda:
		n := *e
		n.Params = copyPatterns(e.Params)
		n.Body = rec(e.Body)
		return &n
	case *ast.Block:
		n := *e
		n.Binds = make([]ast.LocalBind, len(e.Binds))
		for i, b := range e.Binds {
			nb := b
			nb.Params = copyPatterns(b.Params)
			nb.Pattern = copyPattern(b.Pattern)
			nb.Equations = copyEquations(b.Equations, rec)
			nb.Body = rec(b.Body)
			n.Binds[i] = nb
		}
		n.Items = make([]ast.BlockItem, len(e.Items))
		for i, it := range e.Items {
			n.Items[i] = ast.BlockItem{BindIndex: it.BindIndex, Expr: rec(it.Expr)}
		}
		n.Result = rec(e.Result)
		return &n
	case *ast.Case:
		n := *e
		n.Branches = make([]ast.CaseBranch, len(e.Branches))
		for i, br := range e.Branches {
			n.Branches[i] = ast.CaseBranch{Pattern: copyPattern(br.Pattern), Body: rec(br.Body)}
		}
		n.Scrutinee = rec(e.Scrutinee)
		return &n
	case *ast.Handle:
		n := *e
		n.Body = rec(e.Body)
		n.Clauses = make([]ast.HandleClause, len(e.Clauses))
		for i, c := range e.Clauses {
			nc := c
			nc.Params = copyPatterns(c.Params)
			nc.Equations = copyEquations(c.Equations, rec)
			nc.Body = rec(c.Body)
			n.Clauses[i] = nc
		}
		if e.Return != nil {
			ret := *e.Return
			ret.Param = copyPattern(e.Return.Param)
			ret.Equations = copyEquations(e.Return.Equations, rec)
			ret.Body = rec(e.Return.Body)
			n.Return = &ret
		}
		return &n
	case *ast.Quote:
		n := *e
		n.Body = rec(e.Body)
		return &n
	case *ast.Splice:
		n := *e
		n.Operand = rec(e.Operand)
		return &n
	case *ast.TypeOf:
		n := *e
		n.Visible = cloneVisible(e.Visible)
		return &n
	case *ast.MetaValue:
		n := *e
		return &n
	default:
		panic("meta: unhandled expression node in copy")
	}
}

func cloneVisible(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyFields(fs []ast.RecordExprField, f func(ast.Expr) ast.Expr) []ast.RecordExprField {
	out := make([]ast.RecordExprField, len(fs))
	for i, field := range fs {
		nf := field
		nf.Records = append([]string(nil), field.Records...)
		nf.Value = copyWith(field.Value, f)
		out[i] = nf
	}
	return out
}

func copyPattern(p ast.Pattern) ast.Pattern {
	if p == nil {
		return nil
	}
	switch p := p.(type) {
	case *ast.PVar:
		n := *p
		return &n
	case *ast.PWildcard:
		n := *p
		return &n
	case *ast.PUnit:
		n := *p
		return &n
	case *ast.PInt:
		n := *p
		return &n
	case *ast.PFloat:
		n := *p
		return &n
	case *ast.PString:
		n := *p
		return &n
	case *ast.PChar:
		n := *p
		return &n
	case *ast.PPin:
		n := *p
		return &n
	case *ast.PRecord:
		n := *p
		n.Fields = make([]ast.RecordPatternField, len(p.Fields))
		for i, field := range p.Fields {
			nf := field
			nf.Pattern = copyPattern(field.Pattern)
			n.Fields[i] = nf
		}
		return &n
	case *ast.PCtor:
		n := *p
		n.Args = make([]ast.Pattern, len(p.Args))
		for i, a := range p.Args {
			n.Args[i] = copyPattern(a)
		}
		return &n
	default:
		panic("meta: unhandled pattern node in copy")
	}
}

func copyPatterns(ps []ast.Pattern) []ast.Pattern {
	out := make([]ast.Pattern, len(ps))
	for i, p := range ps {
		out[i] = copyPattern(p)
	}
	return out
}

func copyEquations(eqs []ast.Equation, rec func(ast.Expr) ast.Expr) []ast.Equation {
	out := make([]ast.Equation, len(eqs))
	for i, eq := range eqs {
		out[i] = ast.Equation{Params: copyPatterns(eq.Params), Body: rec(eq.Body), NameSpan: eq.NameSpan}
	}
	return out
}

// FillSpans gives compiler-built nodes a source position. The traversal
// skeleton `Meta.match` assembles has no source of its own, so a type error
// inside a derived method would otherwise have nowhere to point; quoted
// fragments keep the spans they were written with.
func FillSpans(e ast.Expr, sp source.Span) {
	if e == nil {
		return
	}
	rec := func(children ...ast.Expr) {
		for _, c := range children {
			FillSpans(c, sp)
		}
	}
	fill := func(at *source.Span) {
		if at.File == nil {
			*at = sp
		}
	}
	switch e := e.(type) {
	case *ast.IntLit:
		fill(&e.Sp)
	case *ast.FloatLit:
		fill(&e.Sp)
	case *ast.StringLit:
		fill(&e.Sp)
	case *ast.CharLit:
		fill(&e.Sp)
	case *ast.UnitLit:
		fill(&e.Sp)
	case *ast.Var:
		fill(&e.Sp)
	case *ast.Ctor:
		fill(&e.Sp)
	case *ast.Resume:
		fill(&e.Sp)
	case *ast.RecordLit:
		fill(&e.Sp)
		fill(&e.NameSpan)
		for i := range e.Fields {
			fill(&e.Fields[i].NameSpan)
			rec(e.Fields[i].Value)
		}
	case *ast.RecordGet:
		fill(&e.FieldSpan)
		rec(e.Record)
	case *ast.RecordUpdate:
		fill(&e.Sp)
		for i := range e.Fields {
			fill(&e.Fields[i].NameSpan)
			rec(e.Fields[i].Value)
		}
		rec(e.Record)
	case *ast.App:
		rec(e.Fn, e.Arg)
	case *ast.Neg:
		fill(&e.Sp)
		rec(e.Operand)
	case *ast.BinOp:
		fill(&e.OpSpan)
		rec(e.L, e.R)
	case *ast.If:
		fill(&e.Sp)
		rec(e.Cond, e.Then, e.Else)
	case *ast.Lambda:
		fill(&e.Sp)
		for i := range e.Params {
			fillPatternSpans(e.Params[i], sp)
		}
		rec(e.Body)
	case *ast.Block:
		for i := range e.Binds {
			b := &e.Binds[i]
			fill(&b.NameSpan)
			for _, param := range b.Params {
				fillPatternSpans(param, sp)
			}
			fillPatternSpans(b.Pattern, sp)
			fillEquationSpans(b.Equations, sp, rec)
			rec(b.Body)
		}
		for _, it := range e.Items {
			rec(it.Expr)
		}
		rec(e.Result)
	case *ast.Case:
		fill(&e.Sp)
		rec(e.Scrutinee)
		for _, br := range e.Branches {
			fillPatternSpans(br.Pattern, sp)
			rec(br.Body)
		}
	case *ast.Handle:
		fill(&e.Sp)
		rec(e.Body)
		for i := range e.Clauses {
			c := &e.Clauses[i]
			for _, param := range c.Params {
				fillPatternSpans(param, sp)
			}
			fillEquationSpans(c.Equations, sp, rec)
			rec(c.Body)
		}
		if e.Return != nil {
			fillPatternSpans(e.Return.Param, sp)
			fillEquationSpans(e.Return.Equations, sp, rec)
			rec(e.Return.Body)
		}
	case *ast.Quote:
		fill(&e.Sp)
		rec(e.Body)
	case *ast.Splice:
		fill(&e.Sp)
		rec(e.Operand)
	case *ast.TypeOf:
		fill(&e.Sp)
	case *ast.MetaValue:
		fill(&e.Sp)
	}
}

func fillEquationSpans(eqs []ast.Equation, sp source.Span, rec func(...ast.Expr)) {
	for i := range eqs {
		for _, param := range eqs[i].Params {
			fillPatternSpans(param, sp)
		}
		rec(eqs[i].Body)
	}
}

func fillPatternSpans(p ast.Pattern, sp source.Span) {
	if p == nil {
		return
	}
	fill := func(at *source.Span) {
		if at.File == nil {
			*at = sp
		}
	}
	switch p := p.(type) {
	case *ast.PVar:
		fill(&p.Sp)
	case *ast.PWildcard:
		fill(&p.Sp)
	case *ast.PUnit:
		fill(&p.Sp)
	case *ast.PInt:
		fill(&p.Sp)
	case *ast.PFloat:
		fill(&p.Sp)
	case *ast.PString:
		fill(&p.Sp)
	case *ast.PChar:
		fill(&p.Sp)
	case *ast.PPin:
		fill(&p.Sp)
	case *ast.PRecord:
		fill(&p.Sp)
		for i := range p.Fields {
			fill(&p.Fields[i].NameSpan)
			fillPatternSpans(p.Fields[i].Pattern, sp)
		}
	case *ast.PCtor:
		fill(&p.NameSpan)
		for _, a := range p.Args {
			fillPatternSpans(a, sp)
		}
	}
}
