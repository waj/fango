package eval

import (
	"fmt"
	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

func (in *interp) openCoroutine(p *machineir.Prog, producer *Closure, row *fangort.EvidenceRow) *MachineIteratorSession {
	if producer == nil {
		return newCoroutineScope(row)
	}
	owner := fangort.NewYieldOwner()
	evidence := fangort.NewCursorEvidence(row)
	callEvidence := cloneEvidence(in.evidence)
	it := &MachineIteratorSession{owner: owner, evidence: evidence}
	it.start = func(input Value) (*MachineSession, error) {
		pause := &Closure{pauseOwner: owner, control: types.Control{Transport: types.Machine}}
		value, err := in.callClosure(producer, pause)
		if err != nil {
			return nil, err
		}
		body, ok := value.(*Closure)
		if !ok {
			return nil, fmt.Errorf("eval: coroutine factory did not return a callback")
		}
		return in.startMachineClosure(p, body.machine, input, callEvidence, evidence.Row())
	}
	return it
}

func (it *MachineIteratorSession) begin(input Value) error {
	if it.start != nil {
		start := it.start
		it.start = nil
		session, err := start(input)
		if err != nil {
			it.done = true
			it.unlink()
			it.evidence.Clear()
			it.clearRegistered()
			it.evidence.Clear()
			return err
		}
		it.session = session
	}
	if it.session == nil {
		return fmt.Errorf("eval: coroutine has no producer")
	}
	return nil
}

func (in *interp) evalCoroutineScope(scope *core.CoroutineScope, fr *Frame) (Value, error) {
	value, err := in.eval(scope.Producer, fr)
	if err != nil {
		return nil, err
	}
	if _, ok := asExit(value); ok {
		return value, nil
	}
	producer, ok := value.(*Closure)
	if !ok && !types.CoroutineScopeType(scope.CursorTy) {
		return nil, fmt.Errorf("eval: coroutine producer is not a callback")
	}
	row, err := in.argumentRow(scope.Row, fr)
	if err != nil {
		return nil, err
	}
	cursor := in.openCoroutine(in.env.machine, producer, row)
	value, err = in.eval(scope.Consumer, fr)
	if err != nil {
		return nil, err
	}
	if _, ok := asExit(value); ok {
		return value, nil
	}
	driver, ok := value.(*Closure)
	if !ok {
		return nil, fmt.Errorf("eval: coroutine driver is not a callback")
	}
	session, err := in.startMachineClosure(in.env.machine, driver.machine, cursor, in.evidence, row)
	if err != nil {
		return nil, err
	}
	session.cleanups = append(session.cleanups, machineCleanupEntry{sync: cursor.Close})
	event, err := session.Run()
	if err != nil {
		return nil, err
	}
	if !event.Done {
		_, closeErr := session.Abandon()
		return nil, fmt.Errorf("eval: foreign suspension escaped synchronous coroutine boundary (cleanup: %v)", closeErr)
	}
	if event.Exit != nil {
		return event.Exit, nil
	}
	return event.Value, nil
}
