package codegen

import (
	goast "go/ast"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func memberName(mode types.Transport) string {
	switch mode {
	case types.Exit:
		return "Exit"
	case types.Machine:
		return "Machine"
	default:
		return "Direct"
	}
}
func callbackMember(value goast.Expr, mode types.Transport) goast.Expr {
	return &goast.SelectorExpr{X: value, Sel: ident(memberName(mode))}
}
func (g *gen) callbackMemberType(fn *types.TFun, mode types.Transport) *goast.FuncType {
	var params []paramSpec
	for _, label := range types.SortedRow(fn.Eff).Labels {
		if !types.RuntimeEvidenceEffect(label) {
			continue
		}
		params = append(params, paramSpec{typ: g.effectTypeMode(core.EffectInstance{Unique: label.Unique, Name: label.Name, Args: label.Args, Control: types.Control{Transport: mode}}, mode)})
	}
	if types.FunctionOpenRow(fn) {
		params = append(params, paramSpec{typ: g.rowType()})
	}
	params = append(params, paramSpec{typ: g.goType(fn.Arg)})
	result := g.goType(fn.Ret)
	if mode == types.Exit {
		result = g.outcomeType(fn.Ret)
	} else if mode == types.Machine {
		result = selector("fangort", "MachineStart")
	}
	return &goast.FuncType{Params: paramFields(params), Results: &goast.FieldList{List: []*goast.Field{{Type: result}}}}
}
func (g *gen) callbackType(fn *types.TFun) goast.Expr {
	fields := []*goast.Field{}
	fields = append(fields, &goast.Field{Names: []*goast.Ident{ident("PauseOwner")}, Type: &goast.StarExpr{X: selector("fangort", "YieldOwner")}})
	for _, mode := range []types.Transport{types.Direct, types.Exit, types.Machine} {
		fields = append(fields, &goast.Field{Names: []*goast.Ident{ident(memberName(mode))}, Type: g.callbackMemberType(fn, mode)})
	}
	g.usesFangort = true
	return &goast.StructType{Fields: &goast.FieldList{List: fields}}
}
func (g *gen) callbackValue(lam *core.Lambda) goast.Expr {
	fn := lam.Ty.(*types.TFun)
	minimum := g.callbackMinimum(lam)
	var fields []goast.Expr
	if closure := g.machineClosures[lam]; closure != nil {
		if target, request := forwardedPause(g.machineWorkers[closure.Worker]); target != nil {
			arg, identity := request.(*core.VarRef)
			callee := target.(*core.VarRef)
			calleeType := callee.Ty.(*types.TFun)
			if identity && arg.Name == lam.Param && callee.Name != lam.Param && types.Equal(fn.Arg, calleeType.Arg) && types.Equal(fn.Ret, calleeType.Ret) {
				// Identity adapters may widen an unused row, but cannot change
				// the request/reply protocol or perform work before forwarding.
				fields = append(fields, &goast.KeyValueExpr{Key: ident("PauseOwner"), Value: &goast.SelectorExpr{X: g.machineExpr(callee), Sel: ident("PauseOwner")}})
			}
		}
	}
	for _, mode := range []types.Transport{types.Direct, types.Exit} {
		if minimum > mode {
			continue
		}
		fields = append(fields, &goast.KeyValueExpr{Key: ident(memberName(mode)), Value: g.directLambdaMember(lam, mode)})
	}
	var machine goast.Expr
	if g.machineClosures[lam] != nil {
		machine = g.machineLambdaExpr(lam)
	} else {
		mode := minimum
		member := g.directLambdaMember(lam, mode)
		var params []paramSpec
		var args []goast.Expr
		for _, label := range types.SortedRow(fn.Eff).Labels {
			if !types.RuntimeEvidenceEffect(label) {
				continue
			}
			name := g.evidenceName(label.Name)
			ev := core.EffectInstance{Unique: label.Unique, Name: label.Name, Args: label.Args, Control: types.Control{Transport: types.Machine}}
			params = append(params, paramSpec{name: name, typ: g.callbackMemberType(fn, types.Machine).Params.List[len(params)].Type})
			args = append(args, g.evidenceArg(ev, ident(name), types.Machine, mode))
		}
		if types.FunctionOpenRow(fn) {
			params = append(params, paramSpec{name: "rowEvidence", typ: g.rowType()})
			args = append(args, ident("rowEvidence"))
		}
		params = append(params, paramSpec{name: "value", typ: g.goType(fn.Arg)})
		args = append(args, ident("value"))
		invoke := callExpr(member, args...)
		var body []goast.Stmt
		if mode == types.Exit {
			body = []goast.Stmt{varDeclStmt("result", g.outcomeType(fn.Ret), invoke), &goast.ReturnStmt{Results: []goast.Expr{selector("result", "Value"), selector("result", "Exit")}}}
		} else {
			body = []goast.Stmt{&goast.ReturnStmt{Results: []goast.Expr{invoke, ident("nil")}}}
		}
		run := &goast.FuncLit{Type: &goast.FuncType{Params: &goast.FieldList{}, Results: &goast.FieldList{List: []*goast.Field{{Type: ident("any")}, {Type: &goast.StarExpr{X: selector("fangort", "ExitRequest")}}}}}, Body: &goast.BlockStmt{List: body}}
		machine = funcLitParams(params, selector("fangort", "MachineStart"), []goast.Stmt{returnStmt(callExpr(selector("fangort", "ImmediateStart"), run))})
	}
	fields = append(fields, &goast.KeyValueExpr{Key: ident("Machine"), Value: machine})
	return &goast.CompositeLit{Type: g.callbackType(fn), Elts: fields}
}

// A closure cannot call a captured resumptive handler using a weaker transport
// than that handler's record. Locally installed evidence and evidence supplied
// on invocation do not constrain construction; nested closures decide their own
// member availability when constructed.
func (g *gen) callbackMinimum(lam *core.Lambda) types.Transport {
	minimum := core.ExprControl(lam.Body).Transport
	bound := map[int]int{}
	for _, ev := range lam.EffectParams {
		bound[ev.Unique]++
	}
	for _, ev := range lam.RowEffects {
		bound[ev.Unique]++
	}
	check := func(ev core.EffectInstance) {
		if bound[ev.Unique] != 0 {
			return
		}
		if effect := g.effects[ev.Unique]; effect != nil && (effect.Suspension || len(effect.Ops) > 0 && effect.Ops[0].Abort) {
			return
		}
		if mode := g.currentEvidenceMode(ev.Unique); mode > minimum {
			minimum = mode
		}
	}
	var visit func(core.Expr)
	visit = func(body core.Expr) {
		core.InspectPruned(body, func(e core.Expr) bool {
			switch e := e.(type) {
			case *core.Lambda:
				return false
			case *core.App:
				for _, ev := range e.EvidenceArgs {
					check(ev)
				}
			case *core.Perform:
				check(e.Effect)
			case *core.Handle:
				bound[e.Effect.Unique]++
				visit(e.Body)
				bound[e.Effect.Unique]--
				for _, clause := range e.Clauses {
					visit(clause.Body)
				}
				if e.Return != nil {
					visit(e.Return.Body)
				}
				if e.State != nil {
					visit(e.State.Initial)
				}
				return false
			}
			return true
		})
	}
	visit(lam.Body)
	return minimum
}
