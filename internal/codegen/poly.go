package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"
	"maps"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func (g *gen) polymorphicPerformExpr(e *core.Perform, evidence goast.Expr) goast.Expr {
	owner := make(map[int]types.Type, len(e.Op.Owner.Params))
	for i, p := range e.Op.Owner.Params {
		owner[p.ID] = e.Effect.Args[i]
	}
	oldNames := g.tyParamNames
	g.tyParamNames = maps.Clone(oldNames)
	if g.tyParamNames == nil {
		g.tyParamNames = make(map[int]string)
	}
	defer func() { g.tyParamNames = oldNames }()
	for _, v := range e.Op.LocalVars {
		g.tyParamNames[v.ID] = "any"
	}
	values := make([]goast.Expr, len(e.Args))
	for i, a := range e.Args {
		actual := callExpr(g.goType(a.Type()), g.expr(a, 0))
		values[i] = g.polyConvert(actual, a.Type(), types.SubstRigid(e.Op.ParamTypes[i], owner))
	}
	localTypes := make([]goast.Expr, len(e.LocalTypes))
	for i, t := range e.LocalTypes {
		localTypes[i] = g.typeDescriptor(t)
	}
	request := &goast.CompositeLit{Type: selector("fangort", "PolyRequest"), Elts: []goast.Expr{
		&goast.KeyValueExpr{Key: ident("Types"), Value: &goast.CompositeLit{Type: &goast.ArrayType{Elt: g.descriptorType()}, Elts: localTypes}},
		&goast.KeyValueExpr{Key: ident("Args"), Value: &goast.CompositeLit{Type: &goast.ArrayType{Elt: ident("any")}, Elts: values}},
	}}
	call := callExpr(&goast.SelectorExpr{X: evidence, Sel: ident("Op_" + linkName(e.Op.Name))}, request)
	erasedResult := types.SubstRigid(e.Op.ResultType, owner)
	raw := goast.Expr(ident("t_poly_raw"))
	if _, isAny := erasedResult.(*types.TVar); !isAny {
		raw = &goast.TypeAssertExpr{X: raw, Type: g.goType(erasedResult)}
	}
	decoder := funcLitParams([]paramSpec{{name: "t_poly_raw", typ: ident("any")}}, g.goType(e.Ty),
		[]goast.Stmt{returnStmt(g.polyConvert(raw, erasedResult, e.Ty))})
	want := g.typeDescriptor(e.Ty)
	exit := e.Control.Resolve(g.control) == types.Exit
	directEvidence := g.currentEvidenceMode(e.Effect.Key()) != types.Exit
	decode := func(value goast.Expr) goast.Expr {
		return callExpr(indexExpr(selector("fangort", "DecodePoly"), []goast.Expr{g.goType(e.Ty)}), value, want, decoder)
	}
	switch {
	case exit && directEvidence:
		return g.normalOutcome(e.Ty, decode(call))
	case exit:
		return callExpr(indexExpr(selector("fangort", "DecodePolyOutcome"), []goast.Expr{g.goType(e.Ty)}), call, want, decoder)
	default:
		return decode(call)
	}
}

// polyConvert changes only the Go representation at an operation-local
// polymorphism boundary. Its source and target Fango types are checked by
// Core; a local variable is represented as Go any inside the clause.
func (g *gen) polyConvert(value goast.Expr, from, to types.Type) goast.Expr {
	if types.Equal(from, to) {
		return value
	}
	if target, variable := to.(*types.TVar); variable && g.tyParamNames[target.ID] == "any" {
		return value
	}
	if source, variable := from.(*types.TVar); variable && g.tyParamNames[source.ID] == "any" {
		return &goast.TypeAssertExpr{X: value, Type: g.goType(to)}
	}
	switch from := from.(type) {
	case *types.TCon:
		to, ok := to.(*types.TCon)
		if !ok || from.Unique != to.Unique || len(from.Args) != len(to.Args) {
			panic("codegen: polymorphic operation conversion changed nominal type")
		}
		adt := g.adts[from.Unique]
		if adt == nil {
			panic("codegen: polymorphic operation conversion has no nominal declaration")
		}
		switch adt.Repr {
		case types.ReprList:
			param := fmt.Sprintf("t_poly_item%d", g.tmp)
			g.tmp++
			mapper := funcLitParams([]paramSpec{{name: param, typ: g.goType(from.Args[0])}}, g.goType(to.Args[0]),
				[]goast.Stmt{returnStmt(g.polyConvert(ident(param), from.Args[0], to.Args[0]))})
			return callExpr(indexExpr(selector("fangort", "ListMap"), []goast.Expr{g.goType(from.Args[0]), g.goType(to.Args[0])}), mapper, value)
		case types.ReprADT:
			return g.polyConvertADT(value, from, to, adt)
		case types.ReprBytes, types.ReprNativeAny:
			return value
		default:
			panic("codegen: unsupported polymorphic operation nominal representation")
		}
	case *types.TFun:
		to, ok := to.(*types.TFun)
		if !ok {
			panic("codegen: polymorphic operation conversion changed callback shape")
		}
		return g.polyConvertCallback(value, from, to)
	default:
		panic("codegen: unsupported polymorphic operation conversion")
	}
}

