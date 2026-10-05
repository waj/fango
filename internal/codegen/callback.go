package codegen

import (
	"fmt"
	goast "go/ast"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func memberName(mode types.Transport) string {
	switch mode {
	case types.Exit:
		return "Exit"

	default:
		return "Direct"
	}
}
func callbackMember(value goast.Expr, mode types.Transport) goast.Expr {
	if _, literal := value.(*goast.CompositeLit); literal {
		value = &goast.ParenExpr{X: value}
	}
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
	}

	return &goast.FuncType{Params: paramFields(params), Results: &goast.FieldList{List: []*goast.Field{{Type: result}}}}
}
func (g *gen) callbackType(fn *types.TFun) goast.Expr {
	fields := []*goast.Field{}
	for _, mode := range []types.Transport{types.Direct, types.Exit} {
		fields = append(fields, &goast.Field{Names: []*goast.Ident{ident(memberName(mode))}, Type: g.callbackMemberType(fn, mode)})
	}
	g.usesFangort = true
	return g.callableAlias(&goast.StructType{Fields: &goast.FieldList{List: fields}})
}
func (g *gen) callbackValue(lam *core.Lambda) goast.Expr {
	fn := lam.Ty.(*types.TFun)
	minimum := g.callbackMinimum(lam)
	if !g.disableOptimizations && minimum == types.Direct && g.shareableDirectCallback(lam) {
		return g.sharedDirectCallback(lam)
	}
	var fields []goast.Expr
	for _, mode := range []types.Transport{types.Direct, types.Exit} {
		if minimum > mode {
			continue
		}
		fields = append(fields, &goast.KeyValueExpr{Key: ident(memberName(mode)), Value: g.directLambdaMember(lam, mode)})
	}
	return &goast.CompositeLit{Type: g.callbackType(fn), Elts: fields}
}

type sharedCallbackKey struct {
	lambda *core.Lambda
	abi    types.Transport
}

// A closed, fixed-Direct body can serve both callable members, including
// copies of this lambda inside an enclosing transport family. Keep lexical
// captures and generic environments at their construction sites. The local
// reference check is conservative: internal binders other than the argument
// also keep the ordinary closure path.
func (g *gen) shareableDirectCallback(lam *core.Lambda) bool {
	control := core.ExprControl(lam.Body)
	if control.Polymorphic || control.Transport != types.Direct || len(lam.RowEffects) != 0 || len(g.tyParamNames) != 0 ||
		len(core.FreeEvidence(lam)) != 0 || len(core.FreeRows(lam)) != 0 {
		return false
	}
	for _, label := range types.SortedRow(lam.Ty.(*types.TFun).Eff).Labels {
		if types.RuntimeEvidenceEffect(label) {
			return false
		}
	}
	closed := true
	core.Inspect(lam.Body, func(e core.Expr) {
		// Lifted local-worker adapters also carry lexical references without
		// Local set. As in expr, only a known definition denotes a global.
		if ref, ok := e.(*core.VarRef); ok && (ref.Local || g.defs[ref.Name] == nil) && ref.Name != lam.Param {
			closed = false
		}
	})
	return closed
}

func (g *gen) sharedDirectCallback(lam *core.Lambda) goast.Expr {
	key := sharedCallbackKey{lambda: lam, abi: g.abi}
	if g.sharedCallbacks == nil {
		g.sharedCallbacks = map[sharedCallbackKey]string{}
	}
	name := g.sharedCallbacks[key]
	if name == "" {
		name = fmt.Sprintf("callbackBody%d", len(g.sharedCallbacks))
		g.sharedCallbacks[key] = name
		body := g.directLambdaMember(lam, types.Direct).(*goast.FuncLit)
		g.sharedCallbackDecls = append(g.sharedCallbackDecls, &goast.FuncDecl{Name: ident(name), Type: body.Type, Body: body.Body})
	}
	fn := lam.Ty.(*types.TFun)
	var params []paramSpec
	var args []goast.Expr
	if lam.RowParam != 0 {
		params = append(params, paramSpec{name: "row", typ: g.rowType()})
		args = append(args, ident("row"))
	}
	params = append(params, paramSpec{name: "value", typ: g.goType(fn.Arg)})
	args = append(args, ident("value"))
	exit := funcLitParams(params, g.outcomeType(fn.Ret), []goast.Stmt{
		returnStmt(g.normalOutcome(fn.Ret, callExpr(ident(name), args...))),
	})
	return &goast.CompositeLit{Type: g.callbackType(fn), Elts: []goast.Expr{
		&goast.KeyValueExpr{Key: ident("Direct"), Value: ident(name)},
		&goast.KeyValueExpr{Key: ident("Exit"), Value: exit},
	}}
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
		if effect := g.effects[ev.Unique]; effect != nil && (len(effect.Ops) > 0 && effect.Ops[0].Abort) {
			return
		}
		if mode := g.currentEvidenceMode(ev.Key()); mode > minimum {
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
				for _, ev := range e.Effects {
					bound[ev.Unique]++
				}
				visit(e.Body)
				for _, ev := range e.Effects {
					bound[ev.Unique]--
				}
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
