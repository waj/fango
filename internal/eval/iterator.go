package eval

import (
	"fmt"

	"github.com/waj/fango/runtime/fangort"
)

// MachineIteratorSession owns one interpreter-side producer traversal. Source
// cursor lifetimes and access are checked by capture contracts before execution.
type MachineIteratorSession struct {
	start    func(Value) (*MachineSession, error)
	evidence *fangort.CursorEvidence
	busy     bool
	owner    *fangort.YieldOwner
	session  *MachineSession
	started  bool
	done     bool
}

func (it *MachineIteratorSession) Close() (*ExitRequest, error) {
	if it == nil || it.done {
		return nil, nil
	}
	if it.busy {
		return nil, fmt.Errorf("eval: overlapping coroutine close")
	}
	it.done = true
	it.start = nil
	if it.session == nil {
		it.evidence.Clear()
		return nil, nil
	}
	it.evidence.Restore()
	exit, err := it.session.Abandon()
	it.evidence.Clear()
	return exit, err
}

func (it *MachineIteratorSession) Stats() MachineStats {
	if it == nil || it.session == nil {
		return MachineStats{}
	}
	return it.session.Stats()
}

func (in *interp) callClosure(closure *Closure, arg Value) (Value, error) {
	saved := in.evidence
	in.evidence = cloneEvidence(closure.Evidence)
	result, err := in.eval(closure.Body, &Frame{parent: closure.Env, vars: map[string]Value{closure.Param: arg}})
	in.evidence = saved
	return result, err
}
