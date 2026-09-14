package eval

import (
	"context"

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
