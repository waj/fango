package codegen

import (
	"fmt"
	goast "go/ast"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// representationType describes a value independently of the transport used
// to execute the expression which constructs it.
func (g *gen) representationType(ty types.Type, mode types.Transport) goast.Expr {
	oldControl, oldABI := g.control, g.abi
	g.control, g.abi = mode, mode
	defer func() { g.control, g.abi = oldControl, oldABI }()
	return g.goType(ty)
}

// liftMachineValue preserves strict construction while deferring callback
// execution until its frame is stepped. Pure curried arrows remain direct.
func (g *gen) liftMachineValue(value goast.Expr, ty types.Type, from types.Transport) goast.Expr {
	return g.adaptMachineValue(value, ty, ty, from)
}

func (g *gen) synchronousMachineWorkerCall(e *core.App, formal []types.Type, sub map[int]types.Type, mode types.Transport) goast.Expr {
	var args []goast.Expr
	var body []goast.Stmt
	for _, ev := range e.EvidenceArgs {
		stack := g.evidence[ev.Unique]
		args = append(args, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Unique), types.Machine))
	}
	for i, arg := range e.Args {
		// Machine workers retain Unit parameters, unlike direct workers.
		name := fmt.Sprintf("t_syncworker%d", g.tmp)
		g.tmp++
		want := types.SubstRigid(formal[i], sub)
		value := g.adaptMachineValue(g.expr(arg, 0), arg.Type(), want, g.representationMode())
		body = append(body, varDeclStmt(name, g.representationType(want, types.Machine), value))
		args = append(args, ident(name))
	}
	ref := e.Callee.(*core.VarRef)
	frame := callExpr(indexExpr(g.machineConstructorRef(ref.Name), g.goTypes(e.TyArgs)), args...)
	result := callExpr(indexExpr(selector("fangort", "RunSynchronousMachine"), []goast.Expr{g.goType(e.Ty)}), frame)
	resultType := g.outcomeType(e.Ty)
	if mode == types.Direct {
		result = callExpr(selector("fangort", "RequireNormal"), result)
		resultType = g.goType(e.Ty)
	}
	body = append(body, returnStmt(result))
	return callExpr(funcLit(resultType, body))
}

func (g *gen) adaptMachineValue(value goast.Expr, ty, want types.Type, from types.Transport) goast.Expr {
	if g.sameRepresentation(ty, want, from, types.Machine) {
		return value
	}
	fn, ok := ty.(*types.TFun)
	if !ok {
		return value
	}
	targetFn, ok := want.(*types.TFun)
	if !ok {
		return value
	}
	sourceMode := types.FunctionControl(fn).Resolve(from)
	targetMode := types.FunctionControl(targetFn).Resolve(types.Machine)
	name := fmt.Sprintf("t_lift%d", g.tmp)
	g.tmp++
	argName := fmt.Sprintf("t_liftarg%d", g.tmp)
	g.tmp++
	params := []paramSpec{}
	args := []goast.Expr{}
	for _, label := range types.SortedRow(fn.Eff).Labels {
		if !types.RuntimeEvidenceEffect(label) {
			continue
		}
		// Fixed evidence has the same representation in each family. Open-row
		// evidence is supplied by the closure at its definition site.
		evName := fmt.Sprintf("t_liftev%d", g.tmp)
		g.tmp++
		oldControl, oldABI := g.control, g.abi
		g.control, g.abi = types.Machine, types.Machine
		evTy := g.goType(fn).(*goast.FuncType).Params.List[len(params)].Type
		g.control, g.abi = oldControl, oldABI
		params = append(params, paramSpec{name: evName, typ: evTy})
		args = append(args, ident(evName))
	}
	params = append(params, paramSpec{name: argName, typ: g.representationType(fn.Arg, types.Machine)})
	args = append(args, ident(argName))
	invoke := callExpr(ident(name), args...)
	var body []goast.Stmt
	if targetMode == types.Machine && sourceMode != types.Machine {
		var runBody []goast.Stmt
		if sourceMode == types.Exit {
			result := fmt.Sprintf("t_liftout%d", g.tmp)
			g.tmp++
			runBody = []goast.Stmt{varDeclStmt(result, &goast.IndexExpr{X: selector("fangort", "Outcome"), Index: g.representationType(fn.Ret, from)}, invoke),
				&goast.ReturnStmt{Results: []goast.Expr{g.adaptMachineValue(selector(result, "Value"), fn.Ret, targetFn.Ret, from), selector(result, "Exit")}}}
		} else {
			runBody = []goast.Stmt{&goast.ReturnStmt{Results: []goast.Expr{g.adaptMachineValue(invoke, fn.Ret, targetFn.Ret, from), ident("nil")}}}
		}
		run := &goast.FuncLit{Type: &goast.FuncType{Params: &goast.FieldList{}, Results: &goast.FieldList{List: []*goast.Field{{Type: ident("any")}, {Type: &goast.StarExpr{X: selector("fangort", "ExitRequest")}}}}}, Body: &goast.BlockStmt{List: runBody}}
		body = []goast.Stmt{returnStmt(callExpr(selector("fangort", "ImmediateMachine"), run))}
	} else {
		body = []goast.Stmt{returnStmt(g.adaptMachineValue(invoke, fn.Ret, targetFn.Ret, from))}
	}
	resultType := g.representationType(targetFn.Ret, types.Machine)
	if targetMode == types.Machine {
		resultType = selector("fangort", "MachineFrame")
	}
	lifted := funcLitParams(params, resultType, body)
	return callExpr(funcLitParams([]paramSpec{{name: name, typ: g.representationType(ty, from)}}, g.representationType(want, types.Machine), []goast.Stmt{returnStmt(lifted)}), value)
}

