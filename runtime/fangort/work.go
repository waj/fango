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
	return &WorkPackage{owner: owner, cursor: cursor}
}
func OpenWork(owner *WorkOwner, work *WorkPackage) *MachineIterator {
	if owner == nil || owner.closed || work == nil || work.owner != owner {
		panic("work package belongs to a different owner")
	}
	return work.cursor
}
