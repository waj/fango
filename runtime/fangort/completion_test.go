package fangort

import "testing"

func TestCompletionReplayUsesFreshTypedEvidence(t *testing.T) {
	text, number := NominalType("String", true), NominalType("Int", true)
	original, next := &ExitTarget{}, &ExitTarget{}
	primary := &ExitRequest{Target: original, Effect: "Stop", Operation: 2, OperationName: "Stop.stop", Payload: []any{"bad", int64(7)}, PayloadTypes: []*TypeDescriptor{text, number}, Suppressed: []*ExitRequest{{Target: original, Effect: "Cleanup", OperationName: "Cleanup.close", Suppressed: []*ExitRequest{{Target: original, Effect: "Nested"}}}}}
	completion := CaptureCompletion(Propagate[int](primary))
	primary.Payload[0] = "mutated"
	row := ExtendEvidenceRow(nil, EvidenceBinding{Name: "Stop", Family: EvidenceFamily{AbortReplay: func(f *Failure) *ExitRequest {
		if !CompletionOperation(f, "Stop", "Stop.stop", 2) {
			t.Fatal("operation proof lost")
		}
		return &ExitRequest{Target: next, Effect: "Stop", Operation: 2, OperationName: "Stop.stop", Payload: []any{CompletionPayload[string](f, 0, text), CompletionPayload[int64](f, 1, number)}, PayloadTypes: []*TypeDescriptor{text, number}}
	}}})
	replayed := ReplayCompletion(completion, row).Exit
	if replayed.Target != next || replayed.Payload[0] != "bad" || replayed.Payload[1] != int64(7) {
		t.Fatalf("wrong replay %#v", replayed)
	}
	if len(replayed.Suppressed) != 1 || replayed.Suppressed[0].Target != nil || len(replayed.Suppressed[0].Suppressed) != 1 || replayed.Suppressed[0].Suppressed[0].Target != nil {
		t.Fatal("suppressed report retained target or lost nested data")
	}
	replayed.Suppressed[0].Effect = "changed"
	if ReplayCompletion(completion, row).Exit.Suppressed[0].Effect != "Cleanup" {
		t.Fatal("replay mutated detached report")
	}
}

func TestCompletionProjectionRejectsStaleProof(t *testing.T) {
	f := SnapshotFailure(&ExitRequest{Payload: []any{int64(1)}, PayloadTypes: []*TypeDescriptor{NominalType("Int", true)}})
	defer func() {
		if recover() == nil {
			t.Fatal("stale descriptor was accepted")
		}
	}()
	CompletionPayload[int64](f, 0, NominalType("Other", true))
}

func TestCompletionReplayRejectsUnknownOperation(t *testing.T) {
	failure := SnapshotFailure(&ExitRequest{Effect: "Stop", OperationName: "Stop.removed"})
	row := ExtendEvidenceRow(nil, EvidenceBinding{Name: "Stop", Family: EvidenceFamily{AbortReplay: func(f *Failure) *ExitRequest {
		if CompletionOperation(f, "Stop", "Stop.stop", 0) {
			return &ExitRequest{Effect: "Stop", OperationName: "Stop.stop"}
		}
		return nil
	}}})
	defer func() {
		if got := recover(); got != "fango: stale completion operation proof" {
			t.Fatalf("unknown operation did not reject its projection: %v", got)
		}
	}()
	ReplayFailure(failure, row)
}

func TestCompletionBoundaryKeepsLocalHandlersAndCleanup(t *testing.T) {
	exit := &ExitRequest{Target: &ExitTarget{}, Effect: "Fail", OperationName: "Fail.fail"}
	cleanup := &ExitRequest{Target: &ExitTarget{}, Effect: "Close", OperationName: "Close.close"}
	child := &completionCleanupFrame{exit: exit, cleanup: cleanup}
	machine := StartMachine(CompletionMachine[int](child))
	event, err := machine.Run()
	if err != nil || event.Done {
		t.Fatalf("expected suspension, got %#v %v", event, err)
	}
	event, err = machine.Resume(struct{}{})
	if err != nil || !event.Done || event.Exit != nil {
		t.Fatalf("capture propagated exit %#v %v", event, err)
	}
	completion, ok := event.Value.(Completion[int])
	if !ok || completion.Failure().Effect() != "Fail" || completion.Failure().Suppressed().Head().Effect() != "Close" {
		t.Fatal("missing detached cleanup failure")
	}
	if child.exit != nil || child.cleanup != nil {
		t.Fatal("completed child frame retained references")
	}
}

type completionCleanupFrame struct {
	pc            int
	exit, cleanup *ExitRequest
}

func (f *completionCleanupFrame) Step(m *Machine) MachineStep {
	if f.pc == 0 {
		f.pc++
		cleanup := f.cleanup
		m.PushCleanup(func() *ExitRequest { return cleanup })
		return MachineStep{Kind: MachineSuspend, Request: "pause"}
	}
	m.TakeResult()
	return MachineStep{Kind: MachineExit, Exit: f.exit}
}
func (f *completionCleanupFrame) Clear() { f.exit = nil; f.cleanup = nil }

func TestOwnerStopDoesNotPublishCompletion(t *testing.T) {
	cleanup := &ExitRequest{Target: &ExitTarget{}, Effect: "Close", OperationName: "Close.close"}
	child := &completionCleanupFrame{cleanup: cleanup}
	machine := StartMachine(CompletionMachine[int](child))
	event, err := machine.Run()
	if err != nil || event.Done {
		t.Fatalf("suspend: %#v %v", event, err)
	}
	exit, err := machine.Abandon()
	if err != nil || exit != cleanup || machine.cause != terminalOwnerStop {
		t.Fatalf("stop: cause=%v exit=%#v err=%v", machine.cause, exit, err)
	}
	if machine.TakeResult() != nil || machine.TakeCaughtExit() != nil || len(machine.frames)+len(machine.handlers)+len(machine.cleanups) != 0 {
		t.Fatal("owner stop published completion or retained frames")
	}
}
