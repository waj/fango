package core

import "github.com/waj/fango/internal/types"

// Rewrite builds a fresh expression/tree spine, mapping every embedded type
// before applying post bottom-up. Metadata identifying declarations stays
// shared; no input expression or slice is mutated.
func Rewrite(e Expr, typ func(types.Type) types.Type, post func(Expr) Expr) Expr {
	r := rewriter{typ: typ, post: post}
	return r.expr(e)
}

type rewriter struct {
	typ  func(types.Type) types.Type
	post func(Expr) Expr
}

func (r rewriter) types(ts []types.Type) []types.Type {
	out := make([]types.Type, len(ts))
	for i, t := range ts {
		out[i] = r.typ(t)
	}
	return out
}
func (r rewriter) exprs(es []Expr) []Expr {
	out := make([]Expr, len(es))
	for i, e := range es {
		out[i] = r.expr(e)
	}
	return out
}
func (r rewriter) effect(e EffectInstance) EffectInstance { e.Args = r.types(e.Args); return e }
func (r rewriter) expr(e Expr) Expr {
	if e == nil {
		return nil
	}
	var out Expr
	switch e := e.(type) {
	case *IntLit:
		n := *e
		n.Ty = r.typ(e.Ty)
		out = &n
	case *FloatLit:
		n := *e
		n.Ty = r.typ(e.Ty)
		out = &n
	case *StringLit:
		n := *e
		n.Ty = r.typ(e.Ty)
		out = &n
	case *CharLit:
		n := *e
		n.Ty = r.typ(e.Ty)
		out = &n
	case *BoolLit:
		n := *e
		n.Ty = r.typ(e.Ty)
		out = &n
	case *UnitLit:
		n := *e
		n.Ty = r.typ(e.Ty)
		out = &n
	case *VarRef:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.TyArgs = r.types(e.TyArgs)
		out = &n
	case *Neg:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.Operand = r.expr(e.Operand)
		out = &n
	case *NativeCall:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.Args = r.exprs(e.Args)
		out = &n
	case *Quote:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.Holes = r.exprs(e.Holes)
		out = &n
	case *TypeOf:
		n := *e
		n.Ty = r.typ(e.Ty)
		out = &n
	case *If:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.Cond, n.Then, n.Else = r.expr(e.Cond), r.expr(e.Then), r.expr(e.Else)
		out = &n
	case *Let:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.Rhs, n.Body = r.expr(e.Rhs), r.expr(e.Body)
		out = &n
	case *Lambda:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.Body = r.expr(e.Body)
		out = &n
	case *Seq:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.First, n.Then = r.expr(e.First), r.expr(e.Then)
		out = &n
	case *ResumeTail:
		n := *e
		n.ClauseResult = r.typ(e.ClauseResult)
		n.Value = r.expr(e.Value)
		n.NextState = r.expr(e.NextState)
		out = &n
	case *Perform:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.Args = r.exprs(e.Args)
		n.Effect = r.effect(e.Effect)
		out = &n
	case *ControlExit:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.Payload = r.exprs(e.Payload)
		n.Effect = r.effect(e.Effect)
		out = &n
	case *Suspend:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.Request = r.expr(e.Request)
		out = &n
	case *Bracket:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.ResourceTy = r.typ(e.ResourceTy)
		n.Acquire = r.expr(e.Acquire)
		n.Release = r.expr(e.Release)
		n.Body = r.expr(e.Body)
		out = &n
	case *Handle:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.Effect = r.effect(e.Effect)
		n.Body = r.expr(e.Body)
		if e.State != nil {
			n.State = &HandlerState{Name: e.State.Name, Initial: r.expr(e.State.Initial), Ty: r.typ(e.State.Ty)}
		}
		n.Clauses = make([]HandlerClause, len(e.Clauses))
		for i, c := range e.Clauses {
			c.Params = append([]string(nil), c.Params...)
			c.ParamTypes = r.types(c.ParamTypes)
			c.ResultType = r.typ(c.ResultType)
			c.Body = r.expr(c.Body)
			n.Clauses[i] = c
		}
		if e.Return != nil {
			n.Return = &ReturnClause{Param: e.Return.Param, Body: r.expr(e.Return.Body)}
		}
		out = &n
	case *App:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.Callee = r.expr(e.Callee)
		n.Args = r.exprs(e.Args)
		n.TyArgs = r.types(e.TyArgs)
		n.EvidenceArgs = make([]EffectInstance, len(e.EvidenceArgs))
		for i, ev := range e.EvidenceArgs {
			n.EvidenceArgs[i] = r.effect(ev)
		}
		out = &n
	case *Case:
		n := *e
		n.Ty = r.typ(e.Ty)
		n.Scrut = r.expr(e.Scrut)
		n.Tree = r.tree(e.Tree)
		out = &n
	default:
		panic("core.Rewrite: unknown expression")
	}
	return r.post(out)
}
func (r rewriter) tree(t Tree) Tree {
	switch t := t.(type) {
	case nil:
		return nil
	case *Unreachable:
		return &Unreachable{}
	case *Leaf:
		return &Leaf{Body: r.expr(t.Body)}
	case *Guard:
		return &Guard{Cond: r.expr(t.Cond), Then: r.tree(t.Then), Else: r.tree(t.Else)}
	case *SwitchCtor:
		n := *t
		n.Default = r.tree(t.Default)
		n.Cases = make([]CtorCase, len(t.Cases))
		for i, c := range t.Cases {
			c.Binds = append([]string(nil), c.Binds...)
			c.Tree = r.tree(c.Tree)
			n.Cases[i] = c
		}
		return &n
	case *SwitchLit:
		n := *t
		n.Default = r.tree(t.Default)
		n.Cases = make([]LitCase, len(t.Cases))
		for i, c := range t.Cases {
			c.Lit = r.expr(c.Lit)
			c.Tree = r.tree(c.Tree)
			n.Cases[i] = c
		}
		return &n
	default:
		panic("core.Rewrite: unknown tree")
	}
}
