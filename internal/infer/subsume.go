package infer

import (
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

func (g *generator) solveConstraints(ps []types.Pred) (Subst, []types.Pred, []diag.Error) {
	var invariant []types.Type
	for _, p := range g.preds {
		invariant = append(invariant, p.pred.Ty)
	}
	for i := range g.cs {
		g.cs[i].Invariant = invariant
	}
	return Solve(g.cs, ps, g.ck.Sub, g.ck.B, g.ck.Sup)
}

// Polarity is a set of occurrences: absent, positive, negative, or both.
// The least fixed point handles recursive fields without assuming covariance.
type polarity uint8

const (
	positive  polarity = 1
	negative  polarity = 2
	invariant          = positive | negative
)

func flip(p polarity) polarity { return (p&positive)<<1 | (p&negative)>>1 }

func compose(a, b polarity) polarity {
	var out polarity
	if a&positive != 0 {
		out |= b
	}
	if a&negative != 0 {
		out |= flip(b)
	}
	return out
}

func variances(adts map[int]*types.ADTInfo) map[int][]polarity {
	out := map[int][]polarity{}
	for id, a := range adts {
		out[id] = make([]polarity, len(a.Params))
	}
	for changed := true; changed; {
		changed = false
		for id, a := range adts {
			params := map[int]int{}
			for i, p := range a.Params {
				params[p.ID] = i
			}
			var visit func(types.Type, polarity)
			visit = func(t types.Type, p polarity) {
				switch t := t.(type) {
				case *types.TVar:
					if i, ok := params[t.ID]; ok {
						next := out[id][i] | p
						if next != out[id][i] {
							out[id][i] = next
							changed = true
						}
					}
				case *types.TFun:
					visit(t.Arg, flip(p))
					visit(t.Eff, p)
					visit(t.Ret, p)
				case *types.TCon:
					vs, known := out[t.Unique]
					for i, arg := range t.Args {
						v := invariant
						if known && i < len(vs) {
							v = vs[i]
						}
						visit(arg, compose(p, v))
					}
				case types.Row:
					if t.Tail != nil {
						visit(t.Tail, p)
					}
					for _, l := range t.Labels {
						for _, arg := range l.Args {
							visit(arg, compose(p, invariant))
						}
					}
				}
			}
			for _, c := range a.Ctors {
				for _, f := range c.Fields {
					visit(f, positive)
				}
			}
		}
	}
	return out
}

// subsumption decomposes value compatibility into shape equalities and row
// bounds. A fresh expected type receives a fresh row view, never the original
// closed row: the next argument can contribute additional effects to that view.
func subsumption(c Constraint, sub Subst, bi *types.Builtins, sup *types.Supply, vs map[int][]polarity) ([]Constraint, *mismatch) {
	var rows []Constraint
	row := func(a, b types.Type) {
		r := c
		r.Left, r.Right = a, b
		r.Subsume = false
		r.Include = true
		rows = append(rows, r)
	}
	var view func(types.Type, polarity) types.Type
	view = func(t types.Type, p polarity) types.Type {
		t = sub.Apply(t)
		if p == invariant || p == 0 {
			return t
		}
		switch t := t.(type) {
		case types.Row:
			r := types.Row{Tail: sup.FreshVar(types.RowVar)}
			if p == positive {
				row(t, r)
			} else {
				row(r, t)
			}
			return r
		case *types.TFun:
			return &types.TFun{Arg: view(t.Arg, flip(p)), Eff: view(t.Eff, p).(types.Row), Ret: view(t.Ret, p), Control: t.Control}
		case *types.TCon:
			args := append([]types.Type(nil), t.Args...)
			for i, a := range args {
				v := invariant
				if vv, ok := vs[t.Unique]; ok && i < len(vv) {
					v = vv[i]
				}
				args[i] = view(a, compose(p, v))
			}
			return &types.TCon{Unique: t.Unique, Name: t.Name, Args: args}
		default:
			return t
		}
	}
	var check func(types.Type, types.Type) *mismatch
	check = func(a, b types.Type) *mismatch {
		a, b = sub.Apply(a), sub.Apply(b)
		for _, t := range c.Invariant {
			if types.Equal(b, sub.Apply(t)) {
				return unify(a, b, sub, bi, sup)
			}
		}
		if v, ok := b.(*types.TVar); ok && !v.Rigid && v.Kind == types.General {
			return unify(view(a, positive), b, sub, bi, sup)
		}
		if v, ok := a.(*types.TVar); ok && !v.Rigid && v.Kind == types.General {
			return unify(a, view(b, negative), sub, bi, sup)
		}
		switch a := a.(type) {
		case *types.TFun:
			if b, ok := b.(*types.TFun); ok {
				if m := check(b.Arg, a.Arg); m != nil {
					return m
				}
				row(a.Eff, b.Eff)
				return check(a.Ret, b.Ret)
			}
		case *types.TCon:
			if b, ok := b.(*types.TCon); ok && a.Unique == b.Unique && len(a.Args) == len(b.Args) {
				for i, arg := range a.Args {
					v := invariant
					if vv, ok := vs[a.Unique]; ok && i < len(vv) {
						v = vv[i]
					}
					var m *mismatch
					switch v {
					case positive:
						m = check(arg, b.Args[i])
					case negative:
						m = check(b.Args[i], arg)
					default:
						m = unify(arg, b.Args[i], sub, bi, sup)
					}
					if m != nil {
						return m
					}
				}
				return nil
			}
		case types.Row:
			if _, ok := b.(types.Row); ok {
				row(a, b)
				return nil
			}
		}
		// Row parameters may still be bare variables before substitution.
		if v, ok := a.(*types.TVar); ok && v.Kind == types.RowVar {
			if br, ok := b.(types.Row); ok {
				row(types.Row{Tail: v}, br)
				return nil
			}
			if bv, ok := b.(*types.TVar); ok && bv.Kind == types.RowVar {
				row(types.Row{Tail: v}, types.Row{Tail: bv})
				return nil
			}
		}
		if ar, ok := a.(types.Row); ok {
			if v, ok := b.(*types.TVar); ok && v.Kind == types.RowVar {
				row(ar, types.Row{Tail: v})
				return nil
			}
		}
		return unify(a, b, sub, bi, sup)
	}
	m := check(c.Left, c.Right)
	return rows, m
}
