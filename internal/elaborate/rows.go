package elaborate

import (
	"maps"
	"slices"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

// This sentinel exists only between elaboration and lexical row binding.
const pendingRow types.CaptureVar = -1

func (el *elab) residualArgument(actual, explicit types.Row) *core.RowArgument {
	row := &core.RowArgument{}
	if actual.Tail != nil {
		row.From = pendingRow
	}
	for _, label := range types.SortedRow(actual).Labels {
		if !types.RuntimeEvidenceEffect(label) {
			continue
		}
		if slices.ContainsFunc(explicit.Labels, func(want types.EffLabel) bool { return want.Unique == label.Unique }) {
			continue
		}
		args := make([]types.Type, len(label.Args))
		for i, arg := range label.Args {
			args[i] = el.eraseRuntimeKinds(eraseRows(arg))
		}
		row.Effects = append(row.Effects, core.EffectInstance{Unique: label.Unique, Name: label.Name, Args: args, Captures: el.evidenceCaptures(label.Unique), Control: el.evidenceControl(label.Unique)})
	}
	return row
}

func (el *elab) valueAppWithRow(callee, arg core.Expr, raw types.Type) core.Expr {
	app := el.valueApp(callee, arg).(*core.App)
	app.SourceType = raw
	if app.Row != nil {
		app.Row = el.residualArgument(raw.(*types.TFun).Eff, callee.Type().(*types.TFun).Eff)
	}
	return app
}

// bindRows runs after ANF/specialization, before capture inference. Source call
// sites already retain their instantiated residual labels; this pass assigns
// lexical binders and fills only erased, abstract forwarding positions.
func bindRows(defs []core.Def, ck *infer.Checker) {
	for i := range defs {
		d := &defs[i]
		if core.ArrowOpenRow(d.Type, len(d.Params)) && d.RowParam == 0 {
			d.RowParam = ck.Sup.FreshCapture()
		}
		evidence := map[int]core.EffectInstance{}
		for _, ev := range append(append([]core.EffectInstance(nil), d.EffectParams...), d.RowEffects...) {
			evidence[ev.Unique] = ev
		}
		bindExpressionRows(d.Body, d.RowParam, evidence, ck)
	}
}

func bindExpressionRows(expr core.Expr, current types.CaptureVar, evidence map[int]core.EffectInstance, ck *infer.Checker) {
	resolve := func(ev *core.EffectInstance) {
		if actual, found := evidence[ev.Unique]; found && ev.Captures.Empty() {
			ev.Captures, ev.Control = actual.Captures, actual.Control
		}
	}
	bind := func(row **core.RowArgument, needed bool) {
		if !needed {
			return
		}
		if *row == nil {
			*row = &core.RowArgument{From: current}
		}
		argument := *row
		if argument.From == pendingRow {
			argument.From = current
		}
		var kept []core.EffectInstance
		for _, ev := range argument.Effects {
			if _, found := evidence[ev.Unique]; !found && current != 0 {
				// An abstract callback receives this interpretation through its
				// invocation row rather than capturing it during construction.
				argument.From = current
				continue
			}
			resolve(&ev)
			kept = append(kept, ev)
		}
		argument.Effects = kept
	}
	core.InspectPruned(expr, func(e core.Expr) bool {
		switch e := e.(type) {
		case *core.Lambda:
			row := current
			if core.ArrowOpenRow(e.Ty, 1) {
				if e.RowParam == 0 {
					e.RowParam = ck.Sup.FreshCapture()
				}
				row = e.RowParam
			}
			inner := maps.Clone(evidence)
			for _, ev := range append(append([]core.EffectInstance(nil), e.EffectParams...), e.RowEffects...) {
				inner[ev.Unique] = ev
			}
			bindExpressionRows(e.Body, row, inner, ck)
			return false
		case *core.Handle:
			inner := maps.Clone(evidence)
			inner[e.Effect.Unique] = e.Effect
			bindExpressionRows(e.Body, current, inner, ck)
			for _, clause := range e.Clauses {
				bindExpressionRows(clause.Body, current, evidence, ck)
			}
			if e.Return != nil {
				bindExpressionRows(e.Return.Body, current, evidence, ck)
			}
			if e.State != nil {
				bindExpressionRows(e.State.Initial, current, evidence, ck)
			}
			return false
		case *core.App:
			arity := 1
			if e.CalleeKind == core.Worker {
				arity = len(e.Args)
			}
			bind(&e.Row, e.CalleeKind != core.Ctor && core.ArrowOpenRow(e.Callee.Type(), arity))
			for i := range e.EvidenceArgs {
				resolve(&e.EvidenceArgs[i])
			}
		case *core.CoroutineScope:
			arity := 1
			if _, _, _, ok := types.CoroutineProtocol(e.CursorTy); ok {
				arity = 2
			}
			bind(&e.Row, core.ArrowOpenRow(e.Producer.Type(), arity))
		case *core.CoroutineAdvance:
			bind(&e.Row, true)
		case *core.Completion:
			bind(&e.Row, e.Name != types.CompletionFailureName)
		case *core.Perform:
			resolve(&e.Effect)
		case *core.ControlExit:
			resolve(&e.Effect)
		}
		return true
	})
}
