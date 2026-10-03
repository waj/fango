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
	var fields []goast.Expr
	for _, mode := range []types.Transport{types.Direct, types.Exit} {
		if minimum > mode {
			continue
		}
		fields = append(fields, &goast.KeyValueExpr{Key: ident(memberName(mode)), Value: g.directLambdaMember(lam, mode)})
	}
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
