package core

import (
	"fmt"
	"maps"

	"github.com/waj/fango/internal/types"
)

func ArrowOpenRow(t types.Type, arity int) bool {
	for i := 0; i < arity; i++ {
		fn, ok := t.(*types.TFun)
		if !ok {
			return false
		}
		if i == arity-1 {
			return types.FunctionOpenRow(fn)
		}
		t = fn.Ret
	}
	return false
}

func RowCaptures(row *RowArgument) types.CaptureSet {
	if row == nil {
		return types.CaptureSet{}
	}
	var captures types.CaptureSet
	if row.From != 0 {
		captures = types.VarCapture(row.From)
	}
	for _, effect := range row.Effects {
		captures = types.UnionCaptures(captures, effect.Captures)
	}
	return captures
}

// EqualValueRepresentation includes residual-row ABI metadata that ordinary
// source type equality intentionally ignores. Execution transport is separate:
// every stored function uses the same Direct/Exit/Machine family record.
func EqualValueRepresentation(a, b types.Type) bool {
	if !types.Equal(a, b) {
		return false
	}
	switch a := a.(type) {
	case *types.TFun:
		b := b.(*types.TFun)
		return types.FunctionOpenRow(a) == types.FunctionOpenRow(b) && EqualValueRepresentation(a.Arg, b.Arg) && EqualValueRepresentation(a.Ret, b.Ret) && EqualValueRepresentation(a.Eff, b.Eff)
	case *types.TCon:
		b := b.(*types.TCon)
		for i, arg := range a.Args {
			if !EqualValueRepresentation(arg, b.Args[i]) {
				return false
			}
		}
	case types.Row:
		a, b := types.SortedRow(a), types.SortedRow(b.(types.Row))
		for i, label := range a.Labels {
			for j, arg := range label.Args {
				if !EqualValueRepresentation(arg, b.Labels[i].Args[j]) {
					return false
				}
			}
		}
	}
	return true
}

// FreeRows reconstructs residual arguments retained by a closure. A nested
// function's invocation binder is local to that function, not a capture.
func FreeRows(expr Expr) map[types.CaptureVar]bool {
	free, bound := map[types.CaptureVar]bool{}, map[types.CaptureVar]int{}
	var visit func(Expr)
	visit = func(expr Expr) {
		InspectPruned(expr, func(e Expr) bool {
			if lambda, ok := e.(*Lambda); ok {
				bound[lambda.RowParam]++
				visit(lambda.Body)
				bound[lambda.RowParam]--
				return false
			}
			if row := ExpressionRow(e); row != nil && row.From != 0 && bound[row.From] == 0 {
				free[row.From] = true
			}
			return true
		})
	}
	visit(expr)
	return free
}

func ExpressionRow(e Expr) *RowArgument {
	switch e := e.(type) {
	case *Completion:
		return e.Row
	case *App:
		return e.Row
	case *CoroutineScope:
		return e.Row
	case *CoroutineAdvance:
		return e.Row
	}
	return nil
}

