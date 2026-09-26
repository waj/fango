package types

import "sort"

// ScopeID identifies one compiler-owned lexical capability scope. A ScopeID
// is proof metadata only: it is never emitted as a runtime liveness token.
// Zero means that evidence is supplied by the caller rather than introduced
// by a concrete scope in the current Core expression.
type ScopeID int

// CaptureVar identifies the captures of an abstract value or caller-supplied
// evidence parameter. Capture variables are compiler-only and are substituted
// with concrete capture sets at calls.
type CaptureVar int

// CaptureSet is a canonical union of concrete scope identities and abstract
// capture variables. The slices are sorted, duplicate-free, and must be
// treated as immutable.
type CaptureSet struct {
	Scopes []ScopeID
	Vars   []CaptureVar
}

type CaptureSummary struct {
	Vars     []CaptureVar
	Captures CaptureSet
}

func ScopeCapture(id ScopeID) CaptureSet {
	if id == 0 {
		return CaptureSet{}
	}
	return CaptureSet{Scopes: []ScopeID{id}}
}

func VarCapture(id CaptureVar) CaptureSet {
	if id == 0 {
		return CaptureSet{}
	}
	return CaptureSet{Vars: []CaptureVar{id}}
}

func (c CaptureSet) Empty() bool { return len(c.Scopes) == 0 && len(c.Vars) == 0 }

func (c CaptureSet) HasScope(id ScopeID) bool {
	i := sort.Search(len(c.Scopes), func(i int) bool { return c.Scopes[i] >= id })
	return i < len(c.Scopes) && c.Scopes[i] == id
}

func (c CaptureSet) HasVar(id CaptureVar) bool {
	i := sort.Search(len(c.Vars), func(i int) bool { return c.Vars[i] >= id })
	return i < len(c.Vars) && c.Vars[i] == id
}

// UnionCaptures returns a canonical union without mutating either operand.
func UnionCaptures(sets ...CaptureSet) CaptureSet {
	scopes := map[ScopeID]bool{}
	vars := map[CaptureVar]bool{}
	for _, set := range sets {
		for _, id := range set.Scopes {
			if id != 0 {
				scopes[id] = true
			}
		}
		for _, id := range set.Vars {
			if id != 0 {
				vars[id] = true
			}
		}
	}
	out := CaptureSet{Scopes: make([]ScopeID, 0, len(scopes)), Vars: make([]CaptureVar, 0, len(vars))}
	for id := range scopes {
		out.Scopes = append(out.Scopes, id)
	}
	for id := range vars {
		out.Vars = append(out.Vars, id)
	}
	sort.Slice(out.Scopes, func(i, j int) bool { return out.Scopes[i] < out.Scopes[j] })
	sort.Slice(out.Vars, func(i, j int) bool { return out.Vars[i] < out.Vars[j] })
	return out
}

// Without removes locally-bound capture variables and scope identities.
func (c CaptureSet) Without(scopes map[ScopeID]bool, vars map[CaptureVar]bool) CaptureSet {
	out := CaptureSet{}
	for _, id := range c.Scopes {
		if !scopes[id] {
			out.Scopes = append(out.Scopes, id)
		}
	}
	for _, id := range c.Vars {
		if !vars[id] {
			out.Vars = append(out.Vars, id)
		}
	}
	return out
}

// Substitute replaces abstract capture variables with the captures supplied
// by a call site. Unmapped variables remain abstract.
func (c CaptureSet) Substitute(m map[CaptureVar]CaptureSet) CaptureSet {
	parts := []CaptureSet{{Scopes: append([]ScopeID(nil), c.Scopes...)}}
	for _, id := range c.Vars {
		if replacement, ok := m[id]; ok {
			parts = append(parts, replacement)
		} else {
			parts = append(parts, VarCapture(id))
		}
	}
	return UnionCaptures(parts...)
}

func EqualCaptures(a, b CaptureSet) bool {
	if len(a.Scopes) != len(b.Scopes) || len(a.Vars) != len(b.Vars) {
		return false
	}
	for i := range a.Scopes {
		if a.Scopes[i] != b.Scopes[i] {
			return false
		}
	}
	for i := range a.Vars {
		if a.Vars[i] != b.Vars[i] {
			return false
		}
	}
	return true
}

func SubstCaptureVars(c CaptureSet, m map[CaptureVar]CaptureVar) CaptureSet {
	out := CaptureSet{Scopes: append([]ScopeID(nil), c.Scopes...)}
	for _, v := range c.Vars {
		if replacement, ok := m[v]; ok {
			v = replacement
		}
		out.Vars = append(out.Vars, v)
	}
	return UnionCaptures(out)
}
