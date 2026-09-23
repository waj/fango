package fangort

import (
	"reflect"
	"testing"
)

type foreignYieldFrame struct {
	pc         int
	outer, own *YieldOwner
	log        *[]string
}

func (f *foreignYieldFrame) Step(m *Machine) MachineStep {
	f.pc++
	switch f.pc {
	case 1:
		log := f.log
		m.PushCleanup(func() *ExitRequest { *log = append(*log, "inner"); return nil })
		return MachineStep{Kind: MachineSuspend, Owner: f.outer, Request: 10}
	case 2:
		m.TakeResult()
		return MachineStep{Kind: MachineSuspend, Owner: f.own, Request: 20}
	default:
		m.TakeResult()
		return MachineStep{Kind: MachineReturn, Value: UnitValue}
	}
}
func (f *foreignYieldFrame) Clear() { *f = foreignYieldFrame{} }

type forwardingFrame struct {
	pc    int
	owner *YieldOwner
	input *MachineIterator
	log   *[]string
	row   *EvidenceRow
}

func (f *forwardingFrame) Step(m *Machine) MachineStep {
	switch f.pc {
	case 0:
		if f.log != nil {
			log := f.log
			m.PushCleanup(func() *ExitRequest { *log = append(*log, "outer"); return nil })
		}
		input := f.input
		m.PushCleanup(func() *ExitRequest { return CloseMachineIterator(input) })
		f.pc = 1
		return MachineStep{Kind: MachineAdvance, Cursor: f.input, Reply: UnitValue, Evidence: f.row}
	case 1:
		result := m.TakeResult().(CursorResult)
		if result.Exit != nil {
			return MachineStep{Kind: MachineExit, Exit: result.Exit}
		}
		if !result.Present {
			return MachineStep{Kind: MachineReturn, Value: UnitValue}
		}
		f.pc = 2
		return MachineStep{Kind: MachineSuspend, Owner: f.owner, Request: result.Value}
	default:
		m.TakeResult()
		f.pc = 1
		return MachineStep{Kind: MachineAdvance, Cursor: f.input, Reply: UnitValue, Evidence: f.row}
	}
}
func (f *forwardingFrame) Clear() { *f = forwardingFrame{} }

type collectingFrame struct {
	pc, limit int
	input     *MachineIterator
	values    *[]int
	check     func()
}

func (f *collectingFrame) Step(m *Machine) MachineStep {
	if f.pc == 0 {
		input := f.input
		m.PushCleanup(func() *ExitRequest { return CloseMachineIterator(input) })
		f.pc = 1
	} else {
		result := m.TakeResult().(CursorResult)
		if result.Exit != nil {
			return MachineStep{Kind: MachineExit, Exit: result.Exit}
		}
		if !result.Present {
			return MachineStep{Kind: MachineReturn, Value: len(*f.values)}
		}
		*f.values = append(*f.values, result.Value.(int))
		if f.check != nil {
			f.check()
		}
		if len(*f.values) == f.limit {
			return MachineStep{Kind: MachineReturn, Value: f.limit}
		}
	}
	return MachineStep{Kind: MachineAdvance, Cursor: f.input, Reply: UnitValue}
}
func (f *collectingFrame) Clear() { *f = collectingFrame{} }

func TestCursorTransfersRouteForeignYieldAndRetainBorrow(t *testing.T) {
	for _, stop := range []int{1, 2, 3} {
		var log []string
		outerOwner, innerOwner := NewYieldOwner(), NewYieldOwner()
		inner := StartOwnedMachineIterator(innerOwner, &foreignYieldFrame{outer: outerOwner, own: innerOwner, log: &log})
		outer := StartOwnedMachineIterator(outerOwner, &forwardingFrame{owner: outerOwner, input: inner, log: &log})
		var values []int
		root := StartMachine(&collectingFrame{input: outer, values: &values, limit: stop, check: func() {
			if len(values) == 1 && !inner.busy {
				t.Fatal("foreign yield released unfinished inner advancement")
			}
			if len(values) == 2 && inner.busy {
				t.Fatal("completed inner advancement kept its borrow")
			}
		}})
		event, err := root.Run()
		want := []int{10, 20}
		if stop == 1 {
			want = want[:1]
		}
		if err != nil || !event.Done || !reflect.DeepEqual(values, want) {
			t.Fatalf("stop %d: event=%+v values=%v err=%v", stop, event, values, err)
		}
		if !reflect.DeepEqual(log, []string{"inner", "outer"}) || inner.busy || outer.busy {
			t.Fatalf("stop %d: cleanup=%v borrows=%v/%v", stop, log, inner.busy, outer.busy)
		}
		if root.Stats().MaxPullDepth != 2 {
			t.Fatalf("pull depth: %+v", root.Stats())
		}
	}
}

func TestCursorTransfersUseBoundedDispatchStorage(t *testing.T) {
	closed := 0
	owner := NewYieldOwner()
	cursor := StartOwnedMachineIterator(owner, &iteratorFixtureFrame{owner: owner, limit: 100000, cleanup: &closed})
	var cursors []*MachineIterator
	cursors = append(cursors, cursor)
	for range 5 {
		owner = NewYieldOwner()
		cursor = StartOwnedMachineIterator(owner, &forwardingFrame{owner: owner, input: cursor})
		cursors = append(cursors, cursor)
	}
	var values []int
	root := StartMachine(&collectingFrame{input: cursor, values: &values, limit: 100001})
	event, err := root.Run()
	if err != nil || !event.Done || event.Value != 100000 || closed != 1 {
		t.Fatalf("completion: %+v %v closed=%d", event, err, closed)
	}
	if root.Stats().MaxPullDepth != len(cursors) {
		t.Fatalf("pull stack grew: %+v", root.Stats())
	}
	for _, cursor := range cursors {
		if cursor.busy || !cursor.done || len(cursor.machine.frames) != 0 || cursor.machine.traversal != nil || cursor.Stats().MaxFrameCap != 1 {
			t.Fatalf("cursor retained live dispatch state: %+v %+v", cursor, cursor.Stats())
		}
	}
}
