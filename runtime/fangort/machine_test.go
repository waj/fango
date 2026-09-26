package fangort

import "testing"

type cpuFrame struct{ remaining int }

func (f *cpuFrame) Step(*Machine) MachineStep {
	if f.remaining == 0 {
		return MachineStep{Kind: MachineReturn, Value: 1}
	}
	f.remaining--
	return MachineStep{Kind: MachineContinue}
}

func (f *cpuFrame) Clear() { f.remaining = 0 }

func TestMachinePollRetainsCPUFrame(t *testing.T) {
	frame := &cpuFrame{remaining: 600}
	m := StartMachine(frame)
	budget := &pollBudget{}
	m.poll = budget
	for _, remaining := range []int{345, 90} {
		event, err := m.Run()
		if err != nil || event.poll != budget || event.Done || frame.remaining != remaining {
			t.Fatalf("poll = %#v, %v; remaining = %d, want %d", event, err, frame.remaining, remaining)
		}
	}
	event, err := m.Run()
	if err != nil || !event.Done || event.Value != 1 || budget.steps != 603 {
		t.Fatalf("completion = %#v, %v; steps = %d", event, err, budget.steps)
	}
	ordinary := StartMachine(&cpuFrame{remaining: 600})
	event, err = ordinary.Run()
	if err != nil || !event.Done || event.Value != 1 || event.poll != nil {
		t.Fatalf("ordinary completion = %#v, %v", event, err)
	}
}

type repeatFrame struct {
	pc        uint8
	remaining int
	value     int
	cleared   bool
}

func (f *repeatFrame) Step(m *Machine) MachineStep {
	for {
		switch f.pc {
		case 0:
			if f.remaining == 0 {
				return MachineStep{Kind: MachineReturn, Value: f.value}
			}
			f.pc = 1
			return MachineStep{Kind: MachineSuspend, Request: f.value}
		case 1:
			f.value = m.TakeResult().(int)
			f.remaining--
			f.pc = 0
		}
	}
}

func (f *repeatFrame) Clear() {
	f.cleared = true
	f.value = 0
}

type callFrame struct {
	pc      uint8
	child   MachineFrame
	result  int
	cleared bool
	tail    bool
}

func (f *callFrame) Step(m *Machine) MachineStep {
	if f.pc == 0 {
		f.pc = 1
		kind := MachineCall
		if f.tail {
			kind = MachineTailCall
		}
		return MachineStep{Kind: kind, Frame: f.child}
	}
	f.result = m.TakeResult().(int)
	return MachineStep{Kind: MachineReturn, Value: f.result}
}

func (f *callFrame) Clear() {
	f.cleared = true
	f.child = nil
}

func TestMachineRepeatedSuspensionKeepsOneFrame(t *testing.T) {
	frame := &repeatFrame{remaining: 3, value: 1}
	m := StartMachine(frame)
	event, err := m.Run()
	for _, want := range []int{1, 2, 3} {
		if err != nil || event.Done || event.Request != want {
			t.Fatalf("event = %#v, %v; want request %d", event, err, want)
		}
		event, err = m.Resume(want + 1)
	}
	if err != nil || !event.Done || event.Value != 4 {
		t.Fatalf("completion = %#v, %v", event, err)
	}
	if stats := m.Stats(); stats.MaxDepth != 1 {
		t.Fatalf("maximum depth = %d, want 1", stats.MaxDepth)
	}
	if !frame.cleared {
		t.Fatal("completed frame was not cleared")
	}
}

