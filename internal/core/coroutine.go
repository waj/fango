package core

import (
	"fmt"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
	"slices"
)

func (l *linter) coroutineScope(e *CoroutineScope, where string) {
	l.control(e.Control, where)
	name := types.CoroutineWithName
	dynamic := types.CoroutineScopeType(e.CursorTy)
	if dynamic {
		name = types.CoroutineScopeName
	}
	if !l.intrinsics[name] || l.defName != name {
		l.errorf("%s: coroutine scope outside %s", where, name)
	}
	if e.Scope == 0 || l.scopeIDs[e.Scope] {
		l.errorf("%s: invalid or reused coroutine owner", where)
	}
	l.scopeIDs[e.Scope] = true
	if e.Producer == nil || e.Consumer == nil {
		l.errorf("%s: missing coroutine callbacks", where)
		return
	}
	if dynamic {
		if d := l.workers[l.defName]; d == nil || !types.CoroutineShape(name, d.SourceType) {
			l.errorf("%s: invalid dynamic scope source contract", where)
		}
		if _, ok := e.Producer.(*UnitLit); !ok {
			l.errorf("%s: invalid coroutine owner/producer type", where)
		}
	}
	if err := CheckCoroutineProducer(e.CursorTy, e.Producer.Type()); !dynamic && err != nil {
		l.errorf("%s: %v", where, err)
	}
	for i, ev := range []EffectInstance{e.Yield, e.Traversal} {
		l.effectInstance(ev, where)
		name := types.CoroutineSuspensionName
		if i == 1 {
			name = types.CoroutineDriveName
		}
		declared := l.effects[ev.Unique]
		if declared == nil || declared.Name != name || !declared.Suspension || len(ev.Args) != 0 || ev.Control.Transport != types.Machine || !types.EqualCaptures(ev.Captures, types.ScopeCapture(e.Scope)) {
			l.errorf("%s: invalid coroutine control owner", where)
		}
	}
	driver, ok := e.Consumer.Type().(*types.TFun)
	if !ok || !EqualValueRepresentation(driver.Arg, e.CursorTy) || !EqualValueRepresentation(driver.Ret, e.Ty) || types.FunctionControl(driver).Transport != types.Machine {
		l.errorf("%s: invalid coroutine driver protocol", where)
	}
	if d := l.workers[l.defName]; d == nil || ArrowControl(d.Type, len(d.Params)) != e.Control {
		l.errorf("%s: stale coroutine boundary control", where)
	}
	l.expr(e.Producer, where)
	l.expr(e.Consumer, where)
}

func (l *linter) coroutineAdvance(e *CoroutineAdvance, where string) {
	name := types.CoroutineAdvanceName
	if e.Close {
		name = types.CoroutineCloseName
		if e.Result != nil {
			name = types.CoroutineStopName
		}
	}
	work := l.defName == types.WorkAdvanceName && !e.Close || l.defName == types.WorkCloseName && e.Close && e.Result == nil || l.defName == types.WorkStopName && e.Close && e.Result != nil
	if work {
		name = l.defName
	}
	if !l.intrinsics[name] || l.defName != name {
		l.errorf("%s: coroutine operation outside its declared intrinsic", where)
	}
	if e.Access != types.ExclusiveAdvance {
		l.errorf("%s: coroutine operation lacks exclusive access proof", where)
	}
	if e.Cursor == nil {
		l.errorf("%s: coroutine operation has no handle", where)
		return
	}
	if err := CheckCoroutineAdvance(e.Cursor.Type(), e.Reply, e.Close, e.Ty, e.Result); err != nil {
		l.errorf("%s: %v", where, err)
	}
	l.expr(e.Cursor, where)
	if e.Reply != nil {
		l.expr(e.Reply, where)
	}
}

// OwnerControlError is reconstructed from executable accesses after substituting
// callback and owner identities. A Direct/Exit annotation is not a discharge.
type OwnerControlError struct {
	Span                 source.Span
	In, Owner, Operation string
}

func (e OwnerControlError) Error() string { return "FOREIGN COROUTINE CONTROL: " + e.Detail() }
func (e OwnerControlError) Detail() string {
	return fmt.Sprintf("`%s` may %s the owner %s, but its execution contract is synchronous.", e.In, e.Operation, e.Owner)
}

func (f *flowChecker) requireControl(call *flowContext, owner int, operation string, effects ...int) {
	if f.collectControlNeed != nil {
		for _, row := range call.sourceRows {
			viaEvidence := false
			for r, ok := row.(types.Row); ok; r, ok = r.Tail.(types.Row) {
				for _, label := range r.Labels {
					viaEvidence = viaEvidence || slices.Contains(effects, label.Unique)
				}
			}
			if operation == "suspend" && viaEvidence {
				continue
			}

			f.collectControlNeed(ControlNeed{Row: row, Operation: operation, Span: call.origin, In: call.def})
		}
		return
	}
	if call.control.Polymorphic || call.control.Transport == types.Machine {
		return
	}
	err := OwnerControlError{Span: call.origin, In: call.def, Owner: f.owners[owner].name, Operation: operation}
	f.errors[err.Error()] = err
}
