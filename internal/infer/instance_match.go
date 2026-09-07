package infer

import (
	"strconv"
	"strings"

	"github.com/waj/fango/internal/types"
)

// headMatch is the three-way outcome of matching an instance head against a
// predicate's type. Blocked means an unsolved metavariable in the target sat
// where the head demands concrete structure, so the answer could change as
// solving proceeds; resolution must defer rather than commit.
type headMatch int

const (
	headNo headMatch = iota
	headBlocked
	headYes
)

// matchHead one-way-matches the instance-head pattern pat (whose variables
// are the instance's rigid parameters) against a fully zonked target t.
// Bindings accumulate in m; a repeated pattern variable must bind equal
// types. Metavariables in the target never get guessed: they yield
// headBlocked wherever the pattern demands structure, while rigid variables
// (skolems) are atomic and yield a definite headNo.
func matchHead(pat, t types.Type, m map[int]types.Type) headMatch {
	switch pat := pat.(type) {
	case *types.TVar:
		if bound, ok := m[pat.ID]; ok {
			if types.Equal(bound, t) {
				return headYes
			}
			if containsMeta(bound) || containsMeta(t) {
				return headBlocked
			}
			return headNo
		}
		m[pat.ID] = t
		return headYes
	case *types.TCon:
		switch t := t.(type) {
		case *types.TVar:
			if !t.Rigid {
				return headBlocked
			}
			return headNo
		case *types.TCon:
			if pat.Unique != t.Unique || len(pat.Args) != len(t.Args) {
				return headNo
			}
			res := headYes
			for i := range pat.Args {
				if r := matchHead(pat.Args[i], t.Args[i], m); r < res {
					res = r
					if res == headNo {
						return headNo
					}
				}
			}
			return res
		}
		return headNo
	case *types.TFun:
		switch t := t.(type) {
		case *types.TVar:
			if !t.Rigid {
				return headBlocked
			}
			return headNo
		case *types.TFun:
			res := matchHead(pat.Arg, t.Arg, m)
			if r := matchRow(pat.Eff, t.Eff, m); r < res {
				res = r
			}
			if res == headNo {
				return headNo
			}
			if r := matchHead(pat.Ret, t.Ret, m); r < res {
				res = r
			}
			return res
		}
		return headNo
	case types.Row:
		t, ok := t.(types.Row)
		if !ok {
			return headNo
		}
		return matchRow(pat, t, m)
	}
	return headNo
}

// matchRow matches a closed pattern row (instance heads reject open rows)
// against a target row. An open target tail could still grow labels, so a
// missing pattern label is only a definite mismatch when the target is
// closed.
func matchRow(pat, t types.Row, m map[int]types.Type) headMatch {
	if v, ok := t.Tail.(*types.TVar); ok && v.Rigid {
		return headNo
	}
	ps, ts := types.SortedRow(pat), types.SortedRow(t)
	byUnique := map[int]types.EffLabel{}
	for _, l := range ps.Labels {
		byUnique[l.Unique] = l
	}
	res := headYes
	if t.Tail != nil {
		res = headBlocked
	}
	matched := 0
	for _, tl := range ts.Labels {
		pl, ok := byUnique[tl.Unique]
		if !ok || len(pl.Args) != len(tl.Args) {
			return headNo
		}
		matched++
		for i := range pl.Args {
			if r := matchHead(pl.Args[i], tl.Args[i], m); r < res {
				res = r
				if res == headNo {
					return headNo
				}
			}
		}
	}
	if matched < len(ps.Labels) {
		if t.Tail == nil {
			return headNo
		}
		return headBlocked
	}
	return res
}

// containsMeta reports whether any metavariable (non-rigid TVar) occurs in t.
func containsMeta(t types.Type) bool {
	switch t := t.(type) {
	case *types.TVar:
		return !t.Rigid
	case *types.TCon:
		for _, a := range t.Args {
			if containsMeta(a) {
				return true
			}
		}
	case *types.TFun:
		return containsMeta(t.Arg) || containsMeta(t.Eff) || containsMeta(t.Ret)
	case types.Row:
		for _, l := range t.Labels {
			for _, a := range l.Args {
				if containsMeta(a) {
					return true
				}
			}
		}
		if t.Tail != nil {
			return containsMeta(t.Tail)
		}
	}
	return false
}

// headAtLeastAsSpecific reports whether instance head a is at least as
// specific as b: every type b's pattern covers via a is covered by b, i.e.
// b's pattern matches a. Heads contain no metavariables, so the match is
// always definite.
func headAtLeastAsSpecific(a, b types.Type) bool {
	return matchHead(b, a, map[int]types.Type{}) == headYes
}