func TestMachineAbandonRunsPendingCleanupAndClearsFrames(t *testing.T) {
	frame := &repeatFrame{remaining: 2, value: 1}
	m := StartMachine(frame)
	want := &ExitRequest{Operation: 4}
	called := false
	m.PushCleanup(func() *ExitRequest { called = true; return want })
	event, err := m.Run()
	if err != nil || event.Done {
		t.Fatalf("suspension = %#v, %v", event, err)
	}
	exit, err := m.Abandon()
	if err != nil || exit != want || !called || !frame.cleared {
		t.Fatalf("abandon = %#v, %v, called=%v cleared=%v", exit, err, called, frame.cleared)
	}
	if _, err := m.Run(); err == nil {
		t.Fatal("abandoned machine remained runnable")
	}
}

type suspendingCleanupParent struct {
	phase int
	exit  *ExitRequest
}

func (f *suspendingCleanupParent) Step(m *Machine) MachineStep {
	if f.phase == 0 {
		f.phase = 1
		if f.exit != nil {
			return MachineStep{Kind: MachineExit, Exit: f.exit}
		}
		return MachineStep{Kind: MachinePopCleanup}
	}
	if exit := m.TakeResult(); exit != nil {
		return MachineStep{Kind: MachineExit, Exit: exit.(*ExitRequest)}
	}
	return MachineStep{Kind: MachineReturn, Value: 7}
}

func TestMachineAbortDrainsSuspendingReleasesInReverseOrder(t *testing.T) {
	primary := &ExitRequest{Operation: 1}
	m := StartMachine(&suspendingCleanupParent{exit: primary})
	order := []string{}
	for i, name := range []string{"outer", "inner"} {
		label := name
		value := i + 1
		m.PushMachineCleanup(func() *Machine {
			order = append(order, label+" start")
			return StartMachine(&repeatFrame{remaining: 1, value: value})
		})
	}
	event, err := m.Run()
	if err != nil || event.Done || event.Request != 2 || len(order) != 1 || order[0] != "inner start" || m.finished {
		t.Fatalf("inner drain: event=%#v err=%v order=%v finished=%v", event, err, order, m.finished)
	}
	event, err = m.Resume(2)
	if err != nil || event.Done || event.Request != 1 || len(order) != 2 || order[1] != "outer start" || m.finished {
		t.Fatalf("outer drain: event=%#v err=%v order=%v finished=%v", event, err, order, m.finished)
	}
	event, err = m.Resume(1)
	if err != nil || !event.Done || event.Exit != primary || !m.finished {
		t.Fatalf("abort after drain: event=%#v err=%v order=%v finished=%v", event, err, order, m.finished)
	}
}

func (f *suspendingCleanupParent) Clear() {}

func TestMachineReleaseCanSuspendBeforeScopeCompletes(t *testing.T) {
	parentFrame := &suspendingCleanupParent{}
	parent := StartMachine(parentFrame)
	release := &repeatFrame{remaining: 1, value: 3}
	started := 0
	parent.PushMachineCleanup(func() *Machine {
		started++
		return StartMachine(release)
	})
	event, err := parent.Run()
	if err != nil || event.Done || event.Request != 3 || started != 1 || parent.finished {
		t.Fatalf("release pause: event=%#v err=%v started=%d finished=%v", event, err, started, parent.finished)
	}
	event, err = parent.Resume(4)
	if err != nil || !event.Done || event.Value != 7 || !release.cleared || started != 1 {
		t.Fatalf("release completion: event=%#v err=%v cleared=%v started=%d parentPhase=%d", event, err, release.cleared, started, parentFrame.phase)
	}
}

func TestMachineCallAndTailCallManageExplicitStack(t *testing.T) {
	leaf := &repeatFrame{value: 9}
	caller := &callFrame{child: leaf}
	m := StartMachine(caller)
	event, err := m.Run()
	if err != nil || !event.Done || event.Value != 9 {
		t.Fatalf("ordinary call = %#v, %v", event, err)
	}
	if m.Stats().MaxDepth != 2 || !caller.cleared || !leaf.cleared {
		t.Fatalf("ordinary call stats/clears = %#v, %v/%v", m.Stats(), caller.cleared, leaf.cleared)
	}

	tailLeaf := &repeatFrame{value: 12}
	tailCaller := &callFrame{child: tailLeaf, tail: true}
	m = StartMachine(tailCaller)
	event, err = m.Run()
	if err != nil || !event.Done || event.Value != 12 {
		t.Fatalf("tail call = %#v, %v", event, err)
	}
	if m.Stats().MaxDepth != 1 || !tailCaller.cleared || !tailLeaf.cleared {
		t.Fatalf("tail call stats/clears = %#v, %v/%v", m.Stats(), tailCaller.cleared, tailLeaf.cleared)
	}
}

