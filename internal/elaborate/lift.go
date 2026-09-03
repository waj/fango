package elaborate

import (
	"fmt"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Lambda-lifting of polymorphic block bindings (§8.4): Go has no generic
// func literals, so a block binding whose generalized scheme quantifies a
// variable becomes an auxiliary top-level generic definition. Its free local
// variables become leading parameters, and every use rewrites to a call
// (through the ordinary saturation machinery — partial uses eta-expand like
// any worker). Monomorphic locals stay ordinary Lets; purity + strictness
// make capture-by-value trivially sound.

type liftedLocal struct {
	defName string
	frees   []scopeVar // captured enclosing locals, in scope order
	genTy   types.Type // the lifted definition's full generic type: frees curried onto the local's scheme body
	vars    []*types.TVar
	arity   int // len(frees) + the binding's own parameter count
}

// liftBinding lifts one generalized block binding into el.aux and registers
// it so subsequent uses (use-after-define scoping guarantees they elaborate
// later — including self-calls, registered before the body elaborates)
// rewrite to calls.
func (el *elab) liftBinding(bind *ast.LocalBind, sch types.Scheme) {
	localGenTy := el.zonkDefault(sch.Body)
	frees := el.freeLocals(bind)
	genTy := localGenTy
	for i := len(frees) - 1; i >= 0; i-- {
		genTy = &types.TFun{Arg: frees[i].ty, Eff: types.Row{}, Ret: genTy}
	}
	el.ck.LiftGen++
	lf := &liftedLocal{
		defName: fmt.Sprintf("_lift%d_%s", el.ck.LiftGen, bind.Name),
		frees:   frees,
		genTy:   genTy,
		vars:    types.RigidVarsIn(genTy),
		arity:   len(frees) + len(bind.Params),
	}
	el.lifted[bind.Name] = lf

	params := make([]string, 0, lf.arity)
	for _, f := range frees {
		params = append(params, f.name)
	}
	var body core.Expr
	if len(bind.Params) > 0 {
		argTys, _ := core.PeelFun(localGenTy, len(bind.Params))
		for i, p := range bind.Params {
			params = append(params, p.Name)
			el.pushScope(p.Name, argTys[i])
		}
		body = el.expr(bind.Body)
		el.popScope(len(bind.Params))
	} else {
		body = el.expr(bind.Body)
	}
	el.aux = append(el.aux, core.Def{
		Name:     lf.defName,
		Type:     genTy,
		TyParams: lf.vars,
		Params:   params,
		Body:     el.anf(body),
	})
}

// liftedCallee builds the callee for one use of a lifted local at its
// occurrence type: the captured frees are pre-supplied leading arguments,
// and the instantiation is matched against the lifted definition's generic
// type (which quantifies the enclosing definition's variables too, when the
// frees' types mention them).
func (el *elab) liftedCallee(lf *liftedLocal, occTy types.Type) callee {
	ty := occTy
	for i := len(lf.frees) - 1; i >= 0; i-- {
		ty = &types.TFun{Arg: lf.frees[i].ty, Eff: types.Row{}, Ret: ty}
	}
	pre := make([]core.Expr, len(lf.frees))
	for i, f := range lf.frees {
		pre[i] = &core.VarRef{Name: f.name, Ty: f.ty}
	}
	var tyArgs []types.Type
	if len(lf.vars) > 0 {
		tyArgs = matchTyArgs(lf.genTy, lf.vars, ty)
	}
	return callee{kind: core.Worker, name: lf.defName, ty: ty,
		arity: lf.arity, tyArgs: tyArgs, pre: pre}
}

// freeLocals computes the enclosing locals a binding's body mentions, in
// scope order — the lifted definition's leading parameters. No shadowing
// makes a plain name walk exact. A mention of an already-lifted sibling
// contributes that sibling's frees instead (the use site will pass them).
func (el *elab) freeLocals(bind *ast.LocalBind) []scopeVar {
	need := map[string]bool{}
	var visit func(e ast.Expr)
	visitVar := func(name string) {
		if name == bind.Name {
			return // self-recursion: becomes top-level recursion
		}
		if lf := el.lifted[name]; lf != nil {
			for _, f := range lf.frees {
				need[f.name] = true
			}
			return
		}
		if _, ok := el.scopeIdx[name]; ok {
			need[name] = true
		}
	}
	visit = func(e ast.Expr) {
		switch e := e.(type) {
		case *ast.Var:
			visitVar(e.Name)
		case *ast.App:
			visit(e.Fn)
			visit(e.Arg)
		case *ast.Neg:
			visit(e.Operand)
		case *ast.If:
			visit(e.Cond)
			visit(e.Then)
			visit(e.Else)
		case *ast.BinOp:
			visit(e.L)
			visit(e.R)
		case *ast.Lambda:
			visit(e.Body)
		case *ast.Block:
			for i := range e.Binds {
				visit(e.Binds[i].Body)
			}
			visit(e.Result)
		case *ast.Case:
			visit(e.Scrutinee)
			for i := range e.Branches {
				visit(e.Branches[i].Body)
			}
		}
	}
	visit(bind.Body)
	var frees []scopeVar
	for _, sv := range el.scope { // scope order: deterministic parameter order
		if need[sv.name] {
			frees = append(frees, sv)
		}
	}
	return frees
}