// canonicalHeadKey renders an instance head deterministically for name
// mangling: fully-qualified constructor names (including the distinct "()"), arguments in
// parentheses only when present, and variables as $<i> by first occurrence,
// so alpha-equivalent heads share a key and a bare constructor keeps the
// pre-structural-heads key. types.Show is unsuitable here — its variable
// naming is per-printer. The caller hex-encodes the key, so its character
// set is unconstrained.
func canonicalHeadKey(head types.Type) string {
	var b strings.Builder
	vars := map[int]int{}
	var render func(types.Type)
	render = func(t types.Type) {
		switch t := t.(type) {
		case *types.TVar:
			i, ok := vars[t.ID]
			if !ok {
				i = len(vars)
				vars[t.ID] = i
			}
			b.WriteString("$")
			b.WriteString(strconv.Itoa(i))
		case *types.TCon:
			name := t.Name
			b.WriteString(name)
			if len(t.Args) > 0 {
				b.WriteString("(")
				for i, a := range t.Args {
					if i > 0 {
						b.WriteString(",")
					}
					render(a)
				}
				b.WriteString(")")
			}
		case *types.TFun:
			b.WriteString("(")
			render(t.Arg)
			b.WriteString("->")
			render(t.Eff)
			render(t.Ret)
			b.WriteString(")")
		case types.Row:
			r := types.SortedRow(t)
			b.WriteString("{")
			for i, l := range r.Labels {
				if i > 0 {
					b.WriteString(",")
				}
				b.WriteString(l.Name)
				for _, a := range l.Args {
					b.WriteString(" ")
					render(a)
				}
			}
			b.WriteString("}")
		}
	}
	render(head)
	return b.String()
}

// headUniques collects the Uniques of every type constructor in the head in
// preorder — the REPL's `_generation_` disambiguator, so an instance name
// changes when any constructor mentioned in the head is redefined.
func headUniques(head types.Type) []int {
	var out []int
	var walk func(types.Type)
	walk = func(t types.Type) {
		switch t := t.(type) {
		case *types.TCon:
			out = append(out, t.Unique)
			for _, a := range t.Args {
				walk(a)
			}
		case *types.TFun:
			walk(t.Arg)
			walk(t.Eff)
			walk(t.Ret)
		case types.Row:
			for _, l := range t.Labels {
				out = append(out, l.Unique)
				for _, a := range l.Args {
					walk(a)
				}
			}
		}
	}
	walk(head)
	return out
}

// headsUnify reports whether two instance heads could both apply to some
// ground type: plain first-order unification treating every variable as
// unifiable, with an occurs check. Pure — bindings stay local.
func headsUnify(a, b types.Type) bool {
	s := map[int]types.Type{}
	var resolve func(types.Type) types.Type
	resolve = func(t types.Type) types.Type {
		for {
			v, ok := t.(*types.TVar)
			if !ok {
				return t
			}
			bound, ok := s[v.ID]
			if !ok {
				return t
			}
			t = bound
		}
	}
	var occurs func(int, types.Type) bool
	occurs = func(id int, t types.Type) bool {
		switch t := resolve(t).(type) {
		case *types.TVar:
			return t.ID == id
		case *types.TCon:
			for _, a := range t.Args {
				if occurs(id, a) {
					return true
				}
			}
		case *types.TFun:
			return occurs(id, t.Arg) || occurs(id, t.Eff) || occurs(id, t.Ret)
		case types.Row:
			for _, l := range t.Labels {
				for _, a := range l.Args {
					if occurs(id, a) {
						return true
					}
				}
			}
		}
		return false
	}
	var walk func(a, b types.Type) bool
	walk = func(a, b types.Type) bool {
		a, b = resolve(a), resolve(b)
		if av, ok := a.(*types.TVar); ok {
			if bv, ok := b.(*types.TVar); ok && av.ID == bv.ID {
				return true
			}
			if occurs(av.ID, b) {
				return false
			}
			s[av.ID] = b
			return true
		}
		if bv, ok := b.(*types.TVar); ok {
			if occurs(bv.ID, a) {
				return false
			}
			s[bv.ID] = a
			return true
		}
		switch a := a.(type) {
		case *types.TCon:
			bc, ok := b.(*types.TCon)
			if !ok || a.Unique != bc.Unique || len(a.Args) != len(bc.Args) {
				return false
			}
			for i := range a.Args {
				if !walk(a.Args[i], bc.Args[i]) {
					return false
				}
			}
			return true
		case *types.TFun:
			bf, ok := b.(*types.TFun)
			return ok && walk(a.Arg, bf.Arg) && walk(a.Eff, bf.Eff) && walk(a.Ret, bf.Ret)
		case types.Row:
			br, ok := b.(types.Row)
			if !ok || len(a.Labels) != len(br.Labels) {
				return false
			}
			as, bs := types.SortedRow(a), types.SortedRow(br)
			for i := range as.Labels {
				al, bl := as.Labels[i], bs.Labels[i]
				if al.Unique != bl.Unique || len(al.Args) != len(bl.Args) {
					return false
				}
				for j := range al.Args {
					if !walk(al.Args[j], bl.Args[j]) {
						return false
					}
				}
			}
			return true
		}
		return false
	}
	return walk(a, b)
}