// CheckRowEvidence independently checks residual binding and call metadata.
// The capture graph retains these arguments for interpretation checking.
func CheckRowEvidence(p *Prog) []error {
	var errors []error
	report := func(format string, args ...any) { errors = append(errors, fmt.Errorf(format, args...)) }
	seen := map[types.CaptureVar]bool{}
	workers := map[string]*Def{}
	for i := range p.Defs {
		workers[p.Defs[i].Name] = &p.Defs[i]
	}
	bind := func(where string, id types.CaptureVar, open bool, rows map[types.CaptureVar]bool) {
		if (id != 0) != open {
			report("%s: residual row binder disagrees with its arrow", where)
		}
		if id != 0 {
			if seen[id] {
				report("%s: duplicate residual row binder %d", where, id)
			}
			seen[id], rows[id] = true, true
		}
	}
	addEffects := func(effects map[int]EffectInstance, explicit, deferred []EffectInstance, row types.CaptureVar, where string) {
		for _, ev := range explicit {
			effects[ev.Unique] = ev
		}
		ids := map[int]bool{}
		for _, ev := range explicit {
			ids[ev.Unique] = true
		}
		for _, ev := range deferred {
			if row == 0 || ids[ev.Unique] || ev.Unique == 0 {
				report("%s: invalid deferred evidence binder", where)
			}
			ids[ev.Unique] = true
			effects[ev.Unique] = ev
		}
	}
	var visit func(Expr, map[types.CaptureVar]bool, map[int]EffectInstance, string)
	visit = func(expr Expr, rows map[types.CaptureVar]bool, effects map[int]EffectInstance, where string) {
		InspectPruned(expr, func(e Expr) bool {
			switch e := e.(type) {
			case *Lambda:
				innerRows, innerEffects := maps.Clone(rows), maps.Clone(effects)
				bind(where, e.RowParam, ArrowOpenRow(e.Ty, 1), innerRows)
				addEffects(innerEffects, e.EffectParams, e.RowEffects, e.RowParam, where)
				visit(e.Body, innerRows, innerEffects, where)
				return false
			case *Handle:
				inner := maps.Clone(effects)
				inner[e.Effect.Unique] = e.Effect
				visit(e.Body, rows, inner, where)
				for _, clause := range e.Clauses {
					visit(clause.Body, rows, effects, where)
				}
				if e.Return != nil {
					visit(e.Return.Body, rows, effects, where)
				}
				if e.State != nil {
					visit(e.State.Initial, rows, effects, where)
				}
				return false
			}
			row := ExpressionRow(e)
			needsRow := false
			switch e := e.(type) {
			case *App:
				if e.CalleeKind == Worker {
					if ref, ok := e.Callee.(*VarRef); ok {
						if def := workers[ref.Name]; def != nil {
							needsRow = ArrowOpenRow(def.Type, len(def.Params))
						} else {
							needsRow = ArrowOpenRow(ref.Ty, len(e.Args))
						}
					}
				} else if e.CalleeKind == Value {
					needsRow = ArrowOpenRow(e.Callee.Type(), 1)
				}
			case *CoroutineScope:
				needsRow = ArrowOpenRow(e.Producer.Type(), 2)
				if types.CoroutineScopeType(e.CursorTy) {
					needsRow = ArrowOpenRow(e.Consumer.Type(), 1)
				}
			case *CoroutineAdvance:
				needsRow = true
			case *Completion:
				needsRow = e.Name != types.CompletionFailureName
			default:
				return true
			}
			if (row != nil) != needsRow {
				report("%s: missing or unexpected residual row argument", where)
			}
			if row == nil {
				return true
			}
			if row.From != 0 && !rows[row.From] {
				report("%s: residual argument references unavailable row %d", where, row.From)
			}
			last := 0
			for _, ev := range row.Effects {
				if ev.Unique <= last {
					report("%s: residual evidence is duplicated or unordered", where)
				}
				last = ev.Unique
				actual, ok := effects[ev.Unique]
				if !ok || !sameRowEffect(actual, ev) {
					report("%s: residual evidence has no matching lexical activation", where)
				}
			}
			return true
		})
	}
	for _, def := range p.Defs {
		where := "def " + def.Name
		rows, effects := map[types.CaptureVar]bool{}, map[int]EffectInstance{}
		bind(where, def.RowParam, ArrowOpenRow(def.Type, len(def.Params)), rows)
		addEffects(effects, def.EffectParams, def.RowEffects, def.RowParam, where)
		visit(def.Body, rows, effects, where)
	}
	return errors
}

func sameRowEffect(a, b EffectInstance) bool { return EqualEvidenceActivation(a, b) }

// EqualEvidenceActivation compares semantic types and capture sets, including
// the lexical identity; nil and empty set representations are equivalent.
func EqualEvidenceActivation(a, b EffectInstance) bool {
	if a.Unique != b.Unique || a.Name != b.Name || a.Control != b.Control || len(a.Args) != len(b.Args) || !types.EqualCaptures(a.Captures, b.Captures) {
		return false
	}
	for i := range a.Args {
		if !EqualValueRepresentation(a.Args[i], b.Args[i]) {
			return false
		}
	}
	return true
}
