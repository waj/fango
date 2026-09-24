package fangort

import "testing"

type startCaller struct {
	start   MachineStart
	waiting bool
	cleared bool
}

func (f *startCaller) Step(m *Machine) MachineStep {
	if !f.waiting {
		f.waiting = true
		return StartCall(f.start, true)
	}
	return MachineStep{Kind: MachineReturn, Value: m.TakeResult()}
}
func (f *startCaller) Clear() { f.start = MachineStart{}; f.cleared = true }

func TestImmediateStartIsLazyAndDoesNotPushFrame(t *testing.T) {
	calls := 0
	f := &startCaller{start: ImmediateStart(func() (any, *ExitRequest) { calls++; return 42, nil })}
	m := StartMachine(f)
	if calls != 0 {
		t.Fatal("factory ran user code")
	}
	e, err := m.Run()
	if err != nil || !e.Done || e.Value != 42 || calls != 1 || !f.cleared || m.Stats().MaxDepth != 1 || m.pendingRun != nil {
		t.Fatalf("completion: %#v %v calls=%d stats=%#v", e, err, calls, m.Stats())
	}
	if m.Stats().Steps != 3 {
		t.Fatalf("lost dispatcher boundary: %#v", m.Stats())
	}
}

func TestPauseStartReplyAndAbandonment(t *testing.T) {
	for _, abandon := range []bool{false, true} {
		owner := NewYieldOwner()
		f := &startCaller{start: PauseStart(owner, 42)}
		m := StartMachine(f)
		cleanups := 0
		m.PushCleanup(func() *ExitRequest { cleanups++; return nil })
		e, err := m.Run()
		if err != nil || e.Done || e.Owner != owner || e.Request != 42 || cleanups != 0 || m.Stats().MaxDepth != 1 {
			t.Fatalf("pause: %#v %v", e, err)
		}
		if abandon {
			if exit, err := m.Abandon(); exit != nil || err != nil {
				t.Fatalf("abandon: %v %v", exit, err)
			}
		} else {
			e, err = m.Resume(73)
			if err != nil || !e.Done || e.Value != 73 {
				t.Fatalf("reply: %#v %v", e, err)
			}
		}
		if cleanups != 1 || !f.cleared {
			t.Fatalf("cleanup=%d cleared=%v", cleanups, f.cleared)
		}
	}
}

func TestImmediateStartExitUnwindsAndClears(t *testing.T) {
	failure := &ExitRequest{}
	f := &startCaller{start: ImmediateStart(func() (any, *ExitRequest) { return nil, failure })}
	m := StartMachine(f)
	cleanups := 0
	m.PushCleanup(func() *ExitRequest { cleanups++; return nil })
	e, err := m.Run()
	if err != nil || !e.Done || e.Exit != failure || cleanups != 1 || !f.cleared || m.pendingRun != nil {
		t.Fatalf("exit: %#v %v cleanup=%d", e, err, cleanups)
	}
}