// synchronousMachineCallback discharges the source-checked non-suspension
// obligation at a Scope parameter. It never changes an unchecked call slot.
func (g *gen) synchronousMachineCallback(arg core.Expr) goast.Expr {
	fn := arg.Type().(*types.TFun)
	name := fmt.Sprintf("t_sync%d", g.tmp)
	g.tmp++
	param := fmt.Sprintf("t_syncarg%d", g.tmp)
	g.tmp++
	invoke := callExpr(ident(name), ident(param))
	mode := types.FunctionControl(fn).Resolve(types.Machine)
	var result goast.Expr
	switch mode {
	case types.Machine:
		result = callExpr(indexExpr(selector("fangort", "RunSynchronousMachine"), []goast.Expr{g.goType(fn.Ret)}), invoke)
	case types.Exit:
		result = invoke
	default:
		result = g.normalOutcome(fn.Ret, invoke)
	}
	callback := funcLitParams([]paramSpec{{name: param, typ: g.goType(fn.Arg)}}, g.outcomeType(fn.Ret), []goast.Stmt{returnStmt(result)})
	callbackType := &goast.FuncType{Params: paramFields([]paramSpec{{typ: g.goType(fn.Arg)}}), Results: &goast.FieldList{List: []*goast.Field{{Type: g.outcomeType(fn.Ret)}}}}
	return callExpr(funcLitParams([]paramSpec{{name: name, typ: g.goType(fn)}}, callbackType, []goast.Stmt{returnStmt(callback)}), g.machineExpr(arg))
}

func (g *gen) sameRepresentation(a, b types.Type, from, to types.Transport) bool {
	if !types.Equal(a, b) {
		return false
	}
	switch a := a.(type) {
	case *types.TFun:
		other := b.(*types.TFun)
		return types.FunctionControl(a).Resolve(from) == types.FunctionControl(other).Resolve(to) && g.sameRepresentation(a.Arg, other.Arg, from, to) && g.sameRepresentation(a.Ret, other.Ret, from, to)
	case *types.TCon:
		if a.Name == types.IteratorTypeName {
			return true
		}
		if from != to && g.controlledType(a, nil) {
			return false
		}
		other := b.(*types.TCon)
		for i, arg := range a.Args {
			if !g.sameRepresentation(arg, other.Args[i], from, to) {
				return false
			}
		}
	}
	return true
}
