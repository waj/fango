package elaborate

import (
	"fmt"
	"slices"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

// Inlining (doc/design/core.md, "Inlining") replaces saturated calls to small
// first-order pure workers with their bodies, then normalizes and simplifies
// the result. It runs during elaboration, so the interpreter executes the
// same Core as both backends.
//
// A candidate has Direct control, no evidence, effect, or row parameters, and
// no function in its type; its body contains only literals, variables,
// negation, natives, conditionals, sequencing, non-recursive bindings,
// constructor and worker applications, and matches. Such a body has no
// capture variables, scopes, or resumptions to refresh, so an inlined copy
// needs only fresh binder names and type-argument substitution.

// inlineLimit bounds a candidate body's node count after its own inlining.
const inlineLimit = 40

// Inlining enables the pass. This package's tests turn it off so their dumps
// show elaboration itself; inline_test.go covers the pass.
var Inlining = true

type inliner struct {
	candidates map[string]*core.Def
	adts       map[int]*types.ADTInfo
	fresh      int
}

// inlineDefs inlines into the owned definitions, reading candidate bodies
// from owned and installed definitions. Owned candidates are processed
// callees first, so a body that is copied has already been inlined into.
func inlineDefs(defs []core.Def, context []core.Def, ck *infer.Checker) {
	if !Inlining {
		return
	}
	in := &inliner{candidates: map[string]*core.Def{}, adts: map[int]*types.ADTInfo{}}
	for _, a := range ck.ADTOrder {
		in.adts[a.Con.Unique] = a
	}
	for i := range context {
		if d := &context[i]; in.eligible(d) && in.size(d.Body) <= inlineLimit {
			in.candidates[d.Name] = d
		}
	}
	owned := map[string]int{}
	for i := range defs {
		owned[defs[i].Name] = i
	}
	state := map[string]int{} // 0 unvisited, 1 in progress, 2 done
	recursive := map[string]bool{}
	var visit func(int)
	visit = func(i int) {
		d := &defs[i]
		state[d.Name] = 1
		for _, callee := range workerCallees(d.Body) {
			j, ok := owned[callee]
			if !ok {
				continue
			}
			switch state[callee] {
			case 0:
				visit(j)
			case 1:
				recursive[callee] = true
				recursive[d.Name] = true
			}
		}
		if d.Body != nil {
			d.Body = in.optimize(d.Body)
		}
		if !recursive[d.Name] && in.eligible(d) && in.size(d.Body) <= inlineLimit && !mentionsWorker(d.Body, d.Name) {
			in.candidates[d.Name] = d
		}
		state[d.Name] = 2
	}
	for i := range defs {
		if state[defs[i].Name] == 0 {
			visit(i)
		}
	}
}

// Unfoldable returns the definitions whose bodies another module could
// inline: a superset of the candidates, so a fingerprint over them changes
// whenever an inlinable body does.
func Unfoldable(defs []core.Def, ck *infer.Checker) []core.Def {
	in := &inliner{adts: map[int]*types.ADTInfo{}}
	for _, a := range ck.ADTOrder {
		in.adts[a.Con.Unique] = a
	}
	var out []core.Def
	for i := range defs {
		if in.eligible(&defs[i]) {
			out = append(out, defs[i])
		}
	}
	return out
}

func workerCallees(e core.Expr) []string {
	var out []string
	core.Inspect(e, func(e core.Expr) {
		if a, ok := e.(*core.App); ok && a.CalleeKind == core.Worker {
			if v, ok := a.Callee.(*core.VarRef); ok && !v.Local {
				out = append(out, v.Name)
			}
		}
	})
	return out
}

func mentionsWorker(e core.Expr, name string) bool {
	return slices.Contains(workerCallees(e), name)
}

func (in *inliner) eligible(d *core.Def) bool {
	if len(d.Params) == 0 || d.Body == nil || len(d.EffectParams) != 0 || d.RowParam != 0 || len(d.RowEffects) != 0 ||
		d.Control.Transport != types.Direct || d.Control.Polymorphic || d.Scoped || !firstOrder(d.Type) {
		return false
	}
	params, _ := core.PeelFun(d.Type, len(d.Params))
	for _, t := range params {
		if in.holdsFunctions(t, map[int]bool{}) {
			return false
		}
	}
	return simpleBody(d.Body)
}

// holdsFunctions reports a value type whose constructors store functions,
// such as a class dictionary; such a parameter carries capture obligations.
func (in *inliner) holdsFunctions(t types.Type, seen map[int]bool) bool {
	switch t := t.(type) {
	case *types.TFun:
		return true
	case *types.TCon:
		for _, a := range t.Args {
			if in.holdsFunctions(a, seen) {
				return true
			}
		}
		a := in.adts[t.Unique]
		if a == nil || seen[t.Unique] {
			return false
		}
		seen[t.Unique] = true
		for _, c := range a.Ctors {
			for _, f := range c.Fields {
				if in.holdsFunctions(f, seen) {
					return true
				}
			}
		}
	}
	return false
}

// firstOrder admits types without functions or effect rows anywhere, so a
// copied body carries no capture obligations.
func firstOrder(t types.Type) bool {
	switch t := t.(type) {
	case *types.TFun:
		if len(t.Eff.Labels) != 0 || t.Eff.Tail != nil {
			return false
		}
		return firstOrderValue(t.Arg) && (func() bool {
			if next, ok := t.Ret.(*types.TFun); ok {
				return firstOrder(next)
			}
			return firstOrderValue(t.Ret)
		})()
	default:
		return firstOrderValue(t)
	}
}

func firstOrderValue(t types.Type) bool {
	switch t := t.(type) {
	case *types.TFun:
		return false
	case *types.TCon:
		for _, a := range t.Args {
			if !firstOrderValue(a) {
				return false
			}
		}
	}
	return true
}

func simpleBody(e core.Expr) bool {
	ok := true
	core.Inspect(e, func(e core.Expr) {
		switch e := e.(type) {
		case *core.IntLit, *core.FloatLit, *core.StringLit, *core.CharLit, *core.BoolLit, *core.UnitLit,
			*core.VarRef, *core.Neg, *core.NativeCall, *core.If, *core.Seq, *core.Case:
		case *core.Let:
			ok = ok && !e.Rec
		case *core.App:
			ok = ok && (e.CalleeKind == core.Worker || e.CalleeKind == core.Ctor) &&
				len(e.EvidenceArgs) == 0 && e.Row == nil && e.Control.Transport == types.Direct && !e.Control.Polymorphic
		default:
			ok = false
		}
	})
	return ok
}

func (in *inliner) size(e core.Expr) int {
	n := 0
	core.Inspect(e, func(core.Expr) { n++ })
	return n
}

// optimize inlines eligible calls in a body, then normalizes and simplifies.
func (in *inliner) optimize(body core.Expr) core.Expr {
	changed := false
	out := core.Rewrite(body, sameType, func(e core.Expr) core.Expr {
		a, ok := e.(*core.App)
		if !ok || a.CalleeKind != core.Worker || len(a.EvidenceArgs) != 0 || a.Row != nil {
			return e
		}
		v, ok := a.Callee.(*core.VarRef)
		if !ok || v.Local {
			return e
		}
		d := in.candidates[v.Name]
		if d == nil || len(a.Args) != len(d.Params) || len(a.TyArgs) != len(d.TyParams) {
			return e
		}
		changed = true
		return in.instantiate(d, a)
	})
	if !changed {
		return body
	}
	out = in.normalize(out)
	return pruneBinders(in.simplify(out, map[string]*core.App{}))
}

// pruneBinders restores the invariant that a field binder is "" when its
// subtree no longer mentions it, as after an alias substitution.
func pruneBinders(e core.Expr) core.Expr {
	return core.Rewrite(e, sameType, func(e core.Expr) core.Expr {
		if c, ok := e.(*core.Case); ok {
			pruneTree(c.Tree)
		}
		return e
	})
}

func pruneTree(t core.Tree) {
	switch t := t.(type) {
	case *core.Guard:
		pruneTree(t.Then)
		pruneTree(t.Else)
	case *core.SwitchCtor:
		for i := range t.Cases {
			for j, b := range t.Cases[i].Binds {
				if b != "" && !treeMentions(t.Cases[i].Tree, b) {
					t.Cases[i].Binds[j] = ""
				}
			}
			pruneTree(t.Cases[i].Tree)
		}
		pruneTree(t.Default)
	case *core.SwitchLit:
		for i := range t.Cases {
			pruneTree(t.Cases[i].Tree)
		}
		pruneTree(t.Default)
	}
}

func treeMentions(t core.Tree, name string) bool {
	switch t := t.(type) {
	case *core.Leaf:
		return core.Mentions(t.Body, name)
	case *core.Guard:
		return core.Mentions(t.Cond, name) || treeMentions(t.Then, name) || treeMentions(t.Else, name)
	case *core.SwitchCtor:
		if t.Scrut == name {
			return true
		}
		for _, c := range t.Cases {
			if treeMentions(c.Tree, name) {
				return true
			}
		}
		return treeMentions(t.Default, name)
	case *core.SwitchLit:
		if t.Scrut == name {
			return true
		}
		for _, c := range t.Cases {
			if treeMentions(c.Tree, name) {
				return true
			}
		}
		return treeMentions(t.Default, name)
	}
	return false
}

// instantiate copies a candidate body with fresh binder names and its type
// parameters replaced by the call's type arguments, binding each parameter
// to its argument with a strict Let. Renaming follows lexical scope: a
// lexical binding wins over a global of the same spelling, as in lint.
func (in *inliner) instantiate(d *core.Def, a *core.App) core.Expr {
	subst := map[int]types.Type{}
	for i, v := range d.TyParams {
		subst[v.ID] = a.TyArgs[i]
	}
	c := &copier{in: in, typ: func(t types.Type) types.Type { return types.SubstRigid(t, subst) }}
	scope := map[string]string{}
	params := make([]string, len(d.Params))
	for i, p := range d.Params {
		params[i] = c.bind(scope, p)
	}
	body := c.expr(d.Body, scope)
	for i := len(params) - 1; i >= 0; i-- {
		body = &core.Let{Name: params[i], Rhs: a.Args[i], Body: body, Ty: body.Type()}
	}
	return body
}

type copier struct {
	in  *inliner
	typ func(types.Type) types.Type
}

// bind adds a fresh name for a binder to a copy of scope's entries visible
// below it; callers pass a scope they own.
func (c *copier) bind(scope map[string]string, name string) string {
	if name == "" {
		return ""
	}
	fresh := fmt.Sprintf("_inl%d_%s", c.in.fresh, name)
	c.in.fresh++
	scope[name] = fresh
	return fresh
}

func within(scope map[string]string) map[string]string {
	out := make(map[string]string, len(scope)+4)
	for k, v := range scope {
		out[k] = v
	}
	return out
}

func (c *copier) exprs(es []core.Expr, scope map[string]string) []core.Expr {
	out := make([]core.Expr, len(es))
	for i, e := range es {
		out[i] = c.expr(e, scope)
	}
	return out
}

func (c *copier) expr(e core.Expr, scope map[string]string) core.Expr {
	switch e := e.(type) {
	case *core.VarRef:
		n := *e
		if to, ok := scope[e.Name]; ok {
			n.Name, n.Local = to, true
		}
		n.Ty = c.typ(e.Ty)
		n.TyArgs = c.types(e.TyArgs)
		return &n
	case *core.Let:
		rhs := c.expr(e.Rhs, scope)
		inner := within(scope)
		name := c.bind(inner, e.Name)
		body := c.expr(e.Body, inner)
		return &core.Let{Name: name, Rhs: rhs, Body: body, Ty: body.Type()}
	case *core.Case:
		scrut := c.expr(e.Scrut, scope)
		inner := within(scope)
		bind := c.bind(inner, e.Bind)
		return &core.Case{Scrut: scrut, Bind: bind, Tree: c.tree(e.Tree, inner), Ty: c.typ(e.Ty)}
	case *core.If:
		return &core.If{Cond: c.expr(e.Cond, scope), Then: c.expr(e.Then, scope), Else: c.expr(e.Else, scope), Ty: c.typ(e.Ty)}
	case *core.Seq:
		return &core.Seq{First: c.expr(e.First, scope), Then: c.expr(e.Then, scope), Ty: c.typ(e.Ty)}
	case *core.Neg:
		return &core.Neg{Operand: c.expr(e.Operand, scope), Ty: c.typ(e.Ty)}
	case *core.NativeCall:
		n := *e
		n.Args = c.exprs(e.Args, scope)
		n.Ty = c.typ(e.Ty)
		return &n
	case *core.App:
		n := *e
		if e.SourceType != nil {
			n.SourceType = c.typ(e.SourceType)
		}
		n.Callee = c.expr(e.Callee, scope)
		n.Args = c.exprs(e.Args, scope)
		n.TyArgs = c.types(e.TyArgs)
		n.Ty = c.typ(e.Ty)
		return &n
	}
	// Literals: simpleBody admits nothing else.
	return core.Rewrite(e, c.typ, func(e core.Expr) core.Expr { return e })
}

func (c *copier) types(ts []types.Type) []types.Type {
	if ts == nil {
		return nil
	}
	out := make([]types.Type, len(ts))
	for i, t := range ts {
		out[i] = c.typ(t)
	}
	return out
}

func (c *copier) name(scope map[string]string, name string) string {
	if to, ok := scope[name]; ok {
		return to
	}
	return name
}

func (c *copier) tree(t core.Tree, scope map[string]string) core.Tree {
	switch t := t.(type) {
	case nil:
		return nil
	case *core.Leaf:
		return &core.Leaf{Body: c.expr(t.Body, scope)}
	case *core.Unreachable:
		return &core.Unreachable{}
	case *core.Guard:
		return &core.Guard{Cond: c.expr(t.Cond, scope), Then: c.tree(t.Then, scope), Else: c.tree(t.Else, scope)}
	case *core.SwitchCtor:
		n := *t
		n.Scrut = c.name(scope, t.Scrut)
		n.Cases = make([]core.CtorCase, len(t.Cases))
		for i, cc := range t.Cases {
			inner := within(scope)
			binds := make([]string, len(cc.Binds))
			for j, b := range cc.Binds {
				binds[j] = c.bind(inner, b)
			}
			n.Cases[i] = core.CtorCase{Ctor: cc.Ctor, Binds: binds, Tree: c.tree(cc.Tree, inner)}
		}
		n.Default = c.tree(t.Default, scope)
		return &n
	case *core.SwitchLit:
		n := *t
		n.Scrut = c.name(scope, t.Scrut)
		n.Cases = make([]core.LitCase, len(t.Cases))
		for i, lc := range t.Cases {
			n.Cases[i] = core.LitCase{Lit: c.expr(lc.Lit, scope), Tree: c.tree(lc.Tree, scope)}
		}
		n.Default = c.tree(t.Default, scope)
		return &n
	}
	panic(fmt.Sprintf("inline: unexpected tree %T", t))
}

// --- Normalization -------------------------------------------------------
//
// An inlined body is a Let or Case where a call was, often in an expression
// slot. normalize floats such bindings above the statement that uses them.
// A slot's earlier siblings that are not atoms are bound first, in order, so
// evaluation order is unchanged.

func atom(e core.Expr) bool {
	switch e := e.(type) {
	case *core.IntLit, *core.FloatLit, *core.StringLit, *core.CharLit, *core.BoolLit, *core.UnitLit:
		return true
	case *core.VarRef:
		return e.Local
	}
	return false
}

// statement reports a node that must not stay in an expression slot.
func statement(e core.Expr) bool {
	switch e.(type) {
	case *core.Let, *core.Case, *core.If, *core.Seq:
		return true
	}
	return false
}

func (in *inliner) temp() string {
	name := fmt.Sprintf("_inl%d", in.fresh)
	in.fresh++
	return name
}

type binding struct {
	name string
	rhs  core.Expr
}

func wrap(bs []binding, body core.Expr) core.Expr {
	for i := len(bs) - 1; i >= 0; i-- {
		body = &core.Let{Name: bs[i].name, Rhs: bs[i].rhs, Body: body, Ty: body.Type()}
	}
	return body
}

// normalize rewrites an expression in tail position.
func (in *inliner) normalize(e core.Expr) core.Expr {
	switch e := e.(type) {
	case *core.Let:
		rhs, bs := in.assign(e.Rhs)
		body := in.normalize(e.Body)
		return wrap(bs, &core.Let{Name: e.Name, Rhs: rhs, Body: body, Rec: e.Rec, Ty: body.Type()})
	case *core.If:
		cond, bs := in.slot(e.Cond)
		return wrap(bs, &core.If{Cond: cond, Then: in.normalize(e.Then), Else: in.normalize(e.Else), Ty: e.Ty})
	case *core.Case:
		scrut, bs := in.slot(e.Scrut)
		return wrap(bs, &core.Case{Scrut: scrut, Bind: e.Bind, Tree: in.tree(e.Tree), Ty: e.Ty})
	case *core.Seq:
		return &core.Seq{First: in.normalize(e.First), Then: in.normalize(e.Then), Ty: e.Ty}
	case *core.Lambda:
		n := *e
		n.Body = in.normalize(e.Body)
		return &n
	case *core.Handle:
		n := *e
		n.Body = in.normalize(e.Body)
		n.Clauses = append([]core.HandlerClause(nil), e.Clauses...)
		for i := range n.Clauses {
			n.Clauses[i].Body = in.normalize(n.Clauses[i].Body)
		}
		if e.Return != nil {
			n.Return = &core.ReturnClause{Param: e.Return.Param, Body: in.normalize(e.Return.Body)}
		}
		if e.State != nil {
			n.State = &core.HandlerState{Name: e.State.Name, Initial: in.normalize(e.State.Initial), Ty: e.State.Ty}
		}
		return &n
	case *core.Bracket:
		n := *e
		n.Acquire, n.Release, n.Body = in.normalize(e.Acquire), in.normalize(e.Release), in.normalize(e.Body)
		return &n
	}
	out, bs := in.children(e)
	return wrap(bs, out)
}

func (in *inliner) tree(t core.Tree) core.Tree {
	switch t := t.(type) {
	case *core.Leaf:
		return &core.Leaf{Body: in.normalize(t.Body)}
	case *core.Guard:
		// A guard condition is evaluated in place; keep its bindings local.
		return &core.Guard{Cond: in.normalize(t.Cond), Then: in.tree(t.Then), Else: in.tree(t.Else)}
	case *core.SwitchCtor:
		n := *t
		n.Cases = append([]core.CtorCase(nil), t.Cases...)
		for i := range n.Cases {
			n.Cases[i].Tree = in.tree(n.Cases[i].Tree)
		}
		n.Default = in.tree(t.Default)
		return &n
	case *core.SwitchLit:
		n := *t
		n.Cases = append([]core.LitCase(nil), t.Cases...)
		for i := range n.Cases {
			n.Cases[i].Tree = in.tree(n.Cases[i].Tree)
		}
		n.Default = in.tree(t.Default)
		return &n
	}
	return t
}

// assign normalizes a Let right-hand side. Case and If may head it; a Let
// heading it floats above the enclosing binding.
func (in *inliner) assign(e core.Expr) (core.Expr, []binding) {
	switch e := e.(type) {
	case *core.Let:
		if e.Rec {
			break
		}
		rhs, bs := in.assign(e.Rhs)
		bs = append(bs, binding{e.Name, rhs})
		body, more := in.assign(e.Body)
		return body, append(bs, more...)
	case *core.Case:
		scrut, bs := in.slot(e.Scrut)
		return &core.Case{Scrut: scrut, Bind: e.Bind, Tree: in.tree(e.Tree), Ty: e.Ty}, bs
	case *core.If:
		cond, bs := in.slot(e.Cond)
		return &core.If{Cond: cond, Then: in.normalize(e.Then), Else: in.normalize(e.Else), Ty: e.Ty}, bs
	case *core.Seq:
		return in.normalize(e), nil
	}
	return in.children(e)
}

// slot normalizes an expression slot: a statement is bound to a temporary.
func (in *inliner) slot(e core.Expr) (core.Expr, []binding) {
	if l, ok := e.(*core.Lambda); ok {
		n := *l
		n.Body = in.normalize(l.Body)
		return &n, nil
	}
	if statement(e) {
		if l, ok := e.(*core.Let); ok && !l.Rec {
			rhs, bs := in.assign(l.Rhs)
			bs = append(bs, binding{l.Name, rhs})
			body, more := in.slot(l.Body)
			return body, append(bs, more...)
		}
		if _, ok := e.(*core.Seq); ok {
			return e, nil
		}
		value, bs := in.assign(e)
		name := in.temp()
		return &core.VarRef{Name: name, Local: true, Ty: value.Type()}, append(bs, binding{name, value})
	}
	return in.children(e)
}

// children normalizes the slots of a non-binding node left to right. When a
// slot needs bindings, earlier non-atomic slots are bound first.
func (in *inliner) children(e core.Expr) (core.Expr, []binding) {
	var args []core.Expr
	var rebuild func([]core.Expr) core.Expr
	switch e := e.(type) {
	case *core.App:
		args = append([]core.Expr{e.Callee}, e.Args...)
		rebuild = func(xs []core.Expr) core.Expr { n := *e; n.Callee, n.Args = xs[0], xs[1:]; return &n }
	case *core.NativeCall:
		args = e.Args
		rebuild = func(xs []core.Expr) core.Expr { n := *e; n.Args = xs; return &n }
	case *core.Neg:
		args = []core.Expr{e.Operand}
		rebuild = func(xs []core.Expr) core.Expr { return &core.Neg{Operand: xs[0], Ty: e.Ty} }
	case *core.Perform:
		args = e.Args
		rebuild = func(xs []core.Expr) core.Expr { n := *e; n.Args = xs; return &n }
	default:
		return e, nil
	}
	out := make([]core.Expr, len(args))
	var bs []binding
	for i, arg := range args {
		value, more := in.slot(arg)
		if len(more) > 0 {
			for j := 0; j < i; j++ {
				if !atom(out[j]) && !isLambdaOrGlobal(out[j]) {
					name := in.temp()
					bs = append(bs, binding{name, out[j]})
					out[j] = &core.VarRef{Name: name, Local: true, Ty: out[j].Type()}
				}
			}
			bs = append(bs, more...)
		}
		out[i] = value
	}
	return rebuild(out), bs
}

// Lambdas and global references are values whose evaluation has no effect,
// so moving a binding before them does not change evaluation order.
func isLambdaOrGlobal(e core.Expr) bool {
	switch e := e.(type) {
	case *core.Lambda:
		return true
	case *core.VarRef:
		return !e.Local
	}
	return false
}

// --- Simplification ------------------------------------------------------

// simplify substitutes alias and literal bindings, floats a match on a
// single-constructor value out of a binding, and resolves matches on a
// variable bound to a known constructor application with atomic fields.
func (in *inliner) simplify(e core.Expr, known map[string]*core.App) core.Expr {
	switch e := e.(type) {
	case *core.Let:
		if !e.Rec {
			if v, ok := e.Rhs.(*core.VarRef); ok && v.Local {
				return in.simplify(substitute(e.Body, e.Name, v.Name), known)
			}
			if c, ok := e.Rhs.(*core.Case); ok {
				if floated := floatProduct(c, e); floated != nil {
					return in.simplify(floated, known)
				}
			}
			if inner, ok := e.Rhs.(*core.Let); ok && !inner.Rec {
				// let x = (let y = a in b) in c  ==>  let y = a in let x = b in c
				outer := &core.Let{Name: e.Name, Rhs: inner.Body, Body: e.Body, Ty: e.Ty}
				return in.simplify(&core.Let{Name: inner.Name, Rhs: inner.Rhs, Body: outer, Ty: e.Ty}, known)
			}
		}
		rhs := in.simplify(e.Rhs, known)
		if a, ok := rhs.(*core.App); ok && a.CalleeKind == core.Ctor && allAtoms(a.Args) {
			known = extend(known, e.Name, a)
		}
		body := in.simplify(e.Body, known)
		if !e.Rec && inert(rhs) && !core.Mentions(body, e.Name) {
			return body
		}
		return &core.Let{Name: e.Name, Rhs: rhs, Body: body, Rec: e.Rec, Ty: body.Type()}
	case *core.Case:
		if v, ok := e.Scrut.(*core.VarRef); ok && v.Local {
			if a := known[v.Name]; a != nil {
				if body := resolve(e, a); body != nil {
					return in.simplify(body, known)
				}
			}
		}
		return &core.Case{Scrut: in.simplify(e.Scrut, known), Bind: e.Bind, Tree: in.simplifyTree(e.Tree, known), Ty: e.Ty}
	case *core.If:
		return &core.If{Cond: e.Cond, Then: in.simplify(e.Then, known), Else: in.simplify(e.Else, known), Ty: e.Ty}
	case *core.Seq:
		return &core.Seq{First: in.simplify(e.First, known), Then: in.simplify(e.Then, known), Ty: e.Ty}
	}
	return e
}

func (in *inliner) simplifyTree(t core.Tree, known map[string]*core.App) core.Tree {
	switch t := t.(type) {
	case *core.Leaf:
		return &core.Leaf{Body: in.simplify(t.Body, known)}
	case *core.Guard:
		return &core.Guard{Cond: t.Cond, Then: in.simplifyTree(t.Then, known), Else: in.simplifyTree(t.Else, known)}
	case *core.SwitchCtor:
		n := *t
		n.Cases = append([]core.CtorCase(nil), t.Cases...)
		for i := range n.Cases {
			n.Cases[i].Tree = in.simplifyTree(n.Cases[i].Tree, known)
		}
		n.Default = in.simplifyTree(t.Default, known)
		return &n
	case *core.SwitchLit:
		n := *t
		n.Cases = append([]core.LitCase(nil), t.Cases...)
		for i := range n.Cases {
			n.Cases[i].Tree = in.simplifyTree(n.Cases[i].Tree, known)
		}
		n.Default = in.simplifyTree(t.Default, known)
		return &n
	}
	return t
}

func extend(known map[string]*core.App, name string, a *core.App) map[string]*core.App {
	out := make(map[string]*core.App, len(known)+1)
	for k, v := range known {
		out[k] = v
	}
	out[name] = a
	return out
}

// inert reports an expression whose evaluation can neither fail nor have an
// effect, so an unused binding of it may be dropped.
func inert(e core.Expr) bool {
	switch e := e.(type) {
	case *core.App:
		return e.CalleeKind == core.Ctor && allAtoms(e.Args)
	case *core.Lambda:
		return true
	}
	return atom(e) || isLambdaOrGlobal(e)
}

func allAtoms(es []core.Expr) bool {
	for _, e := range es {
		if !atom(e) {
			return false
		}
	}
	return true
}

// floatProduct turns `let x = (case s of K a b -> e) in body`, a match on a
// single-constructor value, into `case s of K a b -> let x = e in body`. No
// branch is duplicated, and unique names keep the binders out of body.
func floatProduct(c *core.Case, l *core.Let) core.Expr {
	sw, ok := c.Tree.(*core.SwitchCtor)
	if !ok || len(sw.Cases) != 1 || sw.Default != nil || len(sw.ADT.Ctors) != 1 || sw.Scrut != c.Bind {
		return nil
	}
	leaf, ok := sw.Cases[0].Tree.(*core.Leaf)
	if !ok {
		return nil
	}
	inner := &core.Let{Name: l.Name, Rhs: leaf.Body, Body: l.Body, Ty: l.Body.Type()}
	n := *sw
	n.Cases = []core.CtorCase{{Ctor: sw.Cases[0].Ctor, Binds: sw.Cases[0].Binds, Tree: &core.Leaf{Body: inner}}}
	return &core.Case{Scrut: c.Scrut, Bind: c.Bind, Tree: &n, Ty: inner.Ty}
}

// resolve selects the branch of a match on a known constructor whose tree is
// a single leaf, binding the case variable and fields to the atoms.
func resolve(c *core.Case, a *core.App) core.Expr {
	sw, ok := c.Tree.(*core.SwitchCtor)
	if !ok || sw.Scrut != c.Bind {
		return nil
	}
	var tree core.Tree
	var binds []string
	for _, cc := range sw.Cases {
		if cc.Ctor == a.Ctor {
			tree, binds = cc.Tree, cc.Binds
		}
	}
	if tree == nil {
		tree = sw.Default
	}
	leaf, ok := tree.(*core.Leaf)
	if !ok {
		return nil
	}
	body := leaf.Body
	for i := len(binds) - 1; i >= 0; i-- {
		if binds[i] != "" {
			body = &core.Let{Name: binds[i], Rhs: a.Args[i], Body: body, Ty: body.Type()}
		}
	}
	if c.Bind != "" {
		body = &core.Let{Name: c.Bind, Rhs: c.Scrut, Body: body, Ty: body.Type()}
	}
	return body
}

// substitute renames a local variable, including decision-tree scrutinees.
func substitute(e core.Expr, from, to string) core.Expr {
	names := map[string]string{from: to}
	return core.Rewrite(e, sameType, func(e core.Expr) core.Expr {
		switch e := e.(type) {
		case *core.VarRef:
			// Inside its scope a binder's name is lexical whatever the flag says.
			if e.Name == from {
				e.Name, e.Local = to, true
			}
		case *core.Case:
			renameScrutinees(e.Tree, names)
		}
		return e
	})
}

func renameScrutinees(t core.Tree, names map[string]string) {
	switch t := t.(type) {
	case *core.Guard:
		renameScrutinees(t.Then, names)
		renameScrutinees(t.Else, names)
	case *core.SwitchCtor:
		if to, ok := names[t.Scrut]; ok {
			t.Scrut = to
		}
		for i := range t.Cases {
			renameScrutinees(t.Cases[i].Tree, names)
		}
		renameScrutinees(t.Default, names)
	case *core.SwitchLit:
		if to, ok := names[t.Scrut]; ok {
			t.Scrut = to
		}
		for i := range t.Cases {
			renameScrutinees(t.Cases[i].Tree, names)
		}
		renameScrutinees(t.Default, names)
	}
}