func (g *gen) polyConvertCallback(value goast.Expr, from, to *types.TFun) goast.Expr {
	toLabels := types.SortedRow(to.Eff).Labels
	fromLabels := types.SortedRow(from.Eff).Labels
	if len(toLabels) != len(fromLabels) || len(to.Eff.Labels) != len(from.Eff.Labels) {
		panic("codegen: polymorphic callback changed effect labels")
	}
	paired := map[types.EffectKey]types.EffLabel{}
	for i, label := range to.Eff.Labels {
		other := from.Eff.Labels[i]
		if label.Unique != other.Unique || len(label.Args) != len(other.Args) {
			panic("codegen: polymorphic callback changed effect labels")
		}
		paired[types.EffectLabelKey(label)] = other
	}
	param := fmt.Sprintf("t_poly_callback%d", g.tmp)
	g.tmp++
	result := fmt.Sprintf("t_poly_adapted%d", g.tmp)
	g.tmp++
	callbackType := g.goType(to)
	stmts := []goast.Stmt{varDeclNoValue(result, callbackType)}
	for _, mode := range []types.Transport{types.Direct, types.Exit} {
		member := memberName(mode)
		arg := fmt.Sprintf("t_poly_arg%d", g.tmp)
		g.tmp++
		params := []paramSpec{}
		callArgs := []goast.Expr{}
		convertedEvidence := map[types.EffectKey]goast.Expr{}
		for _, label := range toLabels {
			if !types.RuntimeEvidenceEffect(label) {
				continue
			}
			fromLabel, ok := paired[types.EffectLabelKey(label)]
			if !ok {
				panic("codegen: polymorphic callback lost effect label")
			}
			name := fmt.Sprintf("t_poly_evidence%d", g.tmp)
			g.tmp++
			instance := core.EffectInstance{Unique: label.Unique, Name: label.Name, Args: label.Args, Control: types.Control{Transport: mode}}
			params = append(params, paramSpec{name: name, typ: g.effectTypeMode(instance, mode)})
			convertedEvidence[types.EffectLabelKey(fromLabel)] = g.polyConvertEvidence(ident(name), label, fromLabel, mode)
		}
		for _, label := range fromLabels {
			if !types.RuntimeEvidenceEffect(label) {
				continue
			}
			converted, ok := convertedEvidence[types.EffectLabelKey(label)]
			if !ok {
				panic("codegen: polymorphic callback lost converted effect")
			}
			callArgs = append(callArgs, converted)
		}
		if types.FunctionOpenRow(to) {
			rowName := fmt.Sprintf("t_poly_row%d", g.tmp)
			g.tmp++
			params = append(params, paramSpec{name: rowName, typ: g.rowType()})
			callArgs = append(callArgs, ident(rowName))
		}
		params = append(params, paramSpec{name: arg, typ: g.goType(to.Arg)})
		callArgs = append(callArgs, g.polyConvert(ident(arg), to.Arg, from.Arg))
		call := callExpr(&goast.SelectorExpr{X: ident(param), Sel: ident(member)}, callArgs...)
		var resultType goast.Expr = g.goType(to.Ret)
		var converted goast.Expr
		if mode == types.Exit {
			resultType = g.outcomeType(to.Ret)
			item := fmt.Sprintf("t_poly_result%d", g.tmp)
			g.tmp++
			mapper := funcLitParams([]paramSpec{{name: item, typ: g.goType(from.Ret)}}, g.goType(to.Ret),
				[]goast.Stmt{returnStmt(g.polyConvert(ident(item), from.Ret, to.Ret))})
			converted = callExpr(indexExpr(selector("fangort", "MapOutcome"), []goast.Expr{g.goType(from.Ret), g.goType(to.Ret)}), call, mapper)
		} else {
			converted = g.polyConvert(call, from.Ret, to.Ret)
		}
		wrapper := funcLitParams(params, resultType, []goast.Stmt{returnStmt(converted)})
		assign := &goast.AssignStmt{Lhs: []goast.Expr{&goast.SelectorExpr{X: ident(result), Sel: ident(member)}}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{wrapper}}
		stmts = append(stmts, &goast.IfStmt{Cond: &goast.BinaryExpr{X: &goast.SelectorExpr{X: ident(param), Sel: ident(member)}, Op: gotoken.NEQ, Y: ident("nil")},
			Body: &goast.BlockStmt{List: []goast.Stmt{assign}}})
	}
	stmts = append(stmts, returnStmt(ident(result)))
	return callExpr(funcLitParams([]paramSpec{{name: param, typ: g.goType(from)}}, callbackType, stmts), value)
}

