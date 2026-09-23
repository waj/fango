package eval

import (
	"context"
	"errors"
	"fmt"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/runtime/fangort"
)

// Host-driven fixture adapters; source programs use typed coroutine exchange.
func StartMachineIterator(ctx context.Context, p *machineir.Prog, entry string, args []Value, env *Env, ioctx *IOContext) (*MachineIteratorSession, error) {
	session, err := StartMachine(ctx, p, entry, args, env, ioctx)
	if err != nil {
		return nil, err
	}
	return &MachineIteratorSession{session: session}, nil
}

func (it *MachineIteratorSession) Next() (value Value, yielded bool, exit *ExitRequest, err error) {
	return it.NextWithEvidence(nil)
}

func (it *MachineIteratorSession) NextWithEvidence(row *fangort.EvidenceRow) (value Value, yielded bool, exit *ExitRequest, err error) {
	if it != nil && it.busy {
		return nil, false, nil, fmt.Errorf("eval: overlapping cursor advancement")
	}
	if it == nil || it.session == nil {
		return nil, false, nil, fmt.Errorf("eval: iterator has no machine")
	}
	if it.done {
		return nil, false, nil, nil
	}
	it.evidence.Bind(row)
	defer func() {
		if it.done {
			it.evidence.Clear()
		} else {
			it.evidence.Restore()
		}
	}()
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
