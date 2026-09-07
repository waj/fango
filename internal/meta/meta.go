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
import "github.com/waj/fango/internal/types"

// TypeRepr is the compiler-owned value produced by typeOf. Identity comes
// from TCon.Unique and structural children; Name is only presentation data.
// Visible is captured at the reflection site for later schema inspection.
type TypeRepr struct {
	Type    types.Type
	Visible map[int]bool
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
type Code struct {
	Template int
	Holes    []*Code
	Direct   ast.Expr
}

// Expand renders code as surface AST ready to be checked at the splice site.
// Each hole expands first, so a Code built from other Code produces one tree
// with no splices left in it.
func (t *Table) Expand(c *Code) ast.Expr {
	if c != nil && c.Direct != nil {
		return copyExpr(c.Direct, nil)
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
		n.Params = append([]ast.Param(nil), e.Params...)
		n.Body = rec(e.Body)
		return &n
	case *ast.Block:
		n := *e
		n.Binds = make([]ast.LocalBind, len(e.Binds))
		for i, b := range e.Binds {
			nb := b
			nb.Params = append([]ast.Param(nil), b.Params...)
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
			nc.Params = append([]ast.Param(nil), c.Params...)
			nc.Body = rec(c.Body)
			n.Clauses[i] = nc
		}
		if e.Return != nil {
			ret := *e.Return
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
	switch p := p.(type) {
	case *ast.PVar:
		n := *p
		return &n
	case *ast.PWildcard:
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
