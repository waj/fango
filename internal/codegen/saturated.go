package codegen

import (
	"fmt"
	goast "go/ast"
	"maps"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

type flatCallback struct {
	arity int
	mode  types.Transport
}

func (g *gen) callbackContract(d *core.Def, index int, context types.Transport) (core.CallbackABI, types.Transport, bool) {
	if g.disableOptimizations || d == nil || !d.ABI.Valid || index >= len(d.ABI.Callbacks) {
		return core.CallbackABI{}, types.Direct, false
	}
	c := d.ABI.Callbacks[index]
	mode, ok := c.Mode(context)
	return c, mode, ok
}

// Only the last stage may have evidence. Earlier stages are verified pure
// currying steps. Evidence and the residual row precede all source arguments.
func (g *gen) flatCallbackType(t types.Type, arity int, mode types.Transport) *goast.FuncType {
	args, _ := core.PeelFun(t, arity)
	last := t.(*types.TFun)
	for range arity - 1 {
		last = last.Ret.(*types.TFun)
	}
	shape := g.callbackMemberType(last, mode)
	params := append([]*goast.Field(nil), shape.Params.List[:len(shape.Params.List)-1]...)
	for _, arg := range args {
		params = append(params, &goast.Field{Type: g.goType(arg)})
	}
	return &goast.FuncType{Params: &goast.FieldList{List: params}, Results: shape.Results}
}

func (g *gen) workerArgumentType(d *core.Def, index int, t types.Type, mode types.Transport) goast.Expr {
	if c, selected, ok := g.callbackContract(d, index, mode); ok {
		return g.flatCallbackType(t, c.Arity, selected)
	}
	return g.goType(t)
}

func (g *gen) workerArgument(d *core.Def, index int, e core.Expr, context types.Transport) goast.Expr {
	c, mode, ok := g.callbackContract(d, index, context)
	if !ok {
		return g.expr(e, 0)
	}
	if ref, ok := e.(*core.VarRef); ok && ref.Local {
		if lam := g.forwarders[ref.Name]; lam != nil {
			if value, ok := g.saturatedLambda(lam, c.Arity, mode); ok {
				return value
			}
			panic("codegen: restricted local callback cannot be materialized")
		}
		if existing, found := g.flatCallbacks[ref.Name]; found {
			if existing != (flatCallback{c.Arity, mode}) {
				panic("codegen: incompatible saturated callback forwarding")
			}
			return ident(mangleValue(ref.Name))
		}
	}
	if lam, ok := e.(*core.Lambda); ok {
		if value, ok := g.saturatedLambda(lam, c.Arity, mode); ok {
			return value
		}
	}
	value := g.expr(e, 0)
	if c.Arity == 1 {
		return callbackMember(value, mode)
	}
	// Evaluate the original callable exactly once, at the original argument
	// position. The fallback preserves intermediate execution on every call.
	serial := g.tmp
	g.tmp++
	name := fmt.Sprintf("t_callback%d", serial)
	shape := g.flatCallbackType(e.Type(), c.Arity, mode)
	var arguments []goast.Expr
	for i, field := range shape.Params.List {
		parameter := fmt.Sprintf("t_callbackArg%d_%d", serial, i)
		field.Names = []*goast.Ident{ident(parameter)}
		arguments = append(arguments, ident(parameter))
	}
	prefix := len(arguments) - c.Arity
	call := goast.Expr(ident(name))
	for i := range c.Arity {
		selected := types.Direct
		var args []goast.Expr
		if i == c.Arity-1 {
			selected = mode
			args = append(args, arguments[:prefix]...)
		}
		args = append(args, arguments[prefix+i])
		call = callExpr(callbackMember(call, selected), args...)
	}
	adapter := &goast.FuncLit{Type: shape, Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(call)}}}
	if mode == types.Exit {
		_, ret := core.PeelFun(e.Type(), c.Arity)
		g.markOutcomeCall(call.(*goast.CallExpr), g.goType(ret))
	}
	return callExpr(funcLitParams([]paramSpec{{name: name, typ: g.goType(e.Type())}}, g.flatCallbackType(e.Type(), c.Arity, mode), []goast.Stmt{returnStmt(adapter)}), value)
}

func (g *gen) saturatedLambda(lam *core.Lambda, arity int, mode types.Transport) (goast.Expr, bool) {
	current := lam
	var prefix []*core.Lambda
	for range arity - 1 {
		fn := current.Ty.(*types.TFun)
		control := types.FunctionControl(fn)
		if control != (types.Control{}) || len(fn.Eff.Labels) != 0 || types.FunctionOpenRow(fn) {
			return nil, false
		}
		next, ok := current.Body.(*core.Lambda)
		if !ok {
			return nil, false
		}
		prefix = append(prefix, current)
		current = next
	}
	if g.callbackMinimum(current) > mode {
		panic("codegen: saturated callback selects unavailable transport")
	}
	oldCallbacks, oldForwarders := g.flatCallbacks, g.forwarders
	g.flatCallbacks, g.forwarders = maps.Clone(oldCallbacks), maps.Clone(oldForwarders)
	defer func() { g.flatCallbacks, g.forwarders = oldCallbacks, oldForwarders }()
	var params []paramSpec
	for _, step := range prefix {
		delete(g.flatCallbacks, step.Param)
		delete(g.forwarders, step.Param)
		fn := step.Ty.(*types.TFun)
		name := step.Param
		if name != "_" {
			name = mangleValue(name)
		}
		params = append(params, paramSpec{name, g.goType(fn.Arg)})
		old, had := g.caseVarTys[step.Param]
		g.caseVarTys[step.Param] = fn.Arg
		defer func() {
			if had {
				g.caseVarTys[step.Param] = old
			} else {
				delete(g.caseVarTys, step.Param)
			}
		}()
	}
	member := g.directLambdaMember(current, mode).(*goast.FuncLit)
	fields := member.Type.Params.List
	var combined []*goast.Field
	combined = append(combined, fields[:len(fields)-1]...)
	combined = append(combined, paramFields(params).List...)
	combined = append(combined, fields[len(fields)-1])
	member.Type.Params = &goast.FieldList{List: combined}
	return member, true
}

func (g *gen) flatCallbackCall(e *core.App) (goast.Expr, bool) {
	head, stages := core.CallSpine(e)
	ref, ok := head.(*core.VarRef)
	if !ok || !ref.Local {
		return nil, false
	}
	flat, ok := g.flatCallbacks[ref.Name]
	if !ok {
		return nil, false
	}
	if flat.arity != len(stages) || !core.SaturatedCallback(stages) {
		panic("codegen: unsaturated use of restricted callback")
	}
	last := stages[len(stages)-1]
	if last.Control.Resolve(g.control) != flat.mode {
		panic("codegen: callback transport disagrees with contract")
	}
	var args []goast.Expr
	for _, ev := range last.EvidenceArgs {
		stack := g.evidence[ev.Key()]
		args = append(args, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Key()), flat.mode))
	}
	if last.Row != nil {
		args = append(args, g.rowArgument(last.Row))
	}
	for _, stage := range stages {
		args = append(args, g.expr(stage.Args[0], 0))
	}
	call := callExpr(ident(mangleValue(ref.Name)), args...)
	if flat.mode == types.Exit {
		g.markOutcomeCall(call.(*goast.CallExpr), g.goType(e.Ty))
	}
	return call, true
}
