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
		stack := g.evidence[ev.Unique]
		if len(stack) == 0 {
			panic("codegen: missing residual evidence")
		}
		value, actual := stack[len(stack)-1], g.currentEvidenceMode(ev.Unique)
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
	lookup := callExpr(indexExpr(selector("fangort", "RowEvidence"), []goast.Expr{effectType}), row, stringLit(ev.Name), selector("fangort", memberName(mode)+"Evidence"))
	effect := g.effects[ev.Unique]
	if effect == nil {
		panic("codegen: deferred evidence has no declaration")
	}

	fields := []goast.Expr{&goast.KeyValueExpr{Key: ident("Origin"), Value: callExpr(selector("fangort", "DeferredEvidenceOrigin"), row, stringLit(ev.Name))}}
	if len(effect.Ops) > 0 && effect.Ops[0].Abort {
		resolver := funcLitParams(nil, &goast.StarExpr{X: selector("fangort", "ExitTarget")}, []goast.Stmt{returnStmt(&goast.SelectorExpr{X: lookup, Sel: ident("Target")})})
		fields = append(fields, &goast.KeyValueExpr{Key: ident("Target"), Value: callExpr(selector("fangort", "DeferredExitTarget"), resolver)})
	} else {
		sub := map[int]types.Type{}
		for i, param := range effect.Params {
			sub[param.ID] = ev.Args[i]
		}
		for _, op := range effect.Ops {
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
		g.evidence[ev.Unique] = append(g.evidence[ev.Unique], g.deferredEvidence(ev, row, mode))
		g.evidenceModes[ev.Unique] = append(g.evidenceModes[ev.Unique], mode)
	}
	return func() {
		for _, ev := range effects {
			g.evidence[ev.Unique] = g.evidence[ev.Unique][:len(g.evidence[ev.Unique])-1]
			g.evidenceModes[ev.Unique] = g.evidenceModes[ev.Unique][:len(g.evidenceModes[ev.Unique])-1]
		}
	}
}
