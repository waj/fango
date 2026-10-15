package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

// Loops and higher-order workers retain their selected transport so their
// repeated calls do not pay for a compatibility wrapper on every iteration.
// Tiny bodies also stay specialized: a bridge would be larger than the calls
// it replaces, and would add an evidence adapter to small operation forwarders.
func (g *gen) shareWorkerBody(d *core.Def) bool {
	if g.disableOptimizations || !d.Control.Polymorphic || d.Control.Transport != types.Direct || g.lowered.Functions[d.Name].Loop {
		return false
	}
	args, _ := core.PeelFun(d.Type, len(d.Params))
	for _, arg := range args {
		if _, function := arg.(*types.TFun); function {
			return false
		}
	}
	nodes := 0
	core.InspectPruned(d.Body, func(core.Expr) bool {
		nodes++
		return nodes <= 8
	})
	if nodes <= 8 {
		return false
	}
	return true
}

// Keep the exported ABI while sharing a worker body. A Direct caller proves
// normal completion; its entry delegates to the sole Exit implementation.
func (g *gen) sharedWorkerBridge(d *core.Def, argTypes []types.Type, ret types.Type, params []paramSpec) []goast.Stmt {
	var args, typeArgs []goast.Expr
	index := 0
	next := func() goast.Expr {
		if params[index].name == "_" {
			params[index].name = fmt.Sprintf("bridgeUnused%d", index)
		}
		value := ident(params[index].name)
		index++
		return value
	}
	for _, param := range d.TyParams {
		typeArgs = append(typeArgs, g.goType(param))
		args = append(args, next())
	}
	for _, ev := range d.EffectParams {
		args = append(args, g.evidenceArg(ev, next(), types.Direct, types.Exit))
	}
	if d.RowParam != 0 {
		args = append(args, next())
	}
	for _, t := range argTypes {
		if g.isUnit(t) {
			continue
		}
		value := next()
		args = append(args, value)
	}
	call := callExpr(indexExpr(g.topValueRefMode(d.Name, types.Exit), typeArgs), args...).(*goast.CallExpr)
	g.markWorkerOutcomeCall(call, g.goType(ret), d)
	normal := callExpr(selector("fangort", "RequireNormal"), call)
	if g.isUnit(ret) {
		return []goast.Stmt{assignBlank(normal), bareReturnStmt()}
	}
	return []goast.Stmt{returnStmt(normal)}
}

// Canonical Exit workers can install handlers which were Direct in the old
// worker. Selected Direct callbacks still need a view of those activations.
// Its operations check the caller's normal-completion proof, just like the
// Direct worker wrapper; they never translate or swallow a language exit.
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
