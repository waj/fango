package infer

import (
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
	"sort"
)

// All members share these monotypes until every body and record obligation
// has been solved. Generalization must never make a recursive edge polymorphic.
type recursiveInference struct{ types map[string]types.Type }

func (b *moduleCheck) inferGroup(group []int) {
	ck := b.ck
	rec := &recursiveInference{types: map[string]types.Type{}}
	previous := ck.recursive
	ck.recursive = rec
	defer func() { ck.recursive = previous }()
	for _, i := range group {
		d := b.values[i]
		ty := ck.Sup.FreshVar(types.General)
		rec.types[d.Name] = ty
		ck.Env.Bind(d.Name, types.Scheme{Body: ty})
		ck.Workers[d.Name] = len(d.Params)
	}
	qs := make([]*declInference, len(group))
	all := &generator{ck: ck}
	for k, i := range group {
		d := b.values[i]
		b.context(i, symbolModule(d.Name), func() { qs[k] = ck.prepareDecl(d, true) })
		all.cs = append(all.cs, qs[k].g.cs...)
		all.executionRoots = append(all.executionRoots, qs[k].g.executionRoots...)
		all.workRows = append(all.workRows, qs[k].g.workRows...)
		all.preds = append(all.preds, qs[k].g.preds...)
		b.errs = append(b.errs, qs[k].errs...)
		b.errs = append(b.errs, qs[k].g.errs...)
		qs[k].errs, qs[k].g.errs = nil, nil
	}
	sub, _, es := all.solveConstraints(nil)
	ck.Sub = sub
	b.errs = append(b.errs, es...)
	if len(b.errs) > 0 {
		return
	}

	// Compare every local annotation before any annotation can add labels
	// to the shared recursive rows.
	for _, q := range qs {
		b.errs = append(b.errs, q.g.checkLocalAnnotations()...)
	}
	for _, q := range qs {
		b.errs = append(b.errs, q.g.solveLocalAnnotations()...)
	}

	// Reconcile every annotation before the shared record fixed point.
	for _, q := range qs {
		if q.annTy == nil {
			continue
		}
		if !sameKnownEffects(ck.Sub.Apply(q.annTy), ck.Sub.Apply(q.ty)) {
			b.errs = append(b.errs, diag.Errorf(q.d.Ann.Sp, "EFFECT MISMATCH", "The annotation for %s does not match its body's effects.", q.d.Name))
		}
		sub, _, es := Solve([]Constraint{{Left: q.annTy, Right: q.ty, Span: q.d.NameSpan, Why: Why{Kind: WhyAnnotation, Name: q.d.Name}}}, nil, ck.Sub, ck.B, ck.Sup)
		ck.Sub = sub
		b.errs = append(b.errs, es...)
	}
	for {
		progress := 0
		for _, q := range qs {
			progress += q.g.recordPass(false, 0)
		}
		if progress == 0 {
			break
		}
	}

	for k, i := range group {
		q := qs[k]
		b.context(i, symbolModule(q.d.Name), func() { _, es := ck.finishDecl(q); b.errs = append(b.errs, es...) })
	}
	if len(b.errs) > 0 {
		return
	}
	for _, q := range qs {
		if q.d.Ann == nil && !q.isMain {
			ck.closeSingleRows(q.info.Type)
		}
	}
	// In an SCC every member reaches every other member. Joining obligations is
	// the fixed point of propagation along its monomorphic recursive edges.
	var obligations []predObligation
	for k, q := range qs {
		b.context(group[k], symbolModule(q.d.Name), func() {
			obs := append([]predObligation(nil), q.g.preds...)
			for _, p := range q.given {
				obs = append(obs, predObligation{pred: p, span: q.d.NameSpan})
			}
			residual, es := ck.reduceObligations(obs, nil)
			b.errs = append(b.errs, es...)
			for _, p := range residual {
				obligations = append(obligations, predObligation{pred: p, span: q.d.NameSpan})
			}
		})
	}
	for _, q := range qs {
		if q.isMain {
			q.info.Scheme = types.Scheme{Body: q.info.Type}
		} else {
			q.info.Scheme = ck.generalize(q.info.Type, nil)
		}
	}
	componentVars := map[int]*types.TVar{}
	for _, q := range qs {
		for _, v := range q.info.Scheme.Vars {
			componentVars[v.ID] = v
		}
	}
	for k, i := range group {
		q := qs[k]
		b.context(i, symbolModule(q.d.Name), func() {
			var es []diag.Error
			if q.originalAnn != nil {
				fresh := (&generator{ck: ck}).instantiate(q.info.Scheme)
				_, _, checkErrs := Solve([]Constraint{{Left: q.originalAnn, Right: fresh, Span: q.d.NameSpan, Why: Why{Kind: WhyAnnotation, Name: q.d.Name}}}, nil, Subst{}, ck.B, ck.Sup)
				b.errs = append(b.errs, checkErrs...)
			}
			bound := map[int]bool{}
			for _, v := range q.info.Scheme.Vars {
				bound[v.ID] = true
			}
			q.info.BodySubst = map[int]types.Type{}
			// Fresh occurrence variables keep a sibling's internal default from
			// specializing the public scheme of another function in this component.
			var ids []int
			for id := range componentVars {
				ids = append(ids, id)
			}
			sort.Ints(ids)
			for _, id := range ids {
				if !bound[id] {
					q.info.BodySubst[id] = ck.Sup.FreshVar(componentVars[id].Kind)
				}
			}
			obs := make([]predObligation, len(obligations))
			for j, o := range obligations {
				obs[j] = o
				obs[j].pred.Ty = types.SubstRigid(ck.Sub.Apply(o.pred.Ty), q.info.BodySubst)
			}
			q.info.Scheme, es = ck.qualify(q.info.Scheme, obs, q.given, q.d.Ann != nil, q.d.NameSpan)
			for id, t := range q.info.BodySubst {
				t = ck.Sub.Apply(t)
				if v, ok := t.(*types.TVar); ok && !v.Rigid {
					if v.Kind == types.RowVar {
						t = types.Row{}
					} else {
						t = ck.B.Unit
					}
				}
				q.info.BodySubst[id] = t
			}
			b.errs = append(b.errs, es...)
			b.errs = append(b.errs, ck.checkStageLeaks(q.d, q.info.Type, types.SurfaceName(q.d.Name))...)
		})
	}
	if len(b.errs) > 0 {
		return
	}
	for k, i := range group {
		ck.BindDecl(qs[k].info)
		b.add(i, []DeclInfo{qs[k].info}, nil)
	}
}
