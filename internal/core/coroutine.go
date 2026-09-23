package core

import (
	"fmt"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

func (l *linter) coroutineScope(e *IteratorScope, where string) {
	l.control(e.Control, where)
	if !l.intrinsics[types.CoroutineWithName] || l.defName != types.CoroutineWithName {
		l.errorf("%s: coroutine scope outside Coroutine.with", where)
	}
	if e.Scope == 0 || l.scopeIDs[e.Scope] {
		l.errorf("%s: invalid or reused coroutine owner", where)
	}
	l.scopeIDs[e.Scope] = true
	if e.Producer == nil || e.Consumer == nil {
		l.errorf("%s: missing coroutine callbacks", where)
		return
	}
	if err := CheckCoroutineProducer(e.CursorTy, e.Producer.Type()); err != nil {
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

func (l *linter) coroutineAdvance(e *IteratorNext, where string) {
	name := types.CoroutineAdvanceName
	if e.Close {
		name = types.CoroutineCloseName
	}
	work := l.defName == types.WorkAdvanceName && !e.Close || l.defName == types.WorkCloseName && e.Close
	if work {
		name = l.defName
	}
	if !l.intrinsics[name] || l.defName != name {
		l.errorf("%s: coroutine operation outside its declared intrinsic", where)
	}
	if e.Access != types.ExclusiveAdvance {
		l.errorf("%s: coroutine operation lacks exclusive access proof", where)
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

func (f *flowChecker) requireControl(call *flowContext, owner int, operation string) {
	if f.collectControlNeed != nil {
		for _, row := range call.sourceRows {
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
