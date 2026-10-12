package elaborate

import (
	"maps"
	"slices"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

// Storage inference is independent of source type equality. A hidden opening
// remains abstract to the source checker, while a compiled worker may quantify
// its concrete storage type. Failed trials leave the checked worker untouched.
// In particular, an occurs check bounds recursively growing storage shapes.
type storagePass struct {
	ck      *infer.Checker
	defs    map[string]*core.Def
	mirrors map[int]*types.ADTInfo
}

func hiddenRepresentations(p *core.Prog, context []core.Def, ck *infer.Checker, ownerNames ...string) {
	if !Inlining || ck.RuntimeDebug {
		return
	}
	pass := &storagePass{ck: ck, defs: map[string]*core.Def{}, mirrors: map[int]*types.ADTInfo{}}
	for _, a := range ck.ADTOrder {
		if a.RepresentationOf != nil {
			pass.mirrors[a.RepresentationOf.Con.Unique] = a
		}
	}
	owners := map[string]bool{}
	for _, owner := range ownerNames {
		owners[owner] = true
	}
	for _, d := range p.Defs {
		owners[d.Owner] = true
	}
	for _, a := range ck.ADTOrder {
		if !owners[symbolOwner(a.Con.Name)] || !a.HasHidden() || len(a.Ctors) != 1 || pass.mirrors[a.Con.Unique] != nil {
			continue
		}
		// Sum constructors need a sum of storage witnesses, handled separately.
		m := *a
		m.RepresentationOf = a
		m.Params = append(append([]*types.TVar(nil), a.Params...), a.Ctors[0].Hidden...)
		m.ParamKindsKnown = make([]bool, len(m.Params))
		for i := range m.ParamKindsKnown {
			m.ParamKindsKnown[i] = true
		}
		m.Con = &types.TCon{Unique: ck.Sup.NextUnique(), Name: storageName(a.Con.Name)}
		for _, v := range m.Params {
			m.Con.Args = append(m.Con.Args, v)
		}
		c := *a.Ctors[0]
		c.Name, c.Result, c.Hidden = storageName(a.Con.Name)+"_ctor", m.Con, nil
		m.Ctors = []*types.CtorInfo{&c}
		pass.mirrors[a.Con.Unique] = &m
		ck.ADTs[m.Con.Unique] = &m
		ck.ADTOrder = append(ck.ADTOrder, &m)
	}
	if len(pass.mirrors) == 0 {
		return
	}
	p.ADTs = ck.ADTOrder
	for i := range context {
		pass.defs[context[i].Name] = &context[i]
	}
	for i := range p.Defs {
		pass.defs[p.Defs[i].Name] = &p.Defs[i]
	}
	// Definitions are processed callees first. Self calls use the trial's own
	// signature; mutual recursion keeps the general representation for now.
	state := map[string]int{}
	owned := map[string]int{}
	for i := range p.Defs {
		owned[p.Defs[i].Name] = i
	}
	var additions []core.Def
	var visit func(int)
	visit = func(i int) {
		d := &p.Defs[i]
		if state[d.Name] != 0 {
			return
		}
		state[d.Name] = 1
		for _, name := range workerCallees(d.Body) {
			if j, ok := owned[name]; ok && j != i {
				visit(j)
			}
		}
		if variant := pass.worker(d); variant != nil {
			if variant.Name == d.Name {
				*d = *variant
			} else {
				d.RepresentationWorker = variant.Name
				additions = append(additions, *variant)
				pass.defs[variant.Name] = variant
			}
		}
		state[d.Name] = 2
	}
	for i := range p.Defs {
		visit(i)
	}
	p.Defs = append(p.Defs, additions...)
}

type storageTrial struct {
	pass        *storagePass
	def         *core.Def
	signature   types.Type
	opened      map[int]types.Type
	constraints []infer.Constraint
	metas       []*types.TVar
	failed      bool
	changed     bool
	selfCalls   []*core.App
}

func (t *storageTrial) fresh() *types.TVar {
	v := t.pass.ck.Sup.FreshVar(types.General)
	t.metas = append(t.metas, v)
	return v
}

func (t *storageTrial) expand(ty types.Type) types.Type {
	switch ty := ty.(type) {
	case *types.TVar:
		if v := t.opened[ty.ID]; v != nil {
			return v
		}
		return ty
	case *types.TCon:
		n := *ty
		n.Args = make([]types.Type, len(ty.Args))
		for i, a := range ty.Args {
			n.Args[i] = t.expand(a)
		}
		if m := t.pass.mirrors[ty.Unique]; m != nil {
			n.Name, n.Unique = m.Con.Name, m.Con.Unique
			for range m.RepresentationOf.Ctors[0].Hidden {
				n.Args = append(n.Args, t.fresh())
			}
			t.changed = true
		}
		return &n
	case *types.TFun:
		n := *ty
		n.Arg, n.Ret = t.expand(ty.Arg), t.expand(ty.Ret)
		return &n
	}
	return ty
}

func (t *storageTrial) equal(a, b types.Type) {
	t.constraints = append(t.constraints, infer.Constraint{Left: a, Right: b})
}

func (p *storagePass) worker(d *core.Def) *core.Def {
	if d.Body == nil || d.Scoped || d.RepresentationWorker != "" || !p.relevant(d) {
		return nil
	}
	t := &storageTrial{pass: p, def: d, opened: map[int]types.Type{}}
	t.signature = t.expand(d.Type)
	params, result := core.PeelFun(t.signature, len(d.Params))
	env := map[string]types.Type{}
	for i, name := range d.Params {
		env[name] = params[i]
	}
	body := t.expr(d.Body, env)
	if t.failed || !t.changed {
		return nil
	}
	t.equal(body.Type(), result)
	sub, _, errs := infer.Solve(t.constraints, nil, infer.Subst{}, p.ck.B, p.ck.Sup)
	if len(errs) != 0 {
		return nil
	}
	// Generalize representation variables after all field/call constraints have
	// solved; repeated occurrences in inputs and outputs retain their relation.
	for _, meta := range t.metas {
		if v, ok := sub.Apply(meta).(*types.TVar); ok && !v.Rigid {
			sub[v.ID] = p.ck.Sup.FreshRigid(types.General)
		}
	}
	n := *d
	n.Type = sub.Apply(t.signature)
	// Keep the checked source arrow timing for callback ABI selection.
	if d.SourceType != nil {
		n.SourceType = t.source(d.SourceType)
	}
	n.TyParams = append([]*types.TVar(nil), d.TyParams...)
	for _, v := range types.RigidVarsIn(n.Type) {
		if !slices.ContainsFunc(n.TyParams, func(p *types.TVar) bool { return p.ID == v.ID }) {
			n.TyParams = append(n.TyParams, v)
		}
	}
	if !types.Equal(n.Type, d.Type) || len(n.TyParams) != len(d.TyParams) {
		// A monomorphic top-level value is evaluated once. Publishing a second
		// initializer would duplicate evaluation, even when its fields are pure.
		if !d.IsWorker() {
			return nil
		}
		// A copied handler needs fresh activation/resumption identities. Until
		// those can be cloned, optimize handler bodies only in place.
		hasHandler := false
		core.Inspect(body, func(e core.Expr) {
			if _, ok := e.(*core.Handle); ok {
				hasHandler = true
			}
		})
		if hasHandler {
			return nil
		}
		n.Name = storageName(d.Name)
	}
	for _, call := range t.selfCalls {
		call.Callee.(*core.VarRef).Name = n.Name
		call.TyArgs = nil
		for _, v := range n.TyParams {
			call.TyArgs = append(call.TyArgs, v)
		}
	}
	n.Body = core.Rewrite(body, sub.Apply, func(e core.Expr) core.Expr { return e })
	refreshStorageCaptures(&n, p.ck.Sup)
	return &n
}

func (t *storageTrial) expr(e core.Expr, env map[string]types.Type) core.Expr {
	if e == nil {
		return nil
	}
	switch e := e.(type) {
	case *core.VarRef:
		n := *e
		if ty := env[e.Name]; ty != nil {
			n.Ty = storageUseType(ty, e.Ty)
			return &n
		}
		// Global values and partial applications retain their published ABI.
		return &n
	case *core.IntLit, *core.FloatLit, *core.BoolLit, *core.StringLit, *core.CharLit, *core.UnitLit, *core.RegexLit:
		return core.Rewrite(e, t.expand, func(e core.Expr) core.Expr { return e })
	case *core.Neg:
		n := *e
		n.Operand = t.expr(e.Operand, env)
		n.Ty = n.Operand.Type()
		return &n
	case *core.Let:
		if e.Rec {
			t.failed = true
			return e
		}
		n := *e
		n.Rhs = t.expr(e.Rhs, env)
		inner := maps.Clone(env)
		inner[e.Name] = n.Rhs.Type()
		n.Body = t.expr(e.Body, inner)
		n.Ty = n.Body.Type()
		return &n
	case *core.Seq:
		n := *e
		n.First = t.expr(e.First, env)
		n.Then = t.expr(e.Then, env)
		n.Ty = n.Then.Type()
		return &n
	case *core.If:
		n := *e
		n.Cond = t.expr(e.Cond, env)
		n.Then = t.expr(e.Then, env)
		n.Else = t.expr(e.Else, env)
		t.equal(n.Then.Type(), n.Else.Type())
		n.Ty = n.Then.Type()
		return &n
	case *core.Lambda:
		n := *e
		n.Ty = t.expand(e.Ty)
		if e.SourceType != nil {
			n.SourceType = t.source(e.SourceType)
		}
		fn := n.Ty.(*types.TFun)
		inner := maps.Clone(env)
		inner[e.Param] = fn.Arg
		n.Body = t.expr(e.Body, inner)
		t.equal(fn.Ret, n.Body.Type())
		return &n
	case *core.App:
		return t.app(e, env)
	case *core.Case:
		n := *e
		n.Scrut = t.expr(e.Scrut, env)
		inner := maps.Clone(env)
		inner[e.Bind] = n.Scrut.Type()
		n.Ty = t.expand(e.Ty)
		n.Tree = t.tree(e.Tree, inner, n.Ty)
		if leaf, ok := n.Tree.(*core.Leaf); ok {
			return &core.Let{Name: n.Bind, Rhs: n.Scrut, Body: leaf.Body, Ty: n.Ty}
		}
		return &n
	case *core.Handle:
		n := *e
		inner := maps.Clone(env)
		if e.State != nil {
			state := *e.State
			state.Initial = t.expr(state.Initial, env)
			t.equal(state.Initial.Type(), state.Ty)
			inner[state.Name] = state.Ty
			n.State = &state
		}
		n.Body = t.expr(e.Body, inner)
		t.equal(n.Body.Type(), e.Body.Type())
		n.Clauses = slices.Clone(e.Clauses)
		for i, clause := range e.Clauses {
			local := maps.Clone(inner)
			for j, name := range clause.Params {
				local[name] = clause.ParamTypes[j]
			}
			if clause.TraceParam != "" {
				local[clause.TraceParam] = clause.TraceType
			}
			if clause.SuppressedParam != "" {
				local[clause.SuppressedParam] = clause.SuppressedType
			}
			n.Clauses[i].Body = t.expr(clause.Body, local)
			t.equal(n.Clauses[i].Body.Type(), clause.Body.Type())
		}
		if e.Return != nil {
			ret := *e.Return
			local := maps.Clone(inner)
			local[ret.Param] = n.Body.Type()
			ret.Body = t.expr(ret.Body, local)
			t.equal(ret.Body.Type(), e.Return.Body.Type())
			n.Return = &ret
		}
		return &n
	case *core.Perform:
		n := *e
		n.Args = t.fixedArgs(e.Args, env)
		return &n
	case *core.ControlExit:
		n := *e
		n.Payload = t.fixedArgs(e.Payload, env)
		return &n
	case *core.ResumeTail:
		n := *e
		n.Value = t.expr(e.Value, env)
		t.equal(n.Value.Type(), e.Value.Type())
		if e.NextState != nil {
			n.NextState = t.expr(e.NextState, env)
			t.equal(n.NextState.Type(), e.NextState.Type())
		}
		return &n
	case *core.NativeCall:
		n := *e
		n.Args = make([]core.Expr, len(e.Args))
		for i, a := range e.Args {
			n.Args[i] = t.expr(a, env)
			t.equal(n.Args[i].Type(), a.Type())
		}
		return &n
	default:
		t.failed = true
		return e
	}
}

func (t *storageTrial) app(e *core.App, env map[string]types.Type) core.Expr {
	n := *e
	if e.SourceType != nil {
		n.SourceType = t.source(e.SourceType)
	}
	n.Args = make([]core.Expr, len(e.Args))
	for i, a := range e.Args {
		n.Args[i] = t.expr(a, env)
	}
	var signature types.Type
	switch e.CalleeKind {
	case core.Ctor:
		adt := t.pass.ck.ADTs[e.Ctor.Result.Unique]
		n.TyArgs = make([]types.Type, len(e.TyArgs))
		for i, a := range e.TyArgs {
			n.TyArgs[i] = t.expand(a)
		}
		if m := t.pass.mirrors[adt.Con.Unique]; m != nil {
			adt = m
			n.Ctor = m.Ctors[0]
			n.Witnesses = nil
			t.changed = true
			for _, w := range e.Witnesses {
				n.TyArgs = append(n.TyArgs, t.expand(w))
			}
		}
		n.Ty = &types.TCon{Name: adt.Con.Name, Unique: adt.Con.Unique, Args: n.TyArgs}
		fields := t.fields(adt, n.Ctor, n.TyArgs)
		if len(fields) != len(n.Args) {
			t.failed = true
			return e
		}
		for i, f := range fields {
			t.equal(f, n.Args[i].Type())
		}
		signature = n.Ty
		for i := len(fields) - 1; i >= 0; i-- {
			signature = &types.TFun{Arg: fields[i], Ret: signature}
		}
		n.Callee = &core.VarRef{Name: n.Ctor.Name, Ty: signature}
		return &n
	case core.Worker:
		ref, ok := e.Callee.(*core.VarRef)
		if !ok {
			t.failed = true
			return e
		}
		d := t.pass.defs[ref.Name]
		if d == nil {
			t.failed = true
			return e
		}
		n.TyArgs = make([]types.Type, len(e.TyArgs))
		for i, a := range e.TyArgs {
			n.TyArgs[i] = t.expand(a)
		}
		if d.Name == t.def.Name {
			signature = t.signature
			t.selfCalls = append(t.selfCalls, &n)
		} else {
			if d.RepresentationWorker != "" {
				d = t.pass.defs[d.RepresentationWorker]
				t.changed = true
			}
			if len(n.TyArgs) > len(d.TyParams) {
				t.failed = true
				return e
			}
			for len(n.TyArgs) < len(d.TyParams) {
				n.TyArgs = append(n.TyArgs, t.fresh())
			}
			sub := map[int]types.Type{}
			for i, v := range d.TyParams {
				sub[v.ID] = n.TyArgs[i]
			}
			signature = types.SubstRigid(d.Type, sub)
		}
		n.Callee = &core.VarRef{Name: d.Name, Ty: signature}
	case core.Value:
		n.Callee = t.expr(e.Callee, env)
		signature = n.Callee.Type()
	default:
		t.failed = true
		return e
	}
	args, result := core.PeelFun(signature, len(n.Args))
	if len(args) != len(n.Args) {
		t.failed = true
		return e
	}
	for i, a := range args {
		t.equal(a, n.Args[i].Type())
	}
	n.Ty = result
	return &n
}

func (t *storageTrial) fields(adt *types.ADTInfo, c *types.CtorInfo, args []types.Type) []types.Type {
	sub := map[int]types.Type{}
	for i, p := range adt.Params {
		if i < len(args) {
			sub[p.ID] = args[i]
		}
	}
	fields := make([]types.Type, len(c.Fields))
	el := &elab{ck: t.pass.ck}
	for i, f := range c.Fields {
		fields[i] = el.eraseRuntimeKinds(eraseRowsFrom(f, types.SubstRigid(f, sub)))
	}
	return fields
}

func (t *storageTrial) tree(tree core.Tree, env map[string]types.Type, result types.Type) core.Tree {
	switch tree := tree.(type) {
	case nil, *core.Unreachable:
		return tree
	case *core.Leaf:
		body := t.expr(tree.Body, env)
		t.equal(body.Type(), result)
		return &core.Leaf{Body: body}
	case *core.Guard:
		return &core.Guard{Cond: t.expr(tree.Cond, env), Then: t.tree(tree.Then, env, result), Else: t.tree(tree.Else, env, result)}
	case *core.SwitchLit:
		n := *tree
		n.Cases = append([]core.LitCase(nil), tree.Cases...)
		for i, c := range tree.Cases {
			n.Cases[i].Tree = t.tree(c.Tree, env, result)
		}
		n.Default = t.tree(tree.Default, env, result)
		return &n
	case *core.SwitchCtor:
		n := *tree
		con, ok := env[tree.Scrut].(*types.TCon)
		if !ok {
			t.failed = true
			return tree
		}
		adt := t.pass.ck.ADTs[con.Unique]
		if adt == nil {
			t.failed = true
			return tree
		}
		n.ADT = adt
		n.Cases = append([]core.CtorCase(nil), tree.Cases...)
		for i, c := range tree.Cases {
			inner := maps.Clone(env)
			old := t.opened
			t.opened = maps.Clone(old)
			ctor := adt.Ctors[c.Ctor.Index]
			n.Cases[i].Ctor = ctor
			if adt.RepresentationOf != nil {
				n.Cases[i].Hidden = nil
				start := len(con.Args) - len(c.Hidden)
				for j, v := range c.Hidden {
					t.opened[v.ID] = con.Args[start+j]
				}
			}
			fields := t.fields(adt, ctor, con.Args)
			for j, b := range c.Binds {
				if b != "" {
					inner[b] = fields[j]
				}
			}
			n.Cases[i].Tree = t.tree(c.Tree, inner, result)
			t.opened = old
		}
		n.Default = t.tree(tree.Default, env, result)
		return &n
	}
	t.failed = true
	return tree
}

// Internal names cannot collide with source identifiers (leading underscore is
// reserved), while retaining the defining module for separate compilation.
func storageName(name string) string {
	i := strings.LastIndex(name, ".") + 1
	return name[:i] + "_storage_" + name[i:]
}

func refreshStorageCaptures(d *core.Def, sup *types.Supply) {
	renamed := map[types.CaptureVar]types.CaptureVar{}
	substitutions := map[types.CaptureVar]types.CaptureSet{}
	bind := func(v types.CaptureVar) types.CaptureVar {
		if v == 0 {
			return 0
		}
		if n, ok := renamed[v]; ok {
			return n
		}
		n := sup.FreshCapture()
		renamed[v], substitutions[v] = n, types.VarCapture(n)
		return n
	}
	d.ParamCaptures = slices.Clone(d.ParamCaptures)
	for i, v := range d.ParamCaptures {
		d.ParamCaptures[i] = bind(v)
	}
	d.RowParam = bind(d.RowParam)
	d.EffectParams = slices.Clone(d.EffectParams)
	d.RowEffects = slices.Clone(d.RowEffects)
	effects := func(es []core.EffectInstance) {
		for i := range es {
			for _, v := range es[i].Captures.Vars {
				bind(v)
			}
		}
	}
	effects(d.EffectParams)
	effects(d.RowEffects)
	core.Inspect(d.Body, func(e core.Expr) {
		if l, ok := e.(*core.Lambda); ok {
			bind(l.ParamCapture)
			bind(l.RowParam)
			effects(l.EffectParams)
			effects(l.RowEffects)
		}
	})
	d.Body = core.SubstituteCaptureVars(d.Body, substitutions, nil)
	core.Inspect(d.Body, func(e core.Expr) {
		if l, ok := e.(*core.Lambda); ok {
			l.ParamCapture = renamed[l.ParamCapture]
			l.RowParam = renamed[l.RowParam]
		}
		if row := core.ExpressionRow(e); row != nil {
			if v, ok := renamed[row.From]; ok {
				row.From = v
			}
		}
	})
	for _, es := range [][]core.EffectInstance{d.EffectParams, d.RowEffects} {
		for i := range es {
			es[i].Captures = es[i].Captures.Substitute(substitutions)
		}
	}
	d.ResultCaptures = d.ResultCaptures.Substitute(substitutions)
}

func (t *storageTrial) source(ty types.Type) types.Type {
	// Source rows are retained for timing/owner checks. Storage arguments in the
	// source signature do not participate in representation equality.
	return types.SubstRigid(ty, t.opened)
}

func (t *storageTrial) fixedArgs(args []core.Expr, env map[string]types.Type) []core.Expr {
	out := make([]core.Expr, len(args))
	for i, a := range args {
		out[i] = t.expr(a, env)
		t.equal(out[i].Type(), a.Type())
	}
	return out
}

func (p *storagePass) relevant(d *core.Def) bool {
	var contains func(types.Type) bool
	contains = func(t types.Type) bool {
		switch t := t.(type) {
		case *types.TCon:
			if p.mirrors[t.Unique] != nil {
				return true
			}
			for _, a := range t.Args {
				if contains(a) {
					return true
				}
			}
		case *types.TFun:
			return contains(t.Arg) || contains(t.Ret)
		}
		return false
	}
	if contains(d.Type) {
		return true
	}
	found := false
	core.InspectPruned(d.Body, func(e core.Expr) bool {
		if contains(e.Type()) {
			found = true
		}
		if a, ok := e.(*core.App); ok && a.CalleeKind == core.Worker {
			if ref, ok := a.Callee.(*core.VarRef); ok {
				if target := p.defs[ref.Name]; target != nil && target.RepresentationWorker != "" {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// Representation inference changes data storage, never a checked occurrence's
// invocation protocol. A generic field schema can have a wider control family
// than the source occurrence after effect handling.
func storageUseType(storage, checked types.Type) types.Type {
	switch s := storage.(type) {
	case *types.TFun:
		if c, ok := checked.(*types.TFun); ok {
			n := *c
			n.Arg, n.Ret = storageUseType(s.Arg, c.Arg), storageUseType(s.Ret, c.Ret)
			return &n
		}
	case *types.TCon:
		if c, ok := checked.(*types.TCon); ok {
			n := *s
			n.Args = slices.Clone(s.Args)
			for i := range c.Args {
				if i < len(n.Args) {
					n.Args[i] = storageUseType(n.Args[i], c.Args[i])
				}
			}
			return &n
		}
	}
	return storage
}
