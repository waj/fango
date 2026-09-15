package eval

import (
	"context"
	"errors"
	"fmt"

	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/runtime/fangort"
)

// MachineIteratorSession owns one interpreter-side producer traversal. Source
// cursor lifetimes and access are checked by capture contracts before execution.
type MachineIteratorSession struct {
	busy    bool
	owner   *fangort.YieldOwner
	session *MachineSession
	started bool
	done    bool
}

func StartMachineIterator(ctx context.Context, p *machineir.Prog, entry string, args []Value, env *Env, ioctx *IOContext) (*MachineIteratorSession, error) {
	session, err := StartMachine(ctx, p, entry, args, env, ioctx)
	if err != nil {
		return nil, err
	}
	return &MachineIteratorSession{session: session}, nil
}

func (it *MachineIteratorSession) Next() (value Value, yielded bool, exit *ExitRequest, err error) {
	if it != nil && it.busy {
		return nil, false, nil, fmt.Errorf("eval: overlapping cursor advancement")
	}
	if it == nil || it.session == nil {
		return nil, false, nil, fmt.Errorf("eval: iterator has no machine")
	}
	if it.done {
		return nil, false, nil, nil
	}
	var event MachineEvent
	if it.started {
		event, err = it.session.Resume(struct{}{})
	} else {
		it.started = true
		event, err = it.session.Run()
	}
	if err != nil {
		it.done = true
		return nil, false, event.Exit, err
	}
	if !event.Done {
		if event.Owner != it.owner {
			exit, closeErr := it.Close()
			return nil, false, exit, errors.Join(fmt.Errorf("eval: suspension reached a different cursor owner"), closeErr)
		}
		return event.Request, true, nil, nil
	}
	it.done = true
	return nil, false, event.Exit, nil
}

func (it *MachineIteratorSession) Close() (*ExitRequest, error) {
	if it == nil || it.session == nil || it.done {
		return nil, nil
	}
	it.done = true
	return it.session.Abandon()
}

func (it *MachineIteratorSession) Stats() MachineStats {
	if it == nil || it.session == nil {
		return MachineStats{}
	}
	return it.session.Stats()
}

func (in *interp) evalIteratorScope(scope *core.IteratorScope, fr *Frame) (Value, error) {
	producerValue, err := in.eval(scope.Producer, fr)
	if err != nil {
		return nil, err
	}
	if _, exits := asExit(producerValue); exits {
		return producerValue, nil
	}
	producer, ok := producerValue.(*Closure)
	if !ok || producer.machine == nil {
		return nil, fmt.Errorf("eval: iterator producer is %T, want lowered Machine callback", producerValue)
	}
	if in.env.machine == nil {
		return nil, fmt.Errorf("eval: iterator owner has no installed Machine lowering")
	}
	callEvidence := cloneEvidence(in.evidence)
	var owner *fangort.YieldOwner
	if scope.Yield.Unique != 0 {
		owner = fangort.NewYieldOwner()
		callEvidence[scope.Yield.Unique] = &evidence{yieldOwner: owner}
	}
	machineSession, err := in.startMachineClosure(in.env.machine, producer.machine, struct{}{}, callEvidence)
	if err != nil {
		return nil, err
	}
	iteratorSession := &MachineIteratorSession{owner: owner, session: machineSession}

	consumerValue, consumerErr := in.eval(scope.Consumer, fr)
	var result Value
	if consumerErr == nil {
		if exit, exits := asExit(consumerValue); exits {
			result = exit
		} else {
			consumer, ok := consumerValue.(*Closure)
			if !ok {
				consumerErr = fmt.Errorf("eval: iterator consumer is %T, want Direct/Exit callback", consumerValue)
			} else {
				result, consumerErr = in.callClosure(consumer, iteratorSession)
			}
		}
	}
	closeExit, closeErr := iteratorSession.Close()
	if consumerErr != nil {
		return nil, consumerErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if consumerExit, exits := asExit(result); exits {
		return suppress(consumerExit, closeExit), nil
	}
	if closeExit != nil {
		return closeExit, nil
	}
	return result, nil
}

func (in *interp) evalIteratorForEach(each *core.IteratorForEach, fr *Frame) (Value, error) {
	actionValue, err := in.eval(each.Action, fr)
	if err != nil {
		return nil, err
	}
	if _, exits := asExit(actionValue); exits {
		return actionValue, nil
	}
	action, ok := actionValue.(*Closure)
	if !ok {
		return nil, fmt.Errorf("eval: iterator forEach action is %T, want callback", actionValue)
	}
	cursorValue, err := in.eval(each.Cursor, fr)
	if err != nil {
		return nil, err
	}
	if _, exits := asExit(cursorValue); exits {
		return cursorValue, nil
	}
	cursor, ok := cursorValue.(*MachineIteratorSession)
	if !ok {
		return nil, fmt.Errorf("eval: iterator forEach cursor is %T, want owned iterator", cursorValue)
	}
	for {
		value, yielded, exit, err := cursor.Next()
		if err != nil {
			return nil, err
		}
		if exit != nil {
			return exit, nil
		}
		if !yielded {
			return struct{}{}, nil
		}
		result, err := in.callClosure(action, value)
		if err != nil {
			return nil, err
		}
		if _, exits := asExit(result); exits {
			return result, nil
		}
	}
}

func (in *interp) evalIteratorFold(fold *core.IteratorFold, fr *Frame) (Value, error) {
	combineValue, err := in.eval(fold.Combine, fr)
	if err != nil {
		return nil, err
	}
	if _, exits := asExit(combineValue); exits {
		return combineValue, nil
	}
	combine, ok := combineValue.(*Closure)
	if !ok {
		return nil, fmt.Errorf("eval: iterator fold combine is %T, want callback", combineValue)
	}
	accumulator, err := in.eval(fold.Initial, fr)
	if err != nil {
		return nil, err
	}
	if _, exits := asExit(accumulator); exits {
		return accumulator, nil
	}
	cursorValue, err := in.eval(fold.Cursor, fr)
	if err != nil {
		return nil, err
	}
	if _, exits := asExit(cursorValue); exits {
		return cursorValue, nil
	}
	cursor, ok := cursorValue.(*MachineIteratorSession)
	if !ok {
		return nil, fmt.Errorf("eval: iterator fold cursor is %T, want owned iterator", cursorValue)
	}
	for {
		value, yielded, exit, err := cursor.Next()
		if err != nil {
			return nil, err
		}
		if exit != nil {
			return exit, nil
		}
		if !yielded {
			return accumulator, nil
		}
		stepValue, err := in.callClosure(combine, value)
		if err != nil {
			return nil, err
		}
		if _, exits := asExit(stepValue); exits {
			return stepValue, nil
		}
		step, ok := stepValue.(*Closure)
		if !ok {
			return nil, fmt.Errorf("eval: iterator fold first callback application returned %T, want callback", stepValue)
		}
		accumulator, err = in.callClosure(step, accumulator)
		if err != nil {
			return nil, err
		}
		if _, exits := asExit(accumulator); exits {
			return accumulator, nil
		}
	}
}

func (in *interp) callClosure(closure *Closure, arg Value) (Value, error) {
	saved := in.evidence
	in.evidence = cloneEvidence(closure.Evidence)
	result, err := in.eval(closure.Body, &Frame{parent: closure.Env, vars: map[string]Value{closure.Param: arg}})
	in.evidence = saved
	return result, err
}
