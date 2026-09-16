package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

type localAnnotation struct {
	Constraint
	vars      []*types.TVar
	enclosing []types.Type
}

func (g *generator) deferLocalAnnotation(ann, shape types.Type, vars []*types.TVar, bind *ast.LocalBind) {
	pending := localAnnotation{Constraint: Constraint{Left: ann, Right: shape,
		Span: bind.Ann.Sp, Why: Why{Kind: WhyAnnotation, Name: bind.Name}}, vars: vars}
	// Retain the enclosing types, not the mutable block scope: the annotated
	// binding itself will be installed in that scope after this check point.
	for s := g.locals; s != nil; s = s.parent {
		for _, sch := range s.names {
			pending.enclosing = append(pending.enclosing, sch.Body)
		}
	}
	if rec := g.ck.recursive; rec != nil {
		for _, ty := range rec.types {
			pending.enclosing = append(pending.enclosing, ty)
		}
	}
	g.localAnnotations = append(g.localAnnotations, pending)
}

// sharedOpenEffects identifies final-arrow checks that depend on unfinished
// enclosing inference. Argument types retain their ordinary annotation rules.
func (g *generator) sharedOpenEffects(ty types.Type) bool {
	avoid := g.scopeFreeIDs()
	for {
		fn, ok := g.ck.Sub.Apply(ty).(*types.TFun)
		if !ok {
			return false
		}
		if tail, ok := fn.Eff.Tail.(*types.TVar); ok && !tail.Rigid && avoid[tail.ID] {
			return true
		}
		ty = fn.Ret
	}
}

// annotationShape preserves independent annotated arrows, including returned
// lambdas. Only effects shared with unfinished inference get a fresh row view.
func (g *generator) annotationShape(ann, body types.Type, avoid map[int]bool, bind *ast.LocalBind) types.Type {
	a, aok := ann.(*types.TFun)
	b, bok := g.ck.Sub.Apply(body).(*types.TFun)
	if !aok || !bok {
		return ann
	}
	eff := a.Eff
	if tail, ok := b.Eff.Tail.(*types.TVar); ok && !tail.Rigid && avoid[tail.ID] {
		eff = types.Row{Tail: g.ck.Sup.FreshVar(types.RowVar)}
	} else if !sameKnownRowEffects(a.Eff, b.Eff) {
		g.errs = append(g.errs, diag.Errorf(bind.Ann.Sp, "EFFECT MISMATCH", "The effect row in the annotation for `%s` does not exactly match the effects performed by its body.", bind.Name))
	}
	return &types.TFun{Arg: a.Arg, Eff: eff, Ret: g.annotationShape(a.Ret, b.Ret, avoid, bind)}
}

func (g *generator) checkLocalAnnotations() []diag.Error {
	var errs []diag.Error
	for _, c := range g.localAnnotations {
		if !sameKnownEffects(g.ck.Sub.Apply(c.Left), g.ck.Sub.Apply(c.Right)) {
			errs = append(errs, diag.Errorf(c.Span, "EFFECT MISMATCH", "The effect row in the annotation for `%s` does not exactly match the effects performed by its body.", c.Why.Name))
		}
	}
	return errs
}

func (g *generator) solveLocalAnnotations() []diag.Error {
	cs := make([]Constraint, len(g.localAnnotations))
	for i, pending := range g.localAnnotations {
		cs[i] = pending.Constraint
	}
	sub, _, errs := Solve(cs, nil, g.ck.Sub, g.ck.B, g.ck.Sup)
	g.ck.Sub = sub
	for _, pending := range g.localAnnotations {
		for _, v := range pending.vars {
			for _, ty := range pending.enclosing {
				if g.ck.mentionsVar(ty, v.ID) {
					errs = append(errs, diag.Errorf(pending.Span, "ANNOTATION TOO GENERAL",
						"The annotation for `%s` claims a type variable that the enclosing\ndefinition pins down — the annotation is more general than the body\nallows.", pending.Why.Name))
					break
				}
			}
		}
	}
	g.localAnnotations = nil
	return errs
}

func (g *generator) finishLocalAnnotations() []diag.Error {
	errs := g.checkLocalAnnotations()
	return append(errs, g.solveLocalAnnotations()...)
}
