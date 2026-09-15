package fangort

import (
	"reflect"
	"testing"
)

type iteratorFixtureFrame struct {
	pc        uint8
	next      int
	limit     int
	cleanup   *int
	installed bool
}

func (f *iteratorFixtureFrame) Step(m *Machine) MachineStep {
	if !f.installed {
		f.installed = true
		cleanup := f.cleanup
		m.PushCleanup(func() *ExitRequest { *cleanup++; return nil })
	}
	if f.pc == 1 {
		_ = m.TakeResult().(Unit)
		f.pc = 0
	}
	if f.next == f.limit {
		return MachineStep{Kind: MachineReturn, Value: UnitValue}
	}
	value := f.next
	f.next++
	f.pc = 1
	return MachineStep{Kind: MachineSuspend, Request: value}
}

func (f *iteratorFixtureFrame) Clear() { f.cleanup = nil }

func TestMachineIteratorPullsAndClosesEarly(t *testing.T) {
	closed := 0
	it := StartMachineIterator(&iteratorFixtureFrame{limit: 10000, cleanup: &closed})
	for want := range 3 {
		value, yielded, exit, err := it.Next()
		if err != nil || !yielded || exit != nil || value != want {
			t.Fatalf("next %d = %#v, %v/%v/%v", want, value, yielded, exit, err)
		}
	}
	if exit, err := it.Close(); err != nil || exit != nil || closed != 1 {
		t.Fatalf("close = %#v, %v; cleanup count %d", exit, err, closed)
	}
	if exit, err := it.Close(); err != nil || exit != nil || closed != 1 {
		t.Fatalf("second close = %#v, %v; cleanup count %d", exit, err, closed)
	}
	if stats := it.Stats(); stats.MaxDepth != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestMachineIteratorExhaustionClosesExactlyOnce(t *testing.T) {
	closed := 0
	it := StartMachineIterator(&iteratorFixtureFrame{limit: 2, cleanup: &closed})
	for want := range 2 {
		value, yielded, exit, err := it.Next()
		if err != nil || !yielded || exit != nil || value != want {
			t.Fatalf("next %d = %#v, %v/%v/%v", want, value, yielded, exit, err)
		}
	}
	value, yielded, exit, err := it.Next()
	if err != nil || yielded || exit != nil || value != nil || closed != 1 {
		t.Fatalf("done = %#v, %v/%v/%v; cleanup count %d", value, yielded, exit, err, closed)
	}
}

type iteratorStepFrame struct{ step func(*Machine) MachineStep }

func (f *iteratorStepFrame) Step(m *Machine) MachineStep { return f.step(m) }
func (f *iteratorStepFrame) Clear()                      { f.step = nil }

func TestMachineIteratorFailureAndCleanupOrdering(t *testing.T) {
	for _, bodyFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup failure", true: "body failure"}[bodyFails], func(t *testing.T) {
			primary := &ExitRequest{Effect: "Body", Payload: []any{"body"}}
			inner := &ExitRequest{Effect: "Inner", Suppressed: []*ExitRequest{{Effect: "Nested"}}}
			outer := &ExitRequest{Effect: "Outer"}
			var order []string
			frame := &iteratorStepFrame{step: func(m *Machine) MachineStep {
				m.PushCleanup(func() *ExitRequest { order = append(order, "outer"); return outer })
				m.PushCleanup(func() *ExitRequest { order = append(order, "inner"); return inner })
				if bodyFails {
					return MachineStep{Kind: MachineExit, Exit: primary}
				}
				return MachineStep{Kind: MachineReturn, Value: UnitValue}
			}}
			it := StartMachineIterator(frame)
			_, yielded, exit, err := it.Next()
			if yielded || err != nil || exit == nil {
				t.Fatalf("next: %v, %#v, %v", yielded, exit, err)
			}
			if bodyFails {
				if exit.Effect != "Body" || !reflect.DeepEqual(exit.Suppressed, []*ExitRequest{inner, outer}) {
					t.Fatalf("body primary = %#v", exit)
				}
			} else if exit.Effect != "Inner" || !reflect.DeepEqual(exit.Suppressed, []*ExitRequest{inner.Suppressed[0], outer}) {
				t.Fatalf("cleanup primary = %#v", exit)
			}
			if len(primary.Suppressed) != 0 || len(inner.Suppressed) != 1 {
				t.Fatal("failure packaging mutated a forwarded failure")
			}
			if !reflect.DeepEqual(order, []string{"inner", "outer"}) || frame.step != nil {
				t.Fatalf("cleanup order = %v; cleared frame = %v", order, frame.step == nil)
			}
			for range 3 {
				value, yielded, exit, err := it.Next()
				if value != nil || yielded || exit != nil || err != nil {
					t.Fatalf("read after failure: %#v, %v, %#v, %v", value, yielded, exit, err)
				}
			}
			if exit, err := it.Close(); exit != nil || err != nil || len(order) != 2 {
				t.Fatalf("close after failure: %#v, %v, order %v", exit, err, order)
			}
		})
	}
}

func TestMachineIteratorProtocolFailureClosesProduction(t *testing.T) {
	closed := 0
	cleanupFailure := &ExitRequest{Effect: "Cleanup"}
	frame := &iteratorStepFrame{step: func(m *Machine) MachineStep {
		m.PushCleanup(func() *ExitRequest { closed++; return cleanupFailure })
		return MachineStep{Kind: MachineCall} // malformed generated frame
	}}
	it := StartMachineIterator(frame)
	_, yielded, exit, err := it.Next()
	if yielded || err == nil || exit != cleanupFailure || closed != 1 || frame.step != nil {
		t.Fatalf("malformed pull: %v, %#v, %v; closed %d", yielded, exit, err, closed)
	}
	if !it.machine.finished || len(it.machine.frames)+len(it.machine.cleanups)+len(it.machine.handlers)+len(it.machine.states) != 0 {
		t.Fatal("failed production retained live state")
	}
	if value, yielded, exit, err := it.Next(); value != nil || yielded || exit != nil || err != nil {
		t.Fatalf("read after protocol failure: %#v, %v, %#v, %v", value, yielded, exit, err)
	}
}

func TestMachineIteratorCloseBeforeFirstPullStartsNothing(t *testing.T) {
	started := 0
	frame := &iteratorStepFrame{step: func(*Machine) MachineStep {
		started++
		return MachineStep{Kind: MachineReturn, Value: UnitValue}
	}}
	it := StartMachineIterator(frame)
	if exit, err := it.Close(); exit != nil || err != nil || started != 0 || frame.step != nil {
		t.Fatalf("unstarted close: %#v, %v; started %d", exit, err, started)
	}
}

func TestMachineIteratorLongTraversalKeepsBoundedFrames(t *testing.T) {
	closed := 0
	it := StartMachineIterator(&iteratorFixtureFrame{limit: 100000, cleanup: &closed})
	for want := range 100000 {
		value, yielded, exit, err := it.Next()
		if value != want || !yielded || exit != nil || err != nil {
			t.Fatalf("pull %d: %#v, %v, %#v, %v", want, value, yielded, exit, err)
		}
	}
	for range 3 {
		value, yielded, exit, err := it.Next()
		if value != nil || yielded || exit != nil || err != nil || closed != 1 {
			t.Fatalf("exhausted pull: %#v, %v, %#v, %v; closed %d", value, yielded, exit, err, closed)
		}
	}
	if stats := it.Stats(); stats.MaxDepth != 1 || stats.MaxFrameCap != 1 || stats.MaxCleanups != 1 {
		t.Fatalf("live storage grew with traversal: %+v", stats)
	}
}