func (g *gen) polyConvertEvidence(value goast.Expr, from, to types.EffLabel, mode types.Transport) goast.Expr {
	if from.Unique != to.Unique {
		panic("codegen: polymorphic callback evidence changed effect declaration")
	}
	equal := len(from.Args) == len(to.Args)
	for i := range from.Args {
		equal = equal && types.Equal(from.Args[i], to.Args[i])
	}
	if equal {
		return value
	}
	eff := g.effects[from.Unique]
	if eff == nil {
		panic("codegen: polymorphic callback evidence has no declaration")
	}
	fromInstance := core.EffectInstance{Unique: from.Unique, Name: from.Name, Args: from.Args, Control: types.Control{Transport: mode}}
	toInstance := core.EffectInstance{Unique: to.Unique, Name: to.Name, Args: to.Args, Control: types.Control{Transport: mode}}
	param := fmt.Sprintf("t_poly_effect%d", g.tmp)
	g.tmp++
	fields := []goast.Expr{&goast.KeyValueExpr{Key: ident("Origin"), Value: &goast.SelectorExpr{X: ident(param), Sel: ident("Origin")}}}
	if len(eff.Ops) > 0 && eff.Ops[0].Abort {
		fields = append(fields, &goast.KeyValueExpr{Key: ident("Target"), Value: &goast.SelectorExpr{X: ident(param), Sel: ident("Target")}})
	} else {
		fromSub, toSub := make(map[int]types.Type), make(map[int]types.Type)
		for i, p := range eff.Params {
			fromSub[p.ID], toSub[p.ID] = from.Args[i], to.Args[i]
		}
		for _, op := range eff.Ops {
			field := "Op_" + linkName(op.Name)
			if len(op.LocalVars) > 0 && op.Native == nil {
				request := fmt.Sprintf("t_poly_request%d", g.tmp)
				g.tmp++
				result := goast.Expr(selector("fangort", "PolyReply"))
				if mode == types.Exit {
					result = indexExpr(selector("fangort", "Outcome"), []goast.Expr{result})
				}
				call := callExpr(&goast.SelectorExpr{X: ident(param), Sel: ident(field)}, ident(request))
				fields = append(fields, &goast.KeyValueExpr{Key: ident(field), Value: funcLitParams(
					[]paramSpec{{name: request, typ: selector("fangort", "PolyRequest")}}, result, []goast.Stmt{returnStmt(call)})})
				continue
			}
			var params []paramSpec
			var args []goast.Expr
			for _, raw := range op.RuntimeParamTypes() {
				fromType, toType := types.SubstRigid(raw, fromSub), types.SubstRigid(raw, toSub)
				name := fmt.Sprintf("t_poly_evidence_arg%d", g.tmp)
				g.tmp++
				var incoming goast.Expr = g.unitValue()
				if !g.isUnit(toType) {
					params = append(params, paramSpec{name: name, typ: g.goType(toType)})
					incoming = ident(name)
				}
				if !g.isUnit(fromType) {
					args = append(args, g.polyConvert(incoming, toType, fromType))
				}
			}
			fromResult, toResult := types.SubstRigid(op.ResultType, fromSub), types.SubstRigid(op.ResultType, toSub)
			call := callExpr(&goast.SelectorExpr{X: ident(param), Sel: ident(field)}, args...)
			var body []goast.Stmt
			var result goast.Expr
			if mode == types.Exit {
				result = g.outcomeType(toResult)
				name := fmt.Sprintf("t_poly_result%d", g.tmp)
				g.tmp++
				mapper := funcLitParams([]paramSpec{{name: name, typ: g.goType(fromResult)}}, g.goType(toResult),
					[]goast.Stmt{returnStmt(g.polyConvert(ident(name), fromResult, toResult))})
				body = []goast.Stmt{returnStmt(callExpr(indexExpr(selector("fangort", "MapOutcome"),
					[]goast.Expr{g.goType(fromResult), g.goType(toResult)}), call, mapper))}
			} else if g.isUnit(fromResult) {
				body = append(body, exprStmt(call))
				if !g.isUnit(toResult) {
					result = g.goType(toResult)
					body = append(body, returnStmt(g.polyConvert(g.unitValue(), fromResult, toResult)))
				}
			} else if g.isUnit(toResult) {
				body = []goast.Stmt{exprStmt(g.polyConvert(call, fromResult, toResult))}
			} else {
				result = g.goType(toResult)
				body = []goast.Stmt{returnStmt(g.polyConvert(call, fromResult, toResult))}
			}
			fields = append(fields, &goast.KeyValueExpr{Key: ident(field), Value: funcLitParams(params, result, body)})
		}
	}
	converted := &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: g.effectTypeMode(toInstance, mode).(*goast.StarExpr).X, Elts: fields}}
	return callExpr(funcLitParams([]paramSpec{{name: param, typ: g.effectTypeMode(fromInstance, mode)}},
		g.effectTypeMode(toInstance, mode), []goast.Stmt{returnStmt(g.completeEvidence(toInstance, converted, mode))}), value)
}

