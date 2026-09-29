package infer

import (
	"sort"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

func markScopedScheme(s types.Scheme, arity int) types.Scheme {
	args, _ := peelArrows(s.Body, arity)
	if len(args) == arity && arity > 0 {
		if callback, ok := args[arity-1].(*types.TFun); ok {
			s.ScopedRow, _ = callback.Eff.Tail.(*types.TVar)
			s.ScopedArity = arity
		}
	}
	return s
}

func (ck *Checker) checkScopedDeclaration(d *ast.ValueDecl, ty types.Type, tv *TypeVars) []diag.Error {
	fail := func(message string) []diag.Error {
		return []diag.Error{diag.Errorf(d.NameSpan, "SCOPED CALLBACK", "%s", message)}
	}
	if tv == nil || ty == nil {
		return fail("A scoped runner needs a valid annotation.")
	}
	v := tv.vars[d.ScopedRow]
	if v == nil || v.Kind != types.RowVar {
		return fail("The scoped name must be a row variable in the annotation.")
	}
	arity := len(d.Params)
	if d.Native != nil {
		arity = types.IntrinsicArity(d.Name)
	}
	if arity == 0 {
		return fail("A scoped runner must be an annotated function with a final callback parameter.")
	}
	args, result := peelArrows(ty, arity)
	if len(args) != arity {
		return fail("The annotation must match the runner's parameter count.")
	}
	fn, ok := args[arity-1].(*types.TFun)
	if !ok || len(fn.Eff.Labels) != 0 || !types.Equal(fn.Eff.Tail, v) {
		return fail("The final callback must have one argument and perform exactly the scoped row.")
	}
	outside := append(append([]types.Type(nil), args[:arity-1]...), result)
	cursor := ty
	for range arity {
		arrow := cursor.(*types.TFun)
		outside = append(outside, arrow.Eff)
		cursor = arrow.Ret
	}
	for _, t := range outside {
		ids := map[int]bool{}
		collectVarIDs(t, ids)
		if ids[v.ID] {
			return fail("The scoped row may occur only in the final callback's argument, effect row, and result.")
		}
	}
	if len(d.Ann.Preds) != 0 {
		return fail("Scoped runner annotations do not yet support class constraints.")
	}
	if d.Native == nil {
		for _, eq := range declEquations(d) {
			if _, ok := eq.Params[arity-1].(*ast.PVar); !ok {
				return fail("Bind the scoped callback to a name.")
			}
		}
	}
	return nil
}

func (g *generator) scopedSpine(e *ast.App) (string, types.Scheme, bool) {
	head, ok := appHead(e).(*ast.Var)
	if !ok {
		return "", types.Scheme{}, false
	}
	name := head.Name
	if alias := g.ck.Aliases[name]; alias != "" {
		name = alias
	}
	if _, local := g.locals.lookup(name); local {
		return "", types.Scheme{}, false
	}
	sch, ok := g.ck.Env.Lookup(name)
	return name, sch, ok && sch.ScopedRow != nil && len(appArgs(e)) == sch.ScopedArity
}

func (g *generator) scopeOuterTypes() []types.Type {
	out := []types.Type{g.ambient}
	for s := g.locals; s != nil; s = s.parent {
		names := make([]string, 0, len(s.names))
		for name := range s.names {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			sch := s.names[name]
			out = append(out, sch.Body)
			for _, p := range sch.Preds {
				out = append(out, p.Ty)
			}
		}
	}
	if rec := g.ck.recursive; rec != nil {
		names := make([]string, 0, len(rec.types))
		for name := range rec.types {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out = append(out, rec.types[name])
		}
	}
	for _, p := range g.preds {
		out = append(out, p.pred.Ty)
	}
	for _, ob := range g.records {
		if ob.receiver != nil {
			out = append(out, ob.receiver)
		}
		if ob.result != nil {
			out = append(out, ob.result)
		}
		for _, u := range ob.updates {
			out = append(out, u.ty)
		}
	}
	return out
}

func (g *generator) scopedCall(e *ast.App, name string, sch types.Scheme) types.Type {
	m := map[int]types.Type{}
	for _, v := range sch.Vars {
		m[v.ID] = g.ck.Sup.FreshVar(v.Kind)
	}
	raw := types.SubstRigid(sch.Body, m)
	current := raw
	var base types.Row
	for range sch.ScopedArity {
		fn := current.(*types.TFun)
		base = fn.Eff
		current = fn.Ret
	}
	fresh := NewFreshEffect(g.ck.Sup, "local scope")
	m[sch.ScopedRow.ID] = fresh.Within(base)
	raw = types.SubstRigid(sch.Body, m)
	head := appHead(e).(*ast.Var)
	head.Name = name
	g.ck.ExprSchemes[head] = sch
	g.ck.ExprTypes[head] = raw
	outer := g.scopeOuterTypes()
	if g.annotationAmbient != nil {
		g.cs = append(g.cs, Constraint{Left: base, Right: *g.annotationAmbient, Include: true, Span: e.Span(), Why: Why{Kind: WhyCall}})
	}
	args := appArgs(e)
	parameterTypes, _ := peelArrows(raw, sch.ScopedArity)
	outer = append(outer, parameterTypes[:len(parameterTypes)-1]...)
	// Populate partial spine types for elaboration without treating a partial
	// scoped runner as a first-class value in source.
	spine := make([]*ast.App, len(args))
	node := e
	for i := len(args) - 1; i >= 0; i-- {
		spine[i] = node
		if i > 0 {
			node = node.Fn.(*ast.App)
		}
	}
	current = raw
	for i, arg := range args {
		fn := current.(*types.TFun)
		at := g.exprWant(arg, fn.Arg)
		g.cs = append(g.cs, g.argument(at, fn.Arg, arg))
		g.performs(fn.Eff, arg.Span(), false)
		current = fn.Ret
		g.ck.ExprTypes[spine[i]] = current
	}
	g.scopeObligations = append(g.scopeObligations, Constraint{Scope: &ScopeBoundary{
		Effect: fresh, Result: current, Residual: base, Outer: outer,
	}, Span: e.Span()})
	return current
}

func (g *generator) checkScopeBoundaries() []diag.Error {
	var errs []diag.Error
	for _, c := range g.scopeObligations {
		if c.Scope != nil {
			errs = append(errs, c.Scope.check(g.ck.Sub, c.Span)...)
		}
	}
	return errs
}

func hasScopedPermission(t types.Type) bool {
	switch t := t.(type) {
	case *types.TCon:
		for _, a := range t.Args {
			if hasScopedPermission(a) {
				return true
			}
		}
	case *types.TFun:
		return hasScopedPermission(t.Arg) || hasScopedPermission(t.Eff) || hasScopedPermission(t.Ret)
	case types.Row:
		for _, l := range t.Labels {
			if l.Scoped {
				return true
			}
			for _, a := range l.Args {
				if hasScopedPermission(a) {
					return true
				}
			}
		}
		return t.Tail != nil && hasScopedPermission(t.Tail)
	}
	return false
}