func TestImmediateMachineMatchesDirectAndExitCompletion(t *testing.T) {
	direct := StartMachine(ImmediateMachine(func() (any, *ExitRequest) { return int64(42), nil }))
	event, err := direct.Run()
	if err != nil || !event.Done || event.Value != int64(42) || event.Exit != nil {
		t.Fatalf("direct completion = %#v, %v", event, err)
	}
	want := &ExitRequest{Operation: 9}
	exiting := StartMachine(ImmediateMachine(func() (any, *ExitRequest) { return nil, want }))
	event, err = exiting.Run()
	if err != nil || !event.Done || event.Exit != want {
		t.Fatalf("exit completion = %#v, %v", event, err)
	}
}

type exitFrame struct {
	exit    *ExitRequest
	cleared bool
}

func (f *exitFrame) Step(*Machine) MachineStep {
	return MachineStep{Kind: MachineExit, Exit: f.exit}
}
func (f *exitFrame) Clear() { f.cleared = true }

func TestMachineExitRunsNestedCleanupAndSuppressesFailures(t *testing.T) {
	primary := &ExitRequest{Operation: 1}
	inner := &ExitRequest{Operation: 2}
	outer := &ExitRequest{Operation: 3}
	frame := &exitFrame{exit: primary}
	m := StartMachine(frame)
	var order []string
	m.PushCleanup(func() *ExitRequest { order = append(order, "outer"); return outer })
	m.PushCleanup(func() *ExitRequest { order = append(order, "inner"); return inner })
	event, err := m.Run()
	if err != nil || !event.Done || event.Exit == nil {
		t.Fatalf("exit event = %#v, %v", event, err)
	}
	if len(order) != 2 || order[0] != "inner" || order[1] != "outer" {
		t.Fatalf("cleanup order = %v, want [inner outer]", order)
	}
	if len(event.Exit.Suppressed) != 2 || event.Exit.Suppressed[0].Operation != 2 || event.Exit.Suppressed[1].Operation != 3 {
		t.Fatalf("suppressed exits = %#v", event.Exit.Suppressed)
	}
	if !frame.cleared || m.Stats().MaxCleanups != 2 {
		t.Fatalf("clear/stats = %v/%#v", frame.cleared, m.Stats())
	}
}

type catchingFrame struct {
	pc      uint8
	target  *ExitTarget
	child   MachineFrame
	caught  *ExitRequest
	cleanup MachineCleanup
}

func (f *catchingFrame) Step(m *Machine) MachineStep {
	if f.pc == 0 {
		f.pc = 1
		m.PushHandler(f.target)
		if f.cleanup != nil {
			m.PushCleanup(f.cleanup)
		}
		return MachineStep{Kind: MachineCall, Frame: f.child}
	}
	f.caught = m.TakeCaughtExit()
	if f.caught == nil {
		return MachineStep{Kind: MachineReturn, Value: "normal"}
	}
	return MachineStep{Kind: MachineReturn, Value: f.caught.Operation}
}
func (*catchingFrame) Clear() {}

