package elaborate

import (
	"fmt"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

// A service clause computes its reply under the invocation adapter, closes
// that adapter, then tail-resumes its subject. No resume crosses a handler.
func serviceResumeValue(expr core.Expr, owner types.ResumeID, result types.Type) core.Expr {
	var tree func(core.Tree) core.Tree
	var tail func(core.Expr) core.Expr
	tail = func(expr core.Expr) core.Expr {
		switch e := expr.(type) {
		case *core.ResumeTail:
			if e.Owner != owner || e.NextState != nil {
				panic("invalid service resume")
			}
			return e.Value
		case *core.ControlExit:
			n := *e
			n.Ty = result
			return &n
		case *core.Let:
			n := *e
			n.Body, n.Ty = tail(e.Body), result
			return &n
		case *core.Seq:
			n := *e
			n.Then, n.Ty = tail(e.Then), result
			return &n
		case *core.If:
			n := *e
			n.Then, n.Else, n.Ty = tail(e.Then), tail(e.Else), result
			return &n
		case *core.Case:
			n := *e
			n.Tree, n.Ty = tree(e.Tree), result
			return &n
		default:
			panic(fmt.Sprintf("invalid service clause tail %T", e))
		}
	}
	tree = func(t core.Tree) core.Tree {
		switch t := t.(type) {
		case *core.Leaf:
			return &core.Leaf{Body: tail(t.Body)}
		case *core.Unreachable:
			return t
		case *core.Guard:
			return &core.Guard{Cond: t.Cond, Then: tree(t.Then), Else: tree(t.Else)}
		case *core.SwitchCtor:
			n := *t
			n.Cases = append([]core.CtorCase(nil), t.Cases...)
			for i := range n.Cases {
				n.Cases[i].Tree = tree(n.Cases[i].Tree)
			}
			if t.Default != nil {
				n.Default = tree(t.Default)
			}
			return &n
		case *core.SwitchLit:
			n := *t
			n.Cases = append([]core.LitCase(nil), t.Cases...)
			for i := range n.Cases {
				n.Cases[i].Tree = tree(n.Cases[i].Tree)
			}
			if t.Default != nil {
				n.Default = tree(t.Default)
			}
			return &n
		default:
			panic("invalid service decision tree")
		}
	}
	return tail(expr)
}

func invocationHandler(body, callback core.Expr, ev core.EffectInstance, ck *infer.Checker) core.Expr {
	scope := ev.Captures.Scopes[0]
	// Names must be stable across module consumers; scope identities are
	// fresh proof IDs and must never leak into emitted dependency symbols.
	prefix := callback.(*core.VarRef).Name
	name := prefix + "_request"
	fn := callback.Type().(*types.TFun)
	ck.ResumeGen++
	resume := ck.ResumeGen
	op := ck.EffectsByUnique[ev.Unique].Ops[0]
	call := &core.App{CalleeKind: core.Value, Callee: callback, Args: []core.Expr{&core.VarRef{Name: name, Local: true, Ty: fn.Arg}}, Ty: fn.Ret, Control: types.FunctionControl(fn)}
	reply := prefix + "_reply"
	clause := &core.Let{Name: reply, Rhs: call, Body: &core.ResumeTail{Owner: resume, Value: &core.VarRef{Name: reply, Local: true, Ty: fn.Ret}, ClauseResult: body.Type()}, Ty: body.Type()}
	return &core.Handle{Scope: scope, Scoped: true, Effect: ev, Body: body, Ty: body.Type(), Control: types.Control{Transport: types.Machine}, Clauses: []core.HandlerClause{{Op: op, Params: []string{name}, ParamTypes: []types.Type{fn.Arg}, ResultType: fn.Ret, ResumeID: resume, Body: clause}}}
}

func serviceRunDef(ty types.Type, ck *infer.Checker) core.Def {
	args, result := core.PeelFun(ty, 2)
	pause, action := args[0].(*types.TFun), args[1].(*types.TFun)
	scope := ck.Sup.FreshScope()
	eff := ck.Effects[types.ServiceInvocationName]
	ev := core.EffectInstance{Unique: eff.Unique, Name: eff.Name, Args: []types.Type{pause.Arg, pause.Ret}, Captures: types.ScopeCapture(scope), Control: types.Control{Transport: types.Machine}}
	active := &core.Work{Kind: "invocation-slot", Args: []core.Expr{&core.VarRef{Name: "_pause", Local: true, Ty: pause}}, Ty: pause}
	call := &core.App{CalleeKind: core.Value, Callee: &core.VarRef{Name: "_action", Local: true, Ty: action}, Args: []core.Expr{&core.UnitLit{Ty: ck.B.Unit}}, EvidenceArgs: []core.EffectInstance{ev}, Ty: result, Control: types.FunctionControl(action)}
	body := invocationHandler(call, &core.VarRef{Name: "_active", Local: true, Ty: pause}, ev, ck)
	body = &core.Let{Name: "_active", Rhs: active, Body: body, Ty: result}
	return core.Def{Name: types.ServiceRunName, Owner: "Service", Type: ty, TyParams: runtimeRigidVars(ty), Params: []string{"_pause", "_action"}, ParamCaptures: []types.CaptureVar{ck.Sup.FreshCapture(), ck.Sup.FreshCapture()}, Control: core.ArrowControl(ty, 2), Body: body}
}

func (el *elab) invocationArgument(label types.EffLabel) core.Expr {
	eff := el.ck.EffectsByUnique[label.Unique]
	name := fmt.Sprintf("_invocationRequest%d", el.tmp)
	el.tmp++
	ty := types.InvocationCallback(label)
	inst := core.EffectInstance{Unique: label.Unique, Name: label.Name, Args: label.Args, Captures: el.evidenceCaptures(label.Unique), Control: el.evidenceControl(label.Unique)}
	call := &core.Perform{Op: eff.Ops[0], Effect: inst, Args: []core.Expr{&core.VarRef{Name: name, Local: true, Ty: label.Args[0]}}, Ty: label.Args[1], Control: inst.Control}
	suspension := el.ck.Effects[types.CoroutineSuspensionName]
	raw := &types.TFun{Arg: label.Args[0], Ret: label.Args[1], Eff: types.Row{Labels: []types.EffLabel{{Unique: suspension.Unique, Name: suspension.Name, Suspension: true}}}}
	callback := &core.Lambda{Param: name, ParamCapture: el.ck.Sup.FreshCapture(), Body: call, Ty: ty, SourceType: raw}
	return &core.Work{Kind: "invocation-argument", Args: []core.Expr{callback}, SourceRow: types.Row{Labels: []types.EffLabel{label}}, Ty: ty}
}
