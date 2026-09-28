package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"
	"sort"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func (g *gen) evidenceFamily(ev core.EffectInstance, value goast.Expr, actual types.Transport) goast.Expr {
	fields := []goast.Expr{&goast.KeyValueExpr{Key: ident("Origin"), Value: &goast.SelectorExpr{X: value, Sel: ident("Origin")}}}
	effect := g.effects[ev.Unique]
	abort := effect != nil && len(effect.Ops) > 0 && effect.Ops[0].Abort
	for _, mode := range []types.Transport{types.Direct, types.Exit} {
		if mode < actual && !abort {
			continue
		}
		fields = append(fields, &goast.KeyValueExpr{Key: ident(memberName(mode)), Value: g.evidenceArg(ev, value, actual, mode)})
	}
	return &goast.CompositeLit{Type: selector("fangort", "EvidenceFamily"), Elts: fields}
}

// The factory is recursive only as a Go value: each call builds one activation
// view, and its Rebuild closure calls the same factory with rebased dependencies.
// State and immutable lexical values stay captured by the factory itself.
func (g *gen) forkableHandlerEvidence(e *core.Handle, mode types.Transport, state *handlerState) (goast.Expr, goast.Expr) {
	free := map[int]core.EffectInstance{}
	rows := map[types.CaptureVar]bool{}
	for _, clause := range e.Clauses {
		for id, ev := range core.FreeEvidence(clause.Body) {
			free[id] = ev
		}
		for id := range core.FreeRows(clause.Body) {
			rows[id] = true
		}
	}
	ids := make([]int, 0, len(free))
	for id := range free {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return free[ids[i]].Name < free[ids[j]].Name })
	rowIDs := make([]types.CaptureVar, 0, len(rows))
	for id := range rows {
		rowIDs = append(rowIDs, id)
	}
	sort.Slice(rowIDs, func(i, j int) bool { return rowIDs[i] < rowIDs[j] })
	serial := g.tmp
	g.tmp++
	builder := fmt.Sprintf("t_buildEvidence%d", serial)
	origin := fmt.Sprintf("t_origin%d", serial)
	result := fmt.Sprintf("t_evidence%d", serial)
	forked := fmt.Sprintf("t_forked%d", serial)
	fork := fmt.Sprintf("t_fork%d", serial)
	var params []paramSpec
	var originalArgs, forkArgs []goast.Expr
	for i, id := range ids {
		ev := free[id]
		actual := g.currentEvidenceMode(id)
		ev.Control = types.Control{Transport: actual}
		typ := g.effectTypeMode(ev, actual)
		name := fmt.Sprintf("t_parentEvidence%d_%d", serial, i)
		stack := g.evidence[id]
		if len(stack) == 0 {
			panic("codegen: missing captured handler evidence")
		}
		params = append(params, paramSpec{name: name, typ: typ})
		originalArgs = append(originalArgs, stack[len(stack)-1])
		forkArgs = append(forkArgs, callExpr(indexExpr(selector("fangort", "ForkEvidence"), []goast.Expr{typ}), ident(fork), selector(name, "Origin"), selector("fangort", memberName(actual)+"Evidence")))
		g.evidence[id] = append(g.evidence[id], ident(name))
		g.evidenceModes[id] = append(g.evidenceModes[id], actual)
	}
	var restoreRows []func()
	for i, id := range rowIDs {
		name := fmt.Sprintf("t_parentRow%d_%d", serial, i)
		params = append(params, paramSpec{name: name, typ: g.rowType()})
		originalArgs = append(originalArgs, g.rowValue(id))
		forkArgs = append(forkArgs, callExpr(selector(fork, "Row"), ident(name)))
		restoreRows = append(restoreRows, g.pushRow(id, ident(name)))
	}
	st, record := g.handlerEvidence(e, mode, state)
	for _, restore := range restoreRows {
		restore()
	}
	for _, id := range ids {
		g.evidence[id] = g.evidence[id][:len(g.evidence[id])-1]
		g.evidenceModes[id] = g.evidenceModes[id][:len(g.evidenceModes[id])-1]
	}
	literal := record.(*goast.CompositeLit)
	literal.Elts = append(literal.Elts, &goast.KeyValueExpr{Key: ident("Origin"), Value: ident(origin)})
	originType := &goast.StarExpr{X: selector("fangort", "EvidenceOrigin")}
	originValue := g.evidenceOrigin(e.Effect)
	rebuild := funcLitParams([]paramSpec{{name: fork, typ: &goast.StarExpr{X: selector("fangort", "EvidenceFork")}}}, selector("fangort", "EvidenceFamily"), []goast.Stmt{
		varDeclStmt(forked, st, callExpr(ident(builder), forkArgs...)),
		returnStmt(g.evidenceFamily(e.Effect, ident(forked), mode)),
	})
	factory := funcLitParams(params, st, []goast.Stmt{
		varDeclStmt(origin, originType, originValue),
		varDeclStmt(result, st, record),
		&goast.AssignStmt{Lhs: []goast.Expr{selector(origin, "Rebuild")}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{rebuild}},
		returnStmt(ident(result)),
	}).(*goast.FuncLit)
	g.usesFangort = true
	return st, callExpr(funcLit(st, []goast.Stmt{
		varDeclStmt(builder, factory.Type, ident("nil")),
		assignStmt(builder, factory),
		returnStmt(callExpr(ident(builder), originalArgs...)),
	}))
}

func (g *gen) evidenceOrigin(ev core.EffectInstance) goast.Expr {
	return &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: selector("fangort", "EvidenceOrigin"), Elts: []goast.Expr{
		&goast.KeyValueExpr{Key: ident("Name"), Value: stringLit(ev.Name)},
		&goast.KeyValueExpr{Key: ident("Arguments"), Value: &goast.CompositeLit{Type: &goast.ArrayType{Elt: g.descriptorType()}, Elts: g.typeDescriptorArgs(ev.Args)}},
	}}}
}
