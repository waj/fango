package fangort

// WorkOwner is the identity of one lexical effect budget. Its address cannot be
// confused with the identity of another simultaneously live, equal-budget owner.
type WorkOwner struct{ closed bool }
type WorkPackage struct {
	owner  *WorkOwner
	cursor *MachineIterator
}

func NewWorkOwner() *WorkOwner             { return &WorkOwner{} }
func CloseWorkOwner(owner *WorkOwner) Unit { owner.closed = true; return Unit{} }
func PackWork(owner *WorkOwner, cursor *MachineIterator) *WorkPackage {
	if owner == nil || owner.closed {
		panic("work package has no live owner")
	}
	if cursor.poll == nil {
		cursor.poll = &pollBudget{}
	}
	return &WorkPackage{owner: owner, cursor: cursor}
}
func OpenWork(owner *WorkOwner, work *WorkPackage) *MachineIterator {
	if owner == nil || owner.closed || work == nil || work.owner != owner {
		panic("work package belongs to a different owner")
	}
	return work.cursor
}

func OpenWorkForStop(owner *WorkOwner, work *WorkPackage) *MachineIterator {
	it := OpenWork(owner, work)
	it.reportStop = true
	return it
}

func WorkStopCompletion(owner *WorkOwner, work *WorkPackage) Completion[Unit] {
	it := OpenWork(owner, work)
	if !it.done {
		panic("work stop is not complete")
	}
	if it.stopFailure != nil {
		return CompletionFromFailure[Unit](it.stopFailure)
	}
	return CaptureCompletion(Normal(Unit{}))
}
