package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

// An abort-capable effect installed in an Exit context still has Direct
// callbacks selected under it, which need a Direct view of the activation. Its
// operations check the caller's normal-completion proof; they never translate
// or swallow a language exit.
func (g *gen) checkedDirectEvidence(ev core.EffectInstance, value goast.Expr) goast.Expr {
	eff := g.effects[ev.Unique]
	sub := map[int]types.Type{}
	for i, p := range eff.Params {
		sub[p.ID] = ev.Args[i]
	}
	ev.Control = types.Control{Transport: types.Direct}
	elts := []goast.Expr{&goast.KeyValueExpr{Key: ident("Origin"), Value: &goast.SelectorExpr{X: value, Sel: ident("Origin")}}}
	if eff.HasAbort() {
		elts = append(elts, &goast.KeyValueExpr{Key: ident("Target"), Value: &goast.SelectorExpr{X: value, Sel: ident("Target")}})
	}
	for _, op := range eff.Ops {
		if op.Abort {
			continue
		}
		var params []paramSpec
		var args []goast.Expr
		var result goast.Expr
		var unit bool
		if len(op.LocalVars) > 0 && op.Native == nil {
			params = []paramSpec{{"request", selector("fangort", "PolyRequest")}}
			args = []goast.Expr{ident("request")}
			result = selector("fangort", "PolyReply")
		} else {
			for i, raw := range op.RuntimeParamTypes() {
				t := types.SubstRigid(raw, sub)
				if g.isUnit(t) {
					continue
				}
				n := fmt.Sprintf("argument%d", i)
				params = append(params, paramSpec{n, g.goType(t)})
				args = append(args, ident(n))
			}
			t := types.SubstRigid(op.ResultType, sub)
			unit, result = g.isUnit(t), g.goType(t)
		}
		call := callExpr(&goast.SelectorExpr{X: value, Sel: ident("Op_" + fangort.LinkName(op.Name))}, args...).(*goast.CallExpr)
		g.markOutcomeCall(call, result)
		normal := callExpr(selector("fangort", "RequireNormal"), call)
		body := []goast.Stmt{returnStmt(normal)}
		if unit {
			result = nil
			body = []goast.Stmt{assignBlank(normal)}
		}
		elts = append(elts, &goast.KeyValueExpr{Key: ident("Op_" + fangort.LinkName(op.Name)), Value: funcLitParams(params, result, body)})
	}
	return &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: g.effectTypeMode(ev, types.Direct).(*goast.StarExpr).X, Elts: elts}}
}