func (g *gen) polyConvertADT(value goast.Expr, from, to *types.TCon, adt *types.ADTInfo) goast.Expr {
	if taggedADT(adt) {
		return g.convertTagged(value, from, to, adt)
	}
	if productADT(adt) {
		return g.convertProduct(value, from, to, adt)
	}
	param := fmt.Sprintf("t_poly_value%d", g.tmp)
	g.tmp++
	variant := fmt.Sprintf("t_poly_variant%d", g.tmp)
	g.tmp++
	var cases []goast.Stmt
	for _, ctor := range adt.Ctors {
		fromArgs := g.goTypes(runtimeADTArgs(adt, from.Args))
		toArgs := g.goTypes(runtimeADTArgs(adt, to.Args))
		fromCtor := &goast.StarExpr{X: indexExpr(g.ctorRef(ctor), fromArgs)}
		toCtor := indexExpr(g.ctorRef(ctor), toArgs)
		fromFields := adt.InstFields(ctor, from.Args)
		toFields := adt.InstFields(ctor, to.Args)
		fields := make([]goast.Expr, len(fromFields))
		for i := range fields {
			field := &goast.SelectorExpr{X: ident(variant), Sel: ident(fieldName(i))}
			fields[i] = g.polyConvert(field, fromFields[i], toFields[i])
		}
		converted := &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: toCtor, Elts: fields}}
		cases = append(cases, &goast.CaseClause{List: []goast.Expr{fromCtor}, Body: []goast.Stmt{returnStmt(converted)}})
	}
	zero := fmt.Sprintf("t_poly_zero%d", g.tmp)
	g.tmp++
	cases = append(cases, &goast.CaseClause{Body: []goast.Stmt{varDeclNoValue(zero, g.goType(to)), returnStmt(ident(zero))}})
	switchStmt := &goast.TypeSwitchStmt{Assign: &goast.AssignStmt{Lhs: []goast.Expr{ident(variant)}, Tok: gotoken.DEFINE,
		Rhs: []goast.Expr{&goast.TypeAssertExpr{X: ident(param), Type: nil}}}, Body: &goast.BlockStmt{List: cases}}
	fn := funcLitParams([]paramSpec{{name: param, typ: g.goType(from)}}, g.goType(to), []goast.Stmt{switchStmt})
	return callExpr(fn, value)
}
