package codegen

import (
	"fmt"
	goast "go/ast"
	"maps"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Unit applications used as statements can share the enclosing return/exit
// continuation. Invocation evidence is installed only while emitting the body;
// captured evidence and rows keep their current lexical bindings.
func (g *gen) immediateUnitApplication(e core.Expr, body func(core.Expr) []goast.Stmt) ([]goast.Stmt, bool) {
	app, ok := e.(*core.App)
	if !ok || app.CalleeKind != core.Value || len(app.Args) != 1 || !g.isUnit(app.Type()) {
		return nil, false
	}
	lam, ok := app.Callee.(*core.Lambda)
	if !ok || !g.isUnit(lam.Ty.(*types.TFun).Arg) {
		return nil, false
	}
	mode := app.Control.Resolve(g.control)
	labels := types.SortedRow(lam.Ty.(*types.TFun).Eff).Labels
	var keys []types.EffectKey
	var arguments []goast.Expr
	var stmts []goast.Stmt
	for _, ev := range app.EvidenceArgs {
		stack := g.evidence[ev.Key()]
		argument := g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Key()), mode)
		if _, simple := argument.(*goast.Ident); !simple {
			name := fmt.Sprintf("t_invokeEvidence%d", g.tmp)
			g.tmp++
			instance := ev
			instance.Control = types.Control{Transport: mode}
			stmts = append(stmts, varDeclStmt(name, g.effectTypeMode(instance, mode), argument), assignBlank(ident(name)))
			argument = ident(name)
		}
		arguments = append(arguments, argument)
	}
	var row goast.Expr
	if app.Row != nil {
		row = g.rowArgument(app.Row)
		if _, simple := row.(*goast.Ident); !simple {
			name := fmt.Sprintf("t_invokeRow%d", g.tmp)
			g.tmp++
			stmts = append(stmts, varDeclStmt(name, g.rowType(), row), assignBlank(ident(name)))
			row = ident(name)
		}
	}
	// Evaluate the erased argument before executing the lambda body.
	stmts = append(stmts, g.stmts(app.Args[0])...)
	oldForwarders := g.forwarders
	g.forwarders = maps.Clone(oldForwarders)
	defer func() { g.forwarders = oldForwarders }()
	index := 0
	for _, label := range labels {
		if !types.RuntimeEvidenceEffect(label) {
			continue
		}
		key := types.EffectLabelKey(label)
		keys = append(keys, key)
		g.evidence[key] = append(g.evidence[key], arguments[index])
		g.evidenceModes[key] = append(g.evidenceModes[key], mode)
		index++
	}
	defer func() {
		for _, key := range keys {
			g.evidence[key] = g.evidence[key][:len(g.evidence[key])-1]
			g.evidenceModes[key] = g.evidenceModes[key][:len(g.evidenceModes[key])-1]
		}
	}()
	if lam.RowParam != 0 {
		defer g.pushRow(lam.RowParam, row)()
		defer g.bindDeferredEffects(lam.RowEffects, row, mode)()
	}
	stmts = append(stmts, body(lam.Body)...)
	return []goast.Stmt{&goast.BlockStmt{List: stmts}}, true
}
