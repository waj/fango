package elaborate

import (
	"fmt"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Lambda-lifting of polymorphic block bindings (doc/design.md, "Go backend and runtime"): Go has no generic
// func literals, so a block binding whose generalized scheme quantifies a
// variable becomes an auxiliary top-level generic definition. Its free local
// variables become leading parameters, and every use rewrites to a call
// (through the ordinary saturation machinery — partial uses eta-expand like
// any worker). Monomorphic locals stay ordinary Lets; purity + strictness
// make capture-by-value trivially sound.

type liftedLocal struct {
	scheme   types.Scheme
	defName  string
	frees    []scopeVar // captured enclosing locals, in scope order
	genTy    types.Type // the lifted definition's full generic type: frees curried onto the local's scheme body
	rawGenTy types.Type
	vars     []*types.TVar
	arity    int // len(frees) + the binding's own parameter count
	effects  []core.EffectInstance
}

// liftBinding lifts one generalized block binding into el.aux and registers
// it so subsequent uses (use-after-define scoping guarantees they elaborate
// later — including self-calls, registered before the body elaborates)
// rewrite to calls.
func (el *elab) liftBinding(bind *ast.LocalBind, sch types.Scheme) {
	rawLocalGenTy := el.ck.Sub.Apply(sch.Body)
	el.defaultFree(rawLocalGenTy)
	rawLocalGenTy = el.ck.Sub.Apply(rawLocalGenTy)
	localGenTy := eraseRows(rawLocalGenTy)
	frees := el.freeLocals(bind)
	savedDicts := len(el.dicts)
	dictNames, dictTypes := el.bindDictionaries(sch.Preds)
	defer func() { el.dicts = el.dicts[:savedDicts]; el.popScope(len(dictNames)) }()
	genTy := prependTypes(dictTypes, localGenTy)
	rawGenTy := prependTypes(dictTypes, rawLocalGenTy)
	for i := len(frees) - 1; i >= 0; i-- {
		genTy = &types.TFun{Arg: frees[i].ty, Eff: types.Row{}, Ret: genTy}
		rawGenTy = &types.TFun{Arg: frees[i].ty, Eff: types.Row{}, Ret: rawGenTy}
	}
	var defName string
	if el.stableLifts {
		el.liftSeq++
		defName = fmt.Sprintf("_lift_%s_%d_%s", el.declName, el.liftSeq, bind.Name)
	} else {
		el.ck.LiftGen++
		defName = fmt.Sprintf("_lift%d_%s", el.ck.LiftGen, bind.Name)
	}
	lf := &liftedLocal{
		scheme:   sch,
		defName:  defName,
		frees:    frees,
		genTy:    genTy,
		rawGenTy: rawGenTy,
		vars:     runtimeRigidVars(rawGenTy),
		arity:    len(frees) + len(dictNames) + len(bind.Params),
		effects:  executingEffects(rawGenTy, len(frees)+len(dictNames)+len(bind.Params)),
	}
	el.lifted[bind.Name] = lf

	params := make([]string, 0, lf.arity)
	for _, f := range frees {
		params = append(params, f.name)
	}
	params = append(params, dictNames...)
	var body core.Expr
	if len(bind.Params) > 0 {
		argTys, _ := core.PeelFun(localGenTy, len(bind.Params))
		bound := 0
		for i, p := range bind.Params {
			params = append(params, coreParamName(p))
			if p.Name != "_" && p.Name != "()" {
				el.pushScope(p.Name, argTys[i])
				bound++
			}
		}
		body = el.expr(bind.Body)
		el.popScope(bound)
	} else {
		body = el.expr(bind.Body)
	}
	el.aux = append(el.aux, core.Def{
		Name:         lf.defName,
		Type:         genTy,
		TyParams:     lf.vars,
		Params:       params,
		EffectParams: executingEffects(rawGenTy, lf.arity),
		Body:         el.anf(body),
	})
}

// liftedCallee builds the callee for one use of a lifted local at its
// occurrence type: the captured frees are pre-supplied leading arguments,
// and the instantiation is matched against the lifted definition's generic
// type (which quantifies the enclosing definition's variables too, when the
// frees' types mention them).
func (el *elab) liftedCallee(lf *liftedLocal, occTy, rawOccTy types.Type) callee {
	var dictArgs []core.Expr
	var dictTypes []types.Type
	for _, p := range el.instantiatedPreds(lf.scheme, rawOccTy) {
		d := el.dictionary(p)
		dictArgs = append(dictArgs, d)
		dictTypes = append(dictTypes, d.Type())
	}
	ty := prependTypes(dictTypes, occTy)
	rawTy := prependTypes(dictTypes, rawOccTy)
	for i := len(lf.frees) - 1; i >= 0; i-- {
		ty = &types.TFun{Arg: lf.frees[i].ty, Eff: types.Row{}, Ret: ty}
		rawTy = &types.TFun{Arg: lf.frees[i].ty, Eff: types.Row{}, Ret: rawTy}
	}
	pre := make([]core.Expr, len(lf.frees))
	for i, f := range lf.frees {
		pre[i] = &core.VarRef{Name: f.name, Ty: f.ty}
	}
	pre = append(pre, dictArgs...)
	var tyArgs []types.Type
	evidence := append([]core.EffectInstance(nil), lf.effects...)
	for i := range evidence {
		evidence[i].Args = append([]types.Type(nil), evidence[i].Args...)
	}
	if len(lf.vars) > 0 {
		tyArgs = matchTyArgs(lf.rawGenTy, lf.vars, rawTy)
		m := make(map[int]types.Type, len(lf.vars))
		for i, v := range lf.vars {
			m[v.ID] = tyArgs[i]
		}
		for i := range evidence {
			for j, a := range evidence[i].Args {
				evidence[i].Args[j] = types.SubstRigid(a, m)
			}
		}
	}
	return callee{kind: core.Worker, name: lf.defName, ty: ty,
		arity: lf.arity, tyArgs: tyArgs, pre: pre, evidence: evidence}
}

// freeLocals computes the enclosing locals a binding's body mentions, in
// scope order — the lifted definition's leading parameters. No shadowing
// makes a plain name walk exact. A mention of an already-lifted sibling
// contributes that sibling's frees instead (the use site will pass them).
func (el *elab) freeLocals(bind *ast.LocalBind) []scopeVar {
	need := map[string]bool{}
	for _, d := range el.dicts {
		if v, ok := d.value.(*core.VarRef); ok {
			need[v.Name] = true
		}
	}
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
		case *ast.Handle:
			visit(e.Body)
			for i := range e.Clauses {
				visit(e.Clauses[i].Body)
			}
			if e.Return != nil {
				visit(e.Return.Body)
			}
		case *ast.Resume:
			// No value child; applications containing it are handled above.
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