func TestMachineRoutesExitToNearestHandlerBoundary(t *testing.T) {
	target := &ExitTarget{Marker: 1}
	secondary := &ExitRequest{Operation: 8}
	frame := &catchingFrame{target: target}
	frame.child = &exitFrame{exit: &ExitRequest{Target: target, Operation: 7}}
	m := StartMachine(frame)
	var order []string
	m.PushCleanup(func() *ExitRequest { order = append(order, "outer"); return nil })
	frame.cleanup = func() *ExitRequest { order = append(order, "inner"); return secondary }
	event, err := m.Run()
	if err != nil || !event.Done || event.Exit != nil || event.Value != 7 {
		t.Fatalf("event = %#v, %v", event, err)
	}
	if frame.caught == nil || len(frame.caught.Suppressed) != 1 || frame.caught.Suppressed[0] != secondary || len(order) != 2 || order[0] != "inner" || order[1] != "outer" {
		t.Fatalf("caught/cleanup = %#v/%v", frame.caught, order)
	}
}

func TestMachineFrameBufferGrowthKeepsFrameObjectsStable(t *testing.T) {
	leaf := &repeatFrame{value: 21}
	var root MachineFrame = leaf
	frames := []MachineFrame{leaf}
	for range 256 {
		caller := &callFrame{child: root}
		frames = append(frames, caller)
		root = caller
	}
	m := StartMachine(root)
	event, err := m.Run()
	if err != nil || !event.Done || event.Value != 21 {
		t.Fatalf("completion = %#v, %v", event, err)
	}
	if m.Stats().MaxDepth != 257 || m.Stats().MaxFrameCap < 257 {
		t.Fatalf("stats = %#v", m.Stats())
	}
	for i, frame := range frames {
		switch frame := frame.(type) {
		case *repeatFrame:
			if !frame.cleared {
				t.Fatalf("leaf %d was not cleared", i)
			}
		case *callFrame:
			if !frame.cleared {
				t.Fatalf("caller %d was not cleared", i)
			}
		}
	}
}

func BenchmarkMachineFixedDepthSuspension(b *testing.B) {
	b.ReportAllocs()
	b.ReportMetric(8, "suspensions/op")
	var stats MachineStats
	for i := 0; i < b.N; i++ {
		m := StartMachine(&repeatFrame{remaining: 8})
		event, _ := m.Run()
		for !event.Done {
			event, _ = m.Resume(event.Request.(int) + 1)
		}
		stats = m.Stats()
	}
	b.ReportMetric(float64(stats.MaxDepth), "max-frames")
	b.ReportMetric(float64(stats.MaxFrameCap), "frame-cap")
}

func TestCheckedSynchronousMachineAdapter(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		frame := &repeatFrame{value: 42}
		out := RunSynchronousMachine[int](frame)
		if out.Exit != nil || out.Value != 42 || !frame.cleared {
			t.Fatalf("outcome %#v, cleared %v", out, frame.cleared)
		}
	})
	t.Run("exit", func(t *testing.T) {
		failure := &ExitRequest{Effect: "Failure"}
		out := RunSynchronousMachine[int](ImmediateMachine(func() (any, *ExitRequest) { return nil, failure }))
		if out.Exit != failure {
			t.Fatalf("exit %#v, want original failure", out.Exit)
		}
	})
	t.Run("invalid suspension drains", func(t *testing.T) {
		closed := 0
		frame := &invalidSynchronousFrame{closed: &closed}
		defer func() {
			if recover() == nil {
				t.Error("unchecked suspension was accepted")
			}
			if closed != 1 || !frame.cleared {
				t.Errorf("cleanup count %d, cleared %v", closed, frame.cleared)
			}
		}()
		RunSynchronousMachine[int](frame)
	})
}

type invalidSynchronousFrame struct {
	closed  *int
	cleared bool
}

func (f *invalidSynchronousFrame) Step(m *Machine) MachineStep {
	closed := f.closed
	m.PushCleanup(func() *ExitRequest { *closed++; return nil })
	return MachineStep{Kind: MachineSuspend, Request: 42}
}
func (f *invalidSynchronousFrame) Clear() { f.closed = nil; f.cleared = true }
