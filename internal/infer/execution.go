package infer

import (
	"fmt"
	"maps"
	"sort"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// executionBuilder retains value and owner flow before row defaulting. It uses
// the same finite contract language as executable Core, so latent obligations
// cross a module boundary through its checked contract rather than its AST.
type executionBuilder struct {
	ck   *Checker
	sub  Subst
	next int
	defs map[string]*core.Def
	raw  bool
}

// Keep current inference variables as constraint destinations. Generalized
// dependency contracts are already zonked; their skolems are instantiated by
// the flow engine at calls.
func (b *executionBuilder) source(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	if !b.raw {
		return b.sub.Apply(t)
	}
	if v, ok := t.(*types.TVar); ok && v.Kind == types.RowVar {
		if resolved, ok := b.sub.walk(v).(*types.TVar); ok && resolved.Rigid {
			return resolved
		}
		return v
	}
	t = b.sub.walk(t)
	switch t := t.(type) {
	case *types.TFun:
		return &types.TFun{Arg: b.source(t.Arg), Eff: b.source(t.Eff).(types.Row), Ret: b.source(t.Ret), Control: t.Control, OpenRow: t.OpenRow}
	case *types.TCon:
		args := make([]types.Type, len(t.Args))
		for i, a := range t.Args {
			args[i] = b.source(a)
		}
		return &types.TCon{Unique: t.Unique, Name: t.Name, Args: args}
	case types.Row:
		labels := append([]types.EffLabel(nil), t.Labels...)
		for i, l := range labels {
			labels[i].Args = make([]types.Type, len(l.Args))
			for j, a := range l.Args {
				labels[i].Args[j] = b.source(a)
			}
		}
		return types.Row{Labels: labels, Tail: b.source(t.Tail)}
	default:
		return t
	}
}

func (b *executionBuilder) node(kind string, ty types.Type, children ...*types.CaptureFlow) *types.CaptureFlow {
	b.next++
	if ty != nil {
		ty = b.sub.Apply(ty)
	}
	return &types.CaptureFlow{ID: b.next, Kind: kind, Type: ty, Children: children}
}
func (b *executionBuilder) fresh() string { b.next++; return fmt.Sprintf("$flow%d", b.next) }
func (b *executionBuilder) typ(e ast.Expr) types.Type {
	if e == nil {
		return nil
	}
	if t := b.ck.ExprTypes[e]; t != nil {
		return b.sub.Apply(t)
	}
	return nil
}
func (b *executionBuilder) variable(name string, t types.Type) *types.CaptureFlow {
	n := b.node("var", t)
	n.Name = name
	return n
}
func (b *executionBuilder) bind(name string, rhs, body *types.CaptureFlow) *types.CaptureFlow {
	n := b.node("let", body.Type, rhs, body)
	n.Name = name
	return n
}
func (b *executionBuilder) row(t types.Type) *types.CaptureRow {
	r := &types.CaptureRow{From: 1}
	if fn, ok := t.(*types.TFun); ok {
		for _, l := range fn.Eff.Labels {
			if types.RuntimeEvidenceEffect(l) {
				r.Effects = append(r.Effects, l.Unique)
			}
		}
	}
	return r
}
func (b *executionBuilder) function(params []ast.Pattern, body ast.Expr, ty types.Type, local map[string]bool) *types.CaptureFlow {
	inner := maps.Clone(local)
	for _, p := range params {
		for _, name := range patternNames(p, nil) {
			inner[name] = true
		}
	}
	out := b.expr(body, inner)
	arrows := make([]*types.TFun, len(params))
	cur := ty
	for i := range params {
		if fn, ok := cur.(*types.TFun); ok {
			arrows[i] = fn
			cur = fn.Ret
		}
	}
	for i := len(params) - 1; i >= 0; i-- {
		name := b.fresh()
		var arg types.Type
		if arrows[i] != nil {
			arg = arrows[i].Arg
		}
		out = b.pattern(params[i], b.variable(name, arg), out)
		n := b.node("lambda", arrows[i], out)
		n.Name = name
		n.SourceType = arrows[i]
		n.RowParam = 1
		if arrows[i] != nil {
			for _, l := range arrows[i].Eff.Labels {
				if types.RuntimeEvidenceEffect(l) {
					n.Effects = append(n.Effects, l.Unique)
				}
			}
		}
		out = n
	}
	return out
}
func (b *executionBuilder) definition(info DeclInfo) core.Def {
	t := b.sub.Apply(info.Type)
	local := map[string]bool{}
	params := make([]string, len(info.Params))
	args := make([]*types.CaptureFlow, len(params))
	cur := t
	for i := range params {
		params[i] = b.fresh()
		var at types.Type
		if fn, ok := cur.(*types.TFun); ok {
			at = fn.Arg
			cur = fn.Ret
		}
		args[i] = b.variable(params[i], at)
	}
	eqs := info.Equations
	if len(eqs) == 0 {
		eqs = []ast.Equation{{Params: info.Params, Body: info.Body}}
	}
	body := b.node("choice", cur)
	for _, eq := range eqs {
		inner := maps.Clone(local)
		for _, p := range eq.Params {
			for _, name := range patternNames(p, nil) {
				inner[name] = true
			}
		}
		branch := b.expr(eq.Body, inner)
		for i := len(eq.Params) - 1; i >= 0; i-- {
			branch = b.pattern(eq.Params[i], args[i], branch)
		}
		body.Children = append(body.Children, branch)
	}
	c := &types.CaptureContract{SourceType: b.source(info.Type), Params: params, Body: body, RowParam: 1}
	if len(params) > 0 {
		fn := t
		for range params {
			if a, ok := fn.(*types.TFun); ok {
				for _, l := range a.Eff.Labels {
					if types.RuntimeEvidenceEffect(l) {
						c.Effects = append(c.Effects, l.Unique)
					}
				}
				fn = a.Ret
			}
		}
	}
	return core.Def{Name: info.Name, Type: t, SourceType: t, Params: params, Control: core.ArrowControl(t, len(params)), CaptureContract: c}
}
func (b *executionBuilder) pattern(p ast.Pattern, value, body *types.CaptureFlow) *types.CaptureFlow {
	switch p := p.(type) {
	case *ast.PVar:
		return b.bind(p.Name, value, body)
	case *ast.PCtor:
		ctor := b.ck.Ctors[p.Name]
		if ctor == nil {
			return body
		}
		return b.match(ctor, p.Args, value, body)
	case *ast.PRecord:
		adt := b.ck.RecordPatternUses[p]
		if adt == nil || len(adt.Ctors) == 0 {
			return body
		}
		fields := make([]ast.Pattern, len(adt.RecordFields))
		for _, f := range p.Fields {
			if index, _ := adt.RecordField(f.Name); index >= 0 {
				fields[index] = f.Pattern
			}
		}
		return b.match(adt.Ctors[0], fields, value, body)
	}
	return b.node("seq", nil, value, body)
}
func (b *executionBuilder) match(ctor *types.CtorInfo, ps []ast.Pattern, value, body *types.CaptureFlow) *types.CaptureFlow {
	names := make([]string, len(ctor.Fields))
	for i := range names {
		names[i] = b.fresh()
	}
	for i := len(ps) - 1; i >= 0; i-- {
		if ps[i] != nil {
			body = b.pattern(ps[i], b.variable(names[i], nil), body)
		}
	}
	name := b.fresh()
	sw := b.node("switch", body.Type)
	sw.Name = name
	sw.Clauses = []types.CaptureClause{{Index: ctor.Index, Names: names, Body: body}}
	n := b.node("case", body.Type, value, sw)
	n.Name = name
	return n
}
func (b *executionBuilder) call(e *ast.App, local map[string]bool) *types.CaptureFlow {
	args := appArgs(e)
	head := appHead(e)
	inputs := make([]*types.CaptureFlow, len(args))
	for i, a := range args {
		inputs[i] = b.expr(a, local)
	}
	ht := b.source(b.ck.ExprTypes[head])
	if op := b.ck.OpCalls[e]; op != nil && len(args) == op.Arity {
		kind := "perform"
		if op.Abort {
			kind = "exit"
		}
		n := b.node(kind, b.typ(e), inputs...)
		n.Effects = []int{op.Owner.Unique}
		n.Index = op.Index
		n.Borrow = op.BorrowsEvidence
		n.Retain = op.RetainsArguments
		return n
	}
	if _, ok := head.(*ast.Resume); ok {
		n := b.node("resume", b.typ(e), inputs...)
		return n
	}
	if c, ok := head.(*ast.Ctor); ok {
		if ctor := b.ck.Ctors[c.Name]; ctor != nil {
			return b.saturated("ctor", nil, inputs, len(ctor.Fields), ctor.Index, ht, b.typ(e))
		}
	}
	callee := b.expr(head, local)
	arity := 1
	if v, ok := head.(*ast.Var); ok && !local[v.Name] {
		if method := b.ck.Methods[v.Name]; method != nil {
			binding := map[int]types.Type{}
			matchHead(method.Type, b.sub.Apply(ht), binding)
			if at := binding[method.Class.Param.ID]; at != nil {
				dict := b.dictionary(types.Pred{Class: method.Class.Name, Ty: at}, map[string]bool{})
				names := make([]string, len(method.Class.Methods))
				for i := range names {
					names[i] = b.fresh()
				}
				name := b.fresh()
				sw := b.node("switch", ht)
				sw.Name = name
				sw.Clauses = []types.CaptureClause{{Index: 0, Names: names, Body: b.variable(names[method.Index], ht)}}
				callee = b.node("case", ht, dict, sw)
				callee.Name = name
			}
		}
		if d := b.defs[v.Name]; d != nil {
			arity = len(d.Params)
			if arity == 0 {
				arity = 1
			}
			if sch, ok := b.ck.Env.Lookup(v.Name); ok && len(sch.Preds) > 0 && len(d.Params) > b.ck.Workers[v.Name] {
				binding := map[int]types.Type{}
				matchHead(sch.Body, b.sub.Apply(ht), binding)
				var dicts []*types.CaptureFlow
				var dictTypes []types.Type
				for _, pred := range sch.Preds {
					pred.Ty = types.SubstRigid(pred.Ty, binding)
					dicts = append(dicts, b.dictionary(pred, map[string]bool{}))
					dictTypes = append(dictTypes, b.ck.Classes[pred.Class].DictType(pred.Ty))
				}
				inputs = append(dicts, inputs...)
				for i := len(dictTypes) - 1; i >= 0; i-- {
					ht = &types.TFun{Arg: dictTypes[i], Ret: ht}
				}
			}
		}
	}
	return b.saturated("call", callee, inputs, arity, 0, ht, b.typ(e))
}

func (b *executionBuilder) dictionary(pred types.Pred, seen map[string]bool) *types.CaptureFlow {
	key := pred.Class + types.Show(pred.Ty)
	if seen[key] {
		return b.node("scalar", nil)
	}
	seen[key] = true
	defer delete(seen, key)
	ck := *b.ck
	ck.Sub = b.sub
	resolution := ck.ResolveInstance(pred, ck.CurrentOwner, ck.instanceLimit(), nil)
	if inst := resolution.Instance; inst != nil {
		if d := b.defs[inst.Name]; d != nil {
			raw := types.SubstRigid(d.SourceType, resolution.Bindings)
			global := b.node("global", raw)
			global.Name = inst.Name
			var args []*types.CaptureFlow
			for _, p := range inst.Preds {
				p.Ty = types.SubstRigid(p.Ty, resolution.Bindings)
				args = append(args, b.dictionary(p, seen))
			}
			n := b.node("call", inst.Class.DictType(pred.Ty), append([]*types.CaptureFlow{global}, args...)...)
			n.SourceType = raw
			return n
		}
	}
	// Unknown evidence remains an abstract value until the caller supplies its
	// dictionary. It is not an empty record or proof of an empty budget.
	n := b.node("call", nil, b.node("global", nil))
	n.Children[0].Name = "$unknown-dictionary"
	return n
}
func (b *executionBuilder) saturated(kind string, callee *types.CaptureFlow, args []*types.CaptureFlow, arity, index int, raw, result types.Type) *types.CaptureFlow {
	var missing []string
	var arrows []*types.TFun
	cur := raw
	for i := 0; i < arity; i++ {
		fn, ok := cur.(*types.TFun)
		if !ok {
			break
		}
		arrows = append(arrows, fn)
		cur = fn.Ret
	}
	actual := append([]*types.CaptureFlow(nil), args...)
	var strictNames []string
	var strictValues []*types.CaptureFlow
	if len(args) < arity {
		if callee != nil {
			name := b.fresh()
			strictNames = append(strictNames, name)
			strictValues = append(strictValues, callee)
			callee = b.variable(name, callee.Type)
		}
		for i, value := range args {
			name := b.fresh()
			strictNames = append(strictNames, name)
			strictValues = append(strictValues, value)
			actual[i] = b.variable(name, value.Type)
		}
	}
	for len(actual) < arity {
		name := b.fresh()
		missing = append(missing, name)
		var t types.Type
		if len(actual) < len(arrows) {
			t = arrows[len(actual)].Arg
		}
		actual = append(actual, b.variable(name, t))
	}
	children := actual[:arity]
	if kind == "call" {
		children = append([]*types.CaptureFlow{callee}, children...)
	}
	n := b.node(kind, cur, children...)
	n.SourceType = raw
	n.Index = index
	if arity > 0 && arity <= len(arrows) {
		n.Row = b.row(arrows[arity-1])
	}
	for i := arity; i < len(actual); i++ {
		call := b.node("call", result, n, actual[i])
		call.SourceType = cur
		call.Row = b.row(cur)
		n = call
		if fn, ok := cur.(*types.TFun); ok {
			cur = fn.Ret
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		var t types.Type
		if len(args)+i < len(arrows) {
			t = arrows[len(args)+i]
		}
		l := b.node("lambda", t, n)
		l.Name = missing[i]
		l.SourceType = t
		l.RowParam = 1
		n = l
	}
	for i := len(strictNames) - 1; i >= 0; i-- {
		n = b.bind(strictNames[i], strictValues[i], n)
	}
	return n
}
func (b *executionBuilder) expr(e ast.Expr, local map[string]bool) *types.CaptureFlow {
	if e == nil {
		return b.node("scalar", nil)
	}
	if d := b.ck.Desugared[e]; d != nil {
		return b.expr(d, local)
	}
	ty := b.typ(e)
	var n *types.CaptureFlow
	switch e := e.(type) {
	case *ast.Var:
		kind := "global"
		if local[e.Name] {
			kind = "var"
		}
		n = b.node(kind, ty)
		n.Name = e.Name
		n.SourceType = ty
	case *ast.Ctor:
		c := b.ck.Ctors[e.Name]
		if c != nil {
			n = b.saturated("ctor", nil, nil, len(c.Fields), c.Index, ty, ty)
		}
	case *ast.App:
		n = b.call(e, local)
	case *ast.Lambda:
		n = b.function(e.Params, e.Body, b.source(b.ck.ExprTypes[e]), local)
	case *ast.If:
		n = b.node("branch", ty, b.expr(e.Cond, local), b.expr(e.Then, local), b.expr(e.Else, local))
	case *ast.Block:
		inner := maps.Clone(local)
		for _, bind := range e.Binds {
			inner[bind.Name] = true
			for _, name := range patternNames(bind.Pattern, nil) {
				inner[name] = true
			}
		}
		n = b.expr(e.Result, inner)
		items := e.Items
		if len(items) == 0 {
			for i := range e.Binds {
				items = append(items, ast.BlockItem{BindIndex: i})
			}
		}
		for i := len(items) - 1; i >= 0; i-- {
			item := items[i]
			if item.Expr != nil {
				n = b.node("seq", ty, b.expr(item.Expr, inner), n)
				continue
			}
			bind := &e.Binds[item.BindIndex]
			bt := b.ck.BindTypes[bind]
			if bt != nil {
				bt = b.source(bt)
			}
			var rhs *types.CaptureFlow
			if len(bind.Params) > 0 {
				eqs := bind.Equations
				if len(eqs) == 0 {
					eqs = []ast.Equation{{Params: bind.Params, Body: bind.Body}}
				}
				rhs = b.node("choice", bt)
				for _, eq := range eqs {
					rhs.Children = append(rhs.Children, b.function(eq.Params, eq.Body, bt, inner))
				}
			} else {
				rhs = b.expr(bind.Body, inner)
			}
			if bind.Pattern != nil {
				n = b.pattern(bind.Pattern, rhs, n)
			} else {
				n = b.bind(bind.Name, rhs, n)
				n.Rec = len(bind.Params) > 0
			}
		}
	case *ast.Case:
		value := b.expr(e.Scrutinee, local)
		name := b.fresh()
		n = b.node("choice", ty)
		for _, branch := range e.Branches {
			inner := maps.Clone(local)
			for _, name := range patternNames(branch.Pattern, nil) {
				inner[name] = true
			}
			n.Children = append(n.Children, b.pattern(branch.Pattern, b.variable(name, value.Type), b.expr(branch.Body, inner)))
		}
		n = b.bind(name, value, n)
	case *ast.RecordLit:
		if adt := b.ck.RecordUses[e]; adt != nil && len(adt.Ctors) > 0 {
			fields := make([]*types.CaptureFlow, len(adt.RecordFields))
			for _, f := range e.Fields {
				if i, _ := adt.RecordField(f.Name); i >= 0 {
					fields[i] = b.expr(f.Value, local)
				}
			}
			n = b.node("ctor", ty, fields...)
			n.Index = adt.Ctors[0].Index
		}
	case *ast.RecordGet:
		if adt := b.ck.RecordUses[e]; adt != nil && len(adt.Ctors) > 0 {
			names := make([]string, len(adt.RecordFields))
			for i := range names {
				names[i] = b.fresh()
			}
			if i, _ := adt.RecordField(e.Field); i >= 0 {
				name := b.fresh()
				sw := b.node("switch", ty)
				sw.Name = name
				sw.Clauses = []types.CaptureClause{{Index: adt.Ctors[0].Index, Names: names, Body: b.variable(names[i], ty)}}
				n = b.node("case", ty, b.expr(e.Record, local), sw)
				n.Name = name
			}
		}
	case *ast.RecordUpdate:
		if adt := b.ck.RecordUses[e]; adt != nil && len(adt.Ctors) > 0 {
			names := make([]string, len(adt.RecordFields))
			fields := make([]*types.CaptureFlow, len(names))
			for i := range names {
				names[i] = b.fresh()
				fields[i] = b.variable(names[i], nil)
			}
			for _, field := range e.Fields {
				if i, _ := adt.RecordField(field.Name); i >= 0 {
					fields[i] = b.expr(field.Value, local)
				}
			}
			body := b.node("ctor", ty, fields...)
			body.Index = adt.Ctors[0].Index
			name := b.fresh()
			sw := b.node("switch", ty)
			sw.Name = name
			sw.Clauses = []types.CaptureClause{{Index: adt.Ctors[0].Index, Names: names, Body: body}}
			n = b.node("case", ty, b.expr(e.Record, local), sw)
			n.Name = name
		}
	case *ast.Handle:
		if h := b.ck.HandleInfos[e]; h != nil {
			n = b.node("handle", ty)
			n.Scope = types.ScopeID(n.ID)
			n.Effects = []int{h.Effect.Unique}
			n.Scoped = true
			var initial *types.CaptureFlow
			if e.State != nil {
				initial = b.expr(e.State.Initial, local)
				n.Name = e.State.Name
			}
			n.Children = []*types.CaptureFlow{initial, b.expr(e.Body, local), nil}
			for i, c := range e.Clauses {
				if i >= len(h.Clauses) {
					continue
				}
				op := h.Clauses[i].Op
				inner := maps.Clone(local)
				if e.State != nil {
					inner[e.State.Name] = true
				}
				names := make([]string, len(c.Params))
				for j, p := range c.Params {
					names[j] = b.fresh()
					for _, name := range patternNames(p, nil) {
						inner[name] = true
					}
				}
				body := b.expr(c.Body, inner)
				for j := len(c.Params) - 1; j >= 0; j-- {
					body = b.pattern(c.Params[j], b.variable(names[j], nil), body)
				}
				n.Clauses = append(n.Clauses, types.CaptureClause{Index: op.Index, Names: names, Body: body})
			}
			if e.Return != nil {
				inner := maps.Clone(local)
				if e.State != nil {
					inner[e.State.Name] = true
				}
				for _, name := range patternNames(e.Return.Param, nil) {
					inner[name] = true
				}
				name := b.fresh()
				n.Names = []string{name}
				n.Children[2] = b.pattern(e.Return.Param, b.variable(name, h.BodyResult), b.expr(e.Return.Body, inner))
			}
		}
	case *ast.Neg:
		n = b.node("scalar", ty, b.expr(e.Operand, local))
	}
	if n == nil {
		n = b.node("scalar", ty)
	}
	n.Origin = e.Span()
	return n
}

type executionNeed struct {
	core.WorkNeed
	control bool
}

func (g *generator) executionNeeds(sub Subst) []executionNeed {
	if len(g.executionRoots) == 0 || (g.ck.Intrinsics[types.WorkRunName].Body == nil && g.ck.Intrinsics[types.CoroutineWithName].Body == nil) {
		return nil
	}
	b := &executionBuilder{ck: g.ck, sub: sub, defs: map[string]*core.Def{}}
	var context []core.Def
	names := make([]string, 0, len(g.ck.CaptureSummaries))
	for name := range g.ck.CaptureSummaries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := g.ck.CaptureSummaries[name].Contract
		if c == nil {
			continue
		}
		d := core.Def{Name: name, Type: c.SourceType, SourceType: c.SourceType, Params: c.Params, CaptureContract: c, Control: types.Control{Polymorphic: true}}
		context = append(context, d)
	}
	for i := range context {
		b.defs[context[i].Name] = &context[i]
	}
	infos := append([]DeclInfo(nil), g.ck.Checked...)
	infos = append(infos, g.executionRoots...)
	var pending []DeclInfo
	for _, info := range infos {
		if b.defs[info.Name] == nil && info.Body != nil {
			pending = append(pending, info)
			b.defs[info.Name] = &core.Def{Name: info.Name, Params: make([]string, len(info.Params))}
		}
	}
	for _, info := range pending {
		b.raw = false
		for _, root := range g.executionRoots {
			if root.Name == info.Name {
				b.raw = true
			}
		}
		d := b.definition(info)
		context = append(context, d)
		b.defs[info.Name] = &context[len(context)-1]
	}
	var defs []core.Def
	for _, info := range g.executionRoots {
		if d := b.defs[info.Name]; d != nil {
			defs = append(defs, *d)
		}
	}
	p := &core.Prog{Defs: defs, ADTs: g.ck.ADTOrder}
	var needs []executionNeed
	for _, n := range core.CollectWorkNeeds(p, context, g.ck.B) {
		needs = append(needs, executionNeed{WorkNeed: n})
	}
	for _, control := range core.CollectControlNeeds(p, context, g.ck.B) {
		name := types.CoroutineSuspensionName
		if control.Operation == "drive" {
			name = types.CoroutineDriveName
		}
		if eff := g.ck.Effects[name]; eff != nil {
			needs = append(needs, executionNeed{WorkNeed: core.WorkNeed{Budget: control.Row, Need: types.Row{Labels: []types.EffLabel{{Unique: eff.Unique, Name: eff.Name, Suspension: true}}}, Span: control.Span, In: control.In}, control: true})
		}
	}
	return needs
}

// sharesWorkRow recognizes an immediate execution bound belonging to a stored
// computation. Delay that bound until the computation's own row is known;
// ambient handler labels must not become effects of the packaged coroutine.
func sharesWorkRow(left, need types.Type) bool {
	ids := map[int]bool{}
	var vars func(types.Type, bool) bool
	vars = func(t types.Type, record bool) bool {
		switch t := t.(type) {
		case *types.TVar:
			if t.Kind != types.RowVar {
				return false
			}
			if record {
				ids[t.ID] = true
				return false
			}
			return ids[t.ID]
		case types.Row:
			return vars(t.Tail, record)
		}
		return false
	}
	vars(need, true)
	return vars(left, false)
}
