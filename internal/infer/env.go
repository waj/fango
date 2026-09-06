package infer

import (
	"github.com/waj/fango/internal/types"
)

// generalize quantifies ty's free variables that are not free in the
// enclosing scopes (avoid, keyed by var ID) — the doc/design.md, "Type inference" solve-at-binding
// hybrid's second half. Two variable flavors quantify:
//
//   - free metavariables (General and Number kinds — Number generalizes per
//     the doc/design.md, "Type inference" ruling): each is bound in ck.Sub to a fresh rigid var, so
//     every recorded occurrence type zonks to the scheme's own variables —
//     this is what hands elaboration rigid-typed occurrences with no extra
//     mapping pass;
//   - free rigid vars (the binding's own annotation skolems): already rigid,
//     they quantify as themselves. Skolems from enclosing annotations are in
//     avoid, so they stay bound to their own binder.
//
// Quantifier order is first occurrence in a left-to-right walk of the zonked
// type — deterministic, and the Go type-parameter order at codegen.
func (ck *Checker) generalize(ty types.Type, avoid map[int]bool) types.Scheme {
	ty = ck.Sub.Apply(ty)
	var vars []*types.TVar
	seen := map[int]bool{}
	var walk func(t types.Type)
	walk = func(t types.Type) {
		switch t := t.(type) {
		case *types.TVar:
			if seen[t.ID] || avoid[t.ID] {
				return
			}
			seen[t.ID] = true
			if t.Rigid {
				vars = append(vars, t)
				return
			}
			if t.Kind == types.General || t.Kind == types.RowVar {
				r := ck.Sup.FreshRigid(t.Kind)
				ck.Sub[t.ID] = r
				vars = append(vars, r)
			}
		case *types.TCon:
			for _, a := range t.Args {
				walk(a)
			}
		case *types.TFun:
			walk(t.Arg)
			walk(t.Eff)
			walk(t.Ret)
		case types.Row:
			for _, l := range t.Labels {
				for _, a := range l.Args {
					walk(a)
				}
			}
			if t.Tail != nil {
				walk(t.Tail)
			}
		}
	}
	walk(ty)
	if len(vars) == 0 {
		return types.Scheme{Body: ty}
	}
	return types.Scheme{Vars: vars, Body: ck.Sub.Apply(ty)}
}

// scopeFreeIDs collects every variable ID (meta or rigid) free in the
// enclosing local scopes, zonked — the avoid set for generalizing a block
// binding. Quantified vars of enclosing local schemes are closed and can
// never appear in a new binding's type, so including them is harmless.
func (g *generator) scopeFreeIDs() map[int]bool {
	ids := map[int]bool{}
	for s := g.locals; s != nil; s = s.parent {
		for _, sch := range s.names {
			collectVarIDs(g.ck.Sub.Apply(sch.Body), ids)
			for _, p := range sch.Preds {
				collectVarIDs(g.ck.Sub.Apply(p.Ty), ids)
			}
		}
	}
	return ids
}

func collectVarIDs(t types.Type, ids map[int]bool) {
	switch t := t.(type) {
	case *types.TVar:
		ids[t.ID] = true
	case *types.TCon:
		for _, a := range t.Args {
			collectVarIDs(a, ids)
		}
	case *types.TFun:
		collectVarIDs(t.Arg, ids)
		collectVarIDs(t.Eff, ids)
		collectVarIDs(t.Ret, ids)
	case types.Row:
		for _, l := range t.Labels {
			for _, a := range l.Args {
				collectVarIDs(a, ids)
			}
		}
		if t.Tail != nil {
			collectVarIDs(t.Tail, ids)
		}
	}
}

// mentionsVar reports whether the zonked t mentions the variable id — the
// skolem-escape check for block-binding annotations.
func (ck *Checker) mentionsVar(t types.Type, id int) bool {
	ids := map[int]bool{}
	collectVarIDs(ck.Sub.Apply(t), ids)
	return ids[id]
}
