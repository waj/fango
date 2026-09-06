package core

import "github.com/waj/fango/internal/types"

// Self tail calls compile to loops (doc/design.md, "Go backend and runtime"):
// both backends consume this one predicate, so the differential suite
// referees any divergence. Core itself is untouched — the lint invariants
// that already hold for every App{Worker} (saturation, TyArgs and
// EvidenceArgs counts, lexical evidence availability, no shadowing) make
// eligibility a pure function of (*Def, *App).

// TailLoop is DetectTailLoop's positive verdict. Mutated records the params
// some rewritable call passes anything but a VarRef of themselves — the ones
// a loop body reassigns, and therefore the ones no escaping closure may
// observe.
type TailLoop struct {
	Mutated map[string]bool
}

// DetectTailLoop reports whether d has at least one rewritable self tail
// call and is loop-eligible. Eligibility:
//
//  1. some App reached from d.Body exclusively through the tail skeleton
//     (Let.Body, If branches, Case leaf bodies, Seq.Then — never Lambda
//     bodies, Handle, Let.Rhs, Seq.First, scrutinees, or guard conditions)
//     satisfies IsTailLoopCall, and
//  2. no Lambda body or Handle clause/return anywhere in d.Body mentions a
//     mutated param: generated Go closures capture locals by reference, so
//     an escaping closure would observe later-iteration reassignment.
//     Params passed through unchanged are safe to capture — this is what
//     keeps eta-expansion wrappers around callback params from
//     disqualifying List.each-shaped drivers.
func DetectTailLoop(d *Def) (*TailLoop, bool) {
	if len(d.Params) == 0 {
		// Keeps the nullary-generic-worker corner (polymorphic values) out.
		return nil, false
	}
	tl := &TailLoop{Mutated: map[string]bool{}}
	found := false
	var walk func(e Expr)
	var walkTree func(t Tree)
	walk = func(e Expr) {
		switch e := e.(type) {
		case *Let:
			walk(e.Body)
		case *If:
			walk(e.Then)
			walk(e.Else)
		case *Seq:
			walk(e.Then)
		case *Case:
			walkTree(e.Tree)
		case *App:
			if !IsTailLoopCall(d, e) {
				return
			}
			found = true
			for i, a := range e.Args {
				if v, ok := a.(*VarRef); ok && v.Name == d.Params[i] {
					continue
				}
				if d.Params[i] != "_" {
					tl.Mutated[d.Params[i]] = true
				}
			}
		}
	}
	walkTree = func(t Tree) {
		switch t := t.(type) {
		case *Guard:
			walkTree(t.Then)
			walkTree(t.Else)
		case *Leaf:
			walk(t.Body)
		case *SwitchCtor:
			for _, c := range t.Cases {
				walkTree(c.Tree)
			}
			if t.Default != nil {
				walkTree(t.Default)
			}
		case *SwitchLit:
			for _, c := range t.Cases {
				walkTree(c.Tree)
			}
			walkTree(t.Default)
		}
	}
	walk(d.Body)
	if !found || capturesMutated(d.Body, tl.Mutated) {
		return nil, false
	}
	return tl, true
}

// IsTailLoopCall reports whether e, reached via the tail skeleton, is a
// rewritable self call of d: a saturated Worker call of d itself at the
// identity type instantiation (anything else is polymorphic recursion, where
// frame reuse is unsound) passing d's own evidence params through unchanged
// (structurally guaranteed — the tail skeleton never crosses a Handle, and
// handles and lambdas are the only evidence pushers — but checked
// defensively).
func IsTailLoopCall(d *Def, e *App) bool {
	if e.CalleeKind != Worker {
		return false
	}
	ref, ok := e.Callee.(*VarRef)
	if !ok || ref.Name != d.Name {
		return false
	}
	if len(e.Args) != len(d.Params) || len(e.TyArgs) != len(d.TyParams) || len(e.EvidenceArgs) != len(d.EffectParams) {
		return false
	}
	for i, ta := range e.TyArgs {
		tv, ok := ta.(*types.TVar)
		if !ok || !tv.Rigid || tv.ID != d.TyParams[i].ID {
			return false
		}
	}
	for i, ev := range e.EvidenceArgs {
		if ev.Unique != d.EffectParams[i].Unique {
			return false
		}
	}
	return true
}

// capturesMutated reports whether any Lambda body or Handle clause/return
// under e mentions one of the mutated params. Mentions is exact because Core
// has no shadowing. A Lambda whose body mentions nothing mutated needs no
// deeper walk: any nested closure's mentions are included in the outer
// body's. Handle bodies and the other IIFE-emitting shapes run to completion
// within one iteration, before any reassignment, so only the stored closure
// shapes — lambda literals and handler clauses — are hazards.
func capturesMutated(e Expr, mutated map[string]bool) bool {
	if len(mutated) == 0 {
		return false
	}
	mentionsAny := func(x Expr) bool {
		for p := range mutated {
			if Mentions(x, p) {
				return true
			}
		}
		return false
	}
	var walk func(e Expr) bool
	var walkTree func(t Tree) bool
	walk = func(e Expr) bool {
		switch e := e.(type) {
		case *Lambda:
			return mentionsAny(e.Body)
		case *Handle:
			for _, c := range e.Clauses {
				if mentionsAny(c.Body) {
					return true
				}
			}
			if e.Return != nil && mentionsAny(e.Return.Body) {
				return true
			}
			return walk(e.Body)
		case *Neg:
			return walk(e.Operand)
		case *BinOp:
			return walk(e.L) || walk(e.R)
		case *NativeCall:
			for _, a := range e.Args {
				if walk(a) {
					return true
				}
			}
		case *If:
			return walk(e.Cond) || walk(e.Then) || walk(e.Else)
		case *Perform:
			for _, a := range e.Args {
				if walk(a) {
					return true
				}
			}
		case *Resume:
			return walk(e.Value)
		case *Seq:
			return walk(e.First) || walk(e.Then)
		case *Let:
			return walk(e.Rhs) || walk(e.Body)
		case *App:
			if walk(e.Callee) {
				return true
			}
			for _, a := range e.Args {
				if walk(a) {
					return true
				}
			}
		case *Case:
			return walk(e.Scrut) || walkTree(e.Tree)
		}
		return false
	}
	walkTree = func(t Tree) bool {
		switch t := t.(type) {
		case *Guard:
			return walk(t.Cond) || walkTree(t.Then) || walkTree(t.Else)
		case *Leaf:
			return walk(t.Body)
		case *SwitchCtor:
			for _, c := range t.Cases {
				if walkTree(c.Tree) {
					return true
				}
			}
			return t.Default != nil && walkTree(t.Default)
		case *SwitchLit:
			for _, c := range t.Cases {
				if walkTree(c.Tree) {
					return true
				}
			}
			return walkTree(t.Default)
		}
		return false
	}
	return walk(e)
}
