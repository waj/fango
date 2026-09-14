package eval

import (
	"context"
	"fmt"

	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
)

// MachineIteratorSession is the interpreter-side owner for E8's private pull
// protocol. Source construction remains disabled until cursor ownership is
// represented by capture analysis.
type MachineIteratorSession struct {
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
		return nil, false, nil, err
	}
	if !event.Done {
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
	producer, ok := producerValue.(*machineClosure)
	if !ok {
		return nil, fmt.Errorf("eval: iterator producer is %T, want lowered Machine callback", producerValue)
	}
	if in.env.machine == nil {
		return nil, fmt.Errorf("eval: iterator owner has no installed Machine lowering")
	}
	machineSession, err := startMachineClosure(in.ctx, in.env.machine, producer, struct{}{}, in.evidence, in.env, in.ioctx)
	if err != nil {
		return nil, err
	}
	iteratorSession := &MachineIteratorSession{session: machineSession}

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
				saved := in.evidence
				in.evidence = cloneEvidence(consumer.Evidence)
				result, consumerErr = in.eval(consumer.Body, &Frame{parent: consumer.Env, vars: map[string]Value{consumer.Param: iteratorSession}})
				in.evidence = saved
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
		saved := in.evidence
		in.evidence = cloneEvidence(action.Evidence)
		result, err := in.eval(action.Body, &Frame{parent: action.Env, vars: map[string]Value{action.Param: value}})
		in.evidence = saved
		if err != nil {
			return nil, err
		}
		if _, exits := asExit(result); exits {
			return result, nil
		}
	}
}
