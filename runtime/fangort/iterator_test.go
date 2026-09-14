package fangort

import "testing"

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
