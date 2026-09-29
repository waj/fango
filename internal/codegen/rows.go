package codegen

import (
	"fmt"
	goast "go/ast"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func (g *gen) rowType() goast.Expr {
	g.usesFangort = true
	return &goast.StarExpr{X: selector("fangort", "EvidenceRow")}
}

func (g *gen) rowValue(id types.CaptureVar) goast.Expr {
	stack := g.rows[id]
	if len(stack) == 0 {
		panic("codegen: unavailable residual row")
	}
	return stack[len(stack)-1]
}

func (g *gen) pushRow(id types.CaptureVar, value goast.Expr) func() {
	if g.rows == nil {
		g.rows = map[types.CaptureVar][]goast.Expr{}
	}
	g.rows[id] = append(g.rows[id], value)
	return func() { g.rows[id] = g.rows[id][:len(g.rows[id])-1] }
}

func (g *gen) rowArgument(row *core.RowArgument) goast.Expr {
	var tail goast.Expr = ident("nil")
	if row == nil {
		return tail
	}
	if row.From != 0 {
		tail = g.rowValue(row.From)
	}
	if len(row.Effects) == 0 {
		return tail
	}
	args := []goast.Expr{tail}
	for _, ev := range row.Effects {
		stack := g.evidence[ev.Key()]
		if len(stack) == 0 {
			panic("codegen: missing residual evidence")
		}
		value, actual := stack[len(stack)-1], g.currentEvidenceMode(ev.Key())
		members := []goast.Expr{&goast.KeyValueExpr{Key: ident("Origin"), Value: &goast.SelectorExpr{X: value, Sel: ident("Origin")}}}
		effect := g.effects[ev.Unique]
		lossless := effect != nil && (len(effect.Ops) > 0 && effect.Ops[0].Abort)
		for _, mode := range []types.Transport{types.Direct, types.Exit} {
			if mode < actual && !lossless {
				continue
			}
			members = append(members, &goast.KeyValueExpr{Key: ident(memberName(mode)), Value: g.evidenceArg(ev, value, actual, mode)})
		}
		args = append(args, &goast.CompositeLit{Type: selector("fangort", "EvidenceBinding"), Elts: []goast.Expr{
			&goast.KeyValueExpr{Key: ident("Name"), Value: stringLit(ev.Name)},
			&goast.KeyValueExpr{Key: ident("Arguments"), Value: &goast.CompositeLit{Type: &goast.ArrayType{Elt: g.descriptorType()}, Elts: g.typeDescriptorArgs(ev.Args)}},
			&goast.KeyValueExpr{Key: ident("Family"), Value: &goast.CompositeLit{Type: selector("fangort", "EvidenceFamily"), Elts: members}},
		}})
	}
	g.usesFangort = true
	return callExpr(selector("fangort", "ExtendEvidenceRow"), args...)
}

func (g *gen) deferredEvidence(ev core.EffectInstance, row goast.Expr, mode types.Transport) goast.Expr {
	desired := ev
	desired.Control = types.Control{Transport: mode}
	effectType := g.effectTypeMode(desired, mode)
	lookupArgs := []goast.Expr{row, stringLit(ev.Name), selector("fangort", memberName(mode)+"Evidence")}
	lookupArgs = append(lookupArgs, g.typeDescriptorArgs(ev.Args)...)
	lookup := callExpr(indexExpr(selector("fangort", "RowEvidence"), []goast.Expr{effectType}), lookupArgs...)
	effect := g.effects[ev.Unique]
	if effect == nil {
		panic("codegen: deferred evidence has no declaration")
	}

	originArgs := append([]goast.Expr{row, stringLit(ev.Name)}, g.typeDescriptorArgs(ev.Args)...)
	fields := []goast.Expr{&goast.KeyValueExpr{Key: ident("Origin"), Value: callExpr(selector("fangort", "DeferredEvidenceOrigin"), originArgs...)}}
	if len(effect.Ops) > 0 && effect.Ops[0].Abort {
		resolver := funcLitParams(nil, &goast.StarExpr{X: selector("fangort", "ExitTarget")}, []goast.Stmt{returnStmt(&goast.SelectorExpr{X: lookup, Sel: ident("Target")})})
		fields = append(fields, &goast.KeyValueExpr{Key: ident("Target"), Value: callExpr(selector("fangort", "DeferredExitTarget"), resolver)})
	} else {
		sub := map[int]types.Type{}
		for i, param := range effect.Params {
			sub[param.ID] = ev.Args[i]
		}
		for _, op := range effect.Ops {
			if len(op.LocalVars) > 0 && op.Native == nil {
				request := ident("t_poly_request")
				invoke := callExpr(&goast.SelectorExpr{X: lookup, Sel: ident("Op_" + linkName(op.Name))}, request)
				result := goast.Expr(selector("fangort", "PolyReply"))
				if mode == types.Exit {
					result = indexExpr(selector("fangort", "Outcome"), []goast.Expr{result})
				}
				fields = append(fields, &goast.KeyValueExpr{Key: ident("Op_" + linkName(op.Name)), Value: funcLitParams(
					[]paramSpec{{name: "t_poly_request", typ: selector("fangort", "PolyRequest")}}, result,
					[]goast.Stmt{returnStmt(invoke)})})
				continue
			}
			var params []paramSpec
			var args []goast.Expr
			for i, raw := range op.RuntimeParamTypes() {
				ty := types.SubstRigid(raw, sub)
				if g.isUnit(ty) {
					continue
				}
				name := fmt.Sprintf("rowArg%d", i)
				params = append(params, paramSpec{name: name, typ: g.goType(ty)})
				args = append(args, ident(name))
			}
			resultTy := types.SubstRigid(op.ResultType, sub)
			var result goast.Expr = g.goType(resultTy)
			invoke := callExpr(&goast.SelectorExpr{X: lookup, Sel: ident("Op_" + linkName(op.Name))}, args...)
			body := []goast.Stmt{returnStmt(invoke)}

			if mode == types.Exit {
				result = g.outcomeType(resultTy)
			} else if g.isUnit(resultTy) {
				result, body = nil, []goast.Stmt{exprStmt(invoke)}
			}
			fields = append(fields, &goast.KeyValueExpr{Key: ident("Op_" + linkName(op.Name)), Value: funcLitParams(params, result, body)})
		}
	}
	return &goast.CompositeLit{Type: effectType, Elts: fields}
}

func (g *gen) bindDeferredEffects(effects []core.EffectInstance, row goast.Expr, mode types.Transport) func() {
	for _, ev := range effects {
		g.evidence[ev.Key()] = append(g.evidence[ev.Key()], g.deferredEvidence(ev, row, mode))
		g.evidenceModes[ev.Key()] = append(g.evidenceModes[ev.Key()], mode)
	}
	return func() {
		for _, ev := range effects {
			g.evidence[ev.Key()] = g.evidence[ev.Key()][:len(g.evidence[ev.Key()])-1]
			g.evidenceModes[ev.Key()] = g.evidenceModes[ev.Key()][:len(g.evidenceModes[ev.Key()])-1]
		}
	}
}
