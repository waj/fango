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
	return &goast.SelectorExpr{X: &goast.SelectorExpr{X: value, Sel: ident("Binding")}, Sel: ident("Family")}
}

// The factory is recursive only as a Go value: each call builds one activation
// view, and its Rebuild closure calls the same factory with rebased dependencies.
// State and immutable lexical values stay captured by the factory itself.
func (g *gen) forkableHandlerEvidence(e *core.Handle, label int, mode types.Transport, state *handlerState) (goast.Expr, goast.Expr) {
	inst := e.Effects[label]
	free := map[types.EffectKey]core.EffectInstance{}
	rows := map[types.CaptureVar]bool{}
	for _, clause := range e.Clauses {
		if clause.Effect != label {
			continue
		}
		for id, ev := range core.FreeEvidence(clause.Body) {
			free[id] = ev
		}
		for id := range core.FreeRows(clause.Body) {
			rows[id] = true
		}
	}
	ids := make([]types.EffectKey, 0, len(free))
	for id := range free {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if free[ids[i]].Name != free[ids[j]].Name {
			return free[ids[i]].Name < free[ids[j]].Name
		}
		return ids[i] < ids[j]
	})
	rowIDs := make([]types.CaptureVar, 0, len(rows))
	for id := range rows {
		rowIDs = append(rowIDs, id)
	}
	sort.Slice(rowIDs, func(i, j int) bool { return rowIDs[i] < rowIDs[j] })
	if len(ids) == 0 && len(rowIDs) == 0 {
		serial := g.tmp
		g.tmp++
		name := fmt.Sprintf("t_fixedEvidence%d", serial)
		typ, record := g.handlerEvidence(e, label, mode, state)
		literal := record.(*goast.UnaryExpr).X.(*goast.CompositeLit)
		operations := map[string]*goast.FuncLit{}
		slot := 0
		for _, clause := range e.Clauses {
			if clause.Effect != label {
				continue
			}
			count := 0
			core.Inspect(clause.Body, func(core.Expr) { count++ })
			if len(clause.LocalVars) == 0 && count <= 16 {
				field := literal.Elts[slot].(*goast.KeyValueExpr)
				operations[field.Key.(*goast.Ident).Name] = field.Value.(*goast.FuncLit)
			}
			slot++
		}
		if g.fixedOperations == nil {
			g.fixedOperations = map[activationLabel]map[string]*goast.FuncLit{}
		}
		g.fixedOperations[activationLabel{e, label}] = operations
		literal.Elts = append(literal.Elts, &goast.KeyValueExpr{Key: ident("Origin"), Value: g.evidenceOrigin(inst)})
		stmts := []goast.Stmt{
			varDeclStmt(name, typ, g.completeEvidence(inst, record, mode)),
			&goast.AssignStmt{Lhs: []goast.Expr{&goast.SelectorExpr{X: selector(name, "Origin"), Sel: ident("Fixed")}}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{selector(name, "Binding")}},
		}
		if state != nil {
			// A fixed activation shares its cell with tasks directly; the
			// parent publishes it through this hook before launching them.
			stmts = append(stmts, &goast.AssignStmt{Lhs: []goast.Expr{&goast.SelectorExpr{X: selector(name, "Origin"), Sel: ident("Share")}}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{selector(state.cell, "Share")}})
		}
		return typ, callExpr(funcLit(typ, append(stmts, returnStmt(ident(name)))))
	}
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
		forkArgs = append(forkArgs, callExpr(selector(fork, "Value"), ident(name)))
		restoreRows = append(restoreRows, g.pushRow(id, ident(name)))
	}
	st, record := g.handlerEvidence(e, label, mode, state)
	for _, restore := range restoreRows {
		restore()
	}
	for _, id := range ids {
		g.evidence[id] = g.evidence[id][:len(g.evidence[id])-1]
		g.evidenceModes[id] = g.evidenceModes[id][:len(g.evidenceModes[id])-1]
	}
	literal := record.(*goast.UnaryExpr).X.(*goast.CompositeLit)
	literal.Elts = append(literal.Elts, &goast.KeyValueExpr{Key: ident("Origin"), Value: ident(origin)})
	originType := &goast.StarExpr{X: selector("fangort", "EvidenceOrigin")}
	originValue := g.evidenceOrigin(inst)
	rebuild := funcLitParams([]paramSpec{{name: fork, typ: &goast.StarExpr{X: selector("fangort", "EvidenceFork")}}}, selector("fangort", "EvidenceFamily"), []goast.Stmt{
		varDeclStmt(forked, st, callExpr(ident(builder), forkArgs...)),
		returnStmt(g.evidenceFamily(inst, ident(forked), mode)),
	})
	// Publishing the activation publishes its own cell and, transitively,
	// every captured dependency; a rebuilt view's parameters name the forked
	// dependencies, which are already published.
	var shareBody []goast.Stmt
	if state != nil {
		shareBody = append(shareBody, exprStmt(callExpr(selector(state.cell, "Share"))))
	}
	for i := range ids {
		shareBody = append(shareBody, exprStmt(callExpr(selector("fangort", "ShareOrigin"), selector(fmt.Sprintf("t_parentEvidence%d_%d", serial, i), "Origin"))))
	}
	for i := range rowIDs {
		shareBody = append(shareBody, exprStmt(callExpr(selector("fangort", "ShareEvidenceValue"), ident(fmt.Sprintf("t_parentRow%d_%d", serial, i)))))
	}
	factory := funcLitParams(params, st, []goast.Stmt{
		varDeclStmt(origin, originType, originValue),
		varDeclStmt(result, st, g.completeEvidence(inst, record, mode)),
		&goast.AssignStmt{Lhs: []goast.Expr{selector(origin, "Rebuild")}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{rebuild}},
		&goast.AssignStmt{Lhs: []goast.Expr{selector(origin, "Share")}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{&goast.FuncLit{Type: &goast.FuncType{Params: &goast.FieldList{}}, Body: &goast.BlockStmt{List: shareBody}}}},
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

// completeEvidence initializes both typed transport views and their common
// immutable row binding. No operation runs during this construction.
func (g *gen) completeEvidence(ev core.EffectInstance, value goast.Expr, actual types.Transport) goast.Expr {
	serial := g.tmp
	g.tmp++
	name := fmt.Sprintf("t_family%d", serial)
	storage := fmt.Sprintf("t_familyStorage%d", serial)
	own := ident(name)
	desired := ev
	desired.Control = types.Control{Transport: actual}
	typ := g.effectTypeMode(desired, actual)
	effect := g.effects[ev.Unique]
	abort := effect != nil && len(effect.Ops) > 0 && effect.Ops[0].Abort
	// Keep the typed records and binding in one allocation. Interior pointers
	// retain the same immutable family identity without separate heap objects.
	fields := []*goast.Field{
		{Names: []*goast.Ident{ident("Own")}, Type: typ.(*goast.StarExpr).X},
		{Names: []*goast.Ident{ident("Binding")}, Type: selector("fangort", "EvidenceBinding")},
	}
	if actual == types.Direct || abort {
		other := types.Exit
		if actual == types.Exit {
			other = types.Direct
		}
		otherInstance := ev
		otherInstance.Control = types.Control{Transport: other}
		fields = append(fields, &goast.Field{Names: []*goast.Ident{ident("Other")}, Type: g.effectTypeMode(otherInstance, other).(*goast.StarExpr).X})
	}
	storageType := &goast.StructType{Fields: &goast.FieldList{List: fields}}
	literal := value.(*goast.UnaryExpr).X.(*goast.CompositeLit)
	body := []goast.Stmt{
		varDeclStmt(storage, &goast.StarExpr{X: storageType}, &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: storageType, Elts: []goast.Expr{
			&goast.KeyValueExpr{Key: ident("Own"), Value: literal},
		}}}),
		varDeclStmt(name, typ, &goast.UnaryExpr{Op: gotoken.AND, X: selector(storage, "Own")}),
	}
	var direct, exit goast.Expr = ident("nil"), own
	if actual == types.Direct || abort {
		other := types.Exit
		member, back := "Exit", "Direct"
		if actual == types.Exit {
			other = types.Direct
			member, back = "Direct", "Exit"
		}
		adapted := &goast.SelectorExpr{X: own, Sel: ident(member)}
		adapter := g.rawEvidenceAdapter(ev, own, actual, other).(*goast.UnaryExpr).X
		body = append(body,
			&goast.AssignStmt{Lhs: []goast.Expr{selector(storage, "Other")}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{adapter}},
			&goast.AssignStmt{Lhs: []goast.Expr{adapted}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{&goast.UnaryExpr{Op: gotoken.AND, X: selector(storage, "Other")}}},
			&goast.AssignStmt{Lhs: []goast.Expr{&goast.SelectorExpr{X: adapted, Sel: ident(back)}}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{own}})
		if actual == types.Direct {
			direct, exit = own, adapted
		} else {
			direct = adapted
		}
	}
	family := &goast.CompositeLit{Type: selector("fangort", "EvidenceFamily"), Elts: []goast.Expr{
		&goast.KeyValueExpr{Key: ident("Origin"), Value: &goast.SelectorExpr{X: own, Sel: ident("Origin")}},
		&goast.KeyValueExpr{Key: ident("Direct"), Value: direct}, &goast.KeyValueExpr{Key: ident("Exit"), Value: exit},
	}}
	binding := &goast.SelectorExpr{X: own, Sel: ident("Binding")}
	body = append(body,
		&goast.AssignStmt{Lhs: []goast.Expr{selector(storage, "Binding")}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{&goast.CompositeLit{Type: selector("fangort", "EvidenceBinding"), Elts: []goast.Expr{
			&goast.KeyValueExpr{Key: ident("Name"), Value: &goast.SelectorExpr{X: selector(name, "Origin"), Sel: ident("Name")}},
			&goast.KeyValueExpr{Key: ident("Arguments"), Value: &goast.SelectorExpr{X: selector(name, "Origin"), Sel: ident("Arguments")}},
			&goast.KeyValueExpr{Key: ident("Family"), Value: family},
		}}}},
		&goast.AssignStmt{Lhs: []goast.Expr{binding}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{&goast.UnaryExpr{Op: gotoken.AND, X: selector(storage, "Binding")}}})
	if actual == types.Direct || abort {
		other := "Exit"
		if actual == types.Exit {
			other = "Direct"
		}
		body = append(body, &goast.AssignStmt{Lhs: []goast.Expr{&goast.SelectorExpr{X: &goast.SelectorExpr{X: own, Sel: ident(other)}, Sel: ident("Binding")}}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{binding}})
	}
	return callExpr(funcLit(typ, append(body, returnStmt(own))))
}
