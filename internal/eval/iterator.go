package eval

import (
	"errors"
	"fmt"

	"github.com/waj/fango/runtime/fangort"
)

// MachineIteratorSession owns one interpreter-side producer traversal. Source
// cursor lifetimes and access are checked by capture contracts before execution.
type MachineIteratorSession struct {
	registered                   bool
	work                         *workOwner
	registry                     bool
	parent, previous, next, last *MachineIteratorSession
	start                        func(Value) (*MachineSession, error)
	evidence                     *fangort.CursorEvidence
	busy                         bool
	owner                        *fangort.YieldOwner
	session                      *MachineSession
	closeSession                 *MachineSession
	started                      bool
	polled                       bool
	poll                         *evalPollBudget
	done                         bool
	closing                      bool
	stopped                      bool
	reportStop                   bool
	stopExit                     *ExitRequest
}

func (it *MachineIteratorSession) finishClose() {
	it.done, it.closing, it.busy = true, false, false
	it.closeSession = nil
	it.unlink()
	it.evidence.Clear()
	it.clearRegistered()
}

func (it *MachineIteratorSession) Close() (*ExitRequest, error) {
	if it == nil || it.done {
		return nil, nil
	}
	if it.busy {
		return nil, fmt.Errorf("eval: overlapping coroutine close")
	}
	it.done = true
	defer it.clearRegistered()
	it.unlink()
	if it.registry {
		it.work.closed = true
		defer it.evidence.Clear()
		var primary *ExitRequest
		var failure error
		for it.last != nil {
			child := it.last
			exit, err := child.Close()
			primary = suppress(primary, exit)
			failure = errors.Join(failure, err)
			// A corrupt overlapping close must not trap the cleanup driver.
			child.unlink()
		}
		return primary, failure
	}
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

func (it *MachineIteratorSession) unlink() {
	if it.parent == nil {
		return
	}
	if it.previous != nil {
		it.previous.next = it.next
	}
	if it.next != nil {
		it.next.previous = it.previous
	} else {
		it.parent.last = it.previous
	}
	it.parent, it.previous, it.next = nil, nil, nil
}

func newCoroutineScope(row *fangort.EvidenceRow) *MachineIteratorSession {
	return &MachineIteratorSession{registry: true, work: &workOwner{}, evidence: fangort.NewCursorEvidence(row)}
}
func registerCoroutine(scope, child *MachineIteratorSession) (*MachineIteratorSession, error) {
	if scope == nil || !scope.registry || scope.done {
		return nil, fmt.Errorf("coroutine allocation requires a live scope")
	}
	child.registered = true
	child.parent, child.previous = scope, scope.last
	if scope.last != nil {
		scope.last.next = child
	}
	scope.last = child
	return child, nil
}

// Dynamic terminal handles do not retain even an empty execution session (and,
// in the interpreter, its environment). Lexical host cursors retain statistics.
func (it *MachineIteratorSession) clearRegistered() {
	if it.registered {
		it.session = nil
		it.start = nil
		it.owner = nil
		it.evidence.Clear()
	}
}
