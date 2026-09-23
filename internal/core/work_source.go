package core

import (
	"fmt"
	"github.com/waj/fango/internal/types"
)

// SourceEffectErrors verifies the source-row seam retained for work budgets.
// A call cannot enlarge an owner's recorded budget without charging that same
// row to its enclosing execution; IO is checked here despite having no runtime
// evidence. Ordinary evidence/representation lint still checks runtime edges.
func SourceEffectErrors(p *Prog) []error {
	if !p.Intrinsics[types.WorkRunName] {
		return nil
	}
	var errors []error
	var visit func(Expr, types.Row, bool, string)
	visit = func(expr Expr, ambient types.Row, known bool, where string) {
		InspectPruned(expr, func(e Expr) bool {
			switch e := e.(type) {
			case *Lambda:
				if fn := sourceLambdaContract(e); fn != nil {
					visit(e.Body, fn.Eff, true, where)
				} else {
					errors = append(errors, fmt.Errorf("%s: missing lambda source effect contract for %s", where, e.Param))
					visit(e.Body, types.Row{}, false, where)
				}
				return false
			case *Handle:
				inner := ambient
				inner.Labels = append(append([]types.EffLabel(nil), ambient.Labels...), types.EffLabel{Unique: e.Effect.Unique, Name: e.Effect.Name, Args: e.Effect.Args})
				visit(e.Body, inner, known, where)
				if e.State != nil {
					visit(e.State.Initial, ambient, known, where)
				}
				for _, clause := range e.Clauses {
					visit(clause.Body, ambient, known, where)
				}
				if e.Return != nil {
					visit(e.Return.Body, ambient, known, where)
				}
				return false
			case *App:
				if e.CalleeKind == Ctor {
					return true
				}
				if e.SourceType != nil && !sourceValueRepresentation(e.SourceType, e.Callee.Type(), true) {
					errors = append(errors, fmt.Errorf("%s: source call proof disagrees with runtime callee: %s / %s", where, types.Show(e.SourceType), types.Show(e.Callee.Type())))
				}
				if ref, ok := e.Callee.(*VarRef); ok && types.WorkIntrinsic(ref.Name) && e.SourceType == nil {
					errors = append(errors, fmt.Errorf("%s: missing work source call proof", where))
				}
				if !known || e.SourceType == nil {
					return true
				}
				cur := e.SourceType
				for range len(e.Args) {
					fn, ok := cur.(*types.TFun)
					if !ok {
						break
					}
					if !sourceRowIncluded(ambient, fn.Eff) {
						errors = append(errors, fmt.Errorf("%s: source call effects %s exceed enclosing row %s", where, types.Show(fn.Eff), types.Show(ambient)))
					}
					cur = fn.Ret
				}
			}
			return true
		})
	}
	for _, d := range p.Defs {
		if d.SourceType == nil {
			errors = append(errors, fmt.Errorf("def %s: missing source effect contract", d.Name))
			continue
		}
		if !sourceRepresentation(d.SourceType, d.Type) {
			errors = append(errors, fmt.Errorf("def %s: source effect contract disagrees with runtime definition", d.Name))
		}
		cur := d.SourceType
		row := types.Row{}
		known := true
		for range len(d.Params) {
			fn, ok := cur.(*types.TFun)
			if !ok {
				known = false
				break
			}
			row = fn.Eff
			cur = fn.Ret
		}
		visit(d.Body, row, known, "def "+d.Name)
	}
	return errors
}

// Row erasure may add a single-argument eta adapter around an existing local
// function. Its body forwards exactly that argument, so the underlying call's
// checked source arrow reconstructs the adapter's effects independently. Any
// lambda with its own computation needs the retained source contract.
func sourceLambdaContract(lambda *Lambda) *types.TFun {
	if fn, ok := lambda.SourceType.(*types.TFun); ok {
		return fn
	}
	app, ok := lambda.Body.(*App)
	if !ok || len(app.Args) != 1 {
		return nil
	}
	argument, ok := app.Args[0].(*VarRef)
	if !ok || argument.Name != lambda.Param {
		return nil
	}
	callee, ok := app.Callee.(*VarRef)
	if !ok || !callee.Local {
		return nil
	}
	fn, ok := app.SourceType.(*types.TFun)
	if !ok {
		return nil
	}
	return fn
}

func sourceRowIncluded(ambient, need types.Row) bool {
	if need.Tail != nil && (ambient.Tail == nil || !types.Equal(need.Tail, ambient.Tail)) {
		return false
	}
	for _, label := range need.Labels {
		found := false
		for _, allow := range ambient.Labels {
			if label.Unique == allow.Unique && len(label.Args) == len(allow.Args) {
				same := true
				for i, arg := range label.Args {
					same = same && types.Equal(arg, allow.Args[i])
				}
				found = found || same
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sourceRepresentation(source, runtime types.Type) bool {
	return sourceValueRepresentation(source, runtime, false)
}

func sourceValueRepresentation(source, runtime types.Type, call bool) bool {
	unit := func(t types.Type) bool { c, ok := t.(*types.TCon); return ok && c.Name == "()" && len(c.Args) == 0 }
	switch s := source.(type) {
	case *types.TVar:
		if s.Kind == types.RowVar {
			return unit(runtime)
		}
		return types.Equal(source, runtime)
	case types.Row:
		return unit(runtime)
	case *types.TCon:
		r, ok := runtime.(*types.TCon)
		if !ok || s.Unique != r.Unique || len(s.Args) != len(r.Args) {
			return false
		}
		for i, arg := range s.Args {
			if !sourceValueRepresentation(arg, r.Args[i], call) {
				return false
			}
		}
		return true
	case *types.TFun:
		r, ok := runtime.(*types.TFun)
		if !ok || !sourceValueRepresentation(s.Arg, r.Arg, call) || !sourceValueRepresentation(s.Ret, r.Ret, call) {
			return false
		}
		if !(call && types.FunctionOpenRow(r)) && len(s.Eff.Labels) != len(r.Eff.Labels) {
			return false
		}
		return sourceRowIncluded(types.Row{Labels: s.Eff.Labels}, types.Row{Labels: r.Eff.Labels})
	}
	return source == nil && runtime == nil
}
