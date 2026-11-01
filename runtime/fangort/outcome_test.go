package fangort

import "testing"

func TestNewExitSharesOneAllocation(t *testing.T) {
	target, descriptor := &ExitTarget{}, &TypeDescriptor{}
	var exit *ExitRequest
	if allocs := testing.AllocsPerRun(100, func() {
		exit = NewExit(target, "Fail.Fail", 2, "Fail.fail", "payload", descriptor)
	}); allocs != 1 {
		t.Errorf("allocations = %v, want 1", allocs)
	}
	if exit.Target != target || exit.Effect != "Fail.Fail" || exit.Operation != 2 || exit.OperationName != "Fail.fail" {
		t.Errorf("request = %+v", exit)
	}
	if len(exit.Payload) != 1 || exit.Payload[0] != "payload" || len(exit.PayloadTypes) != 1 || exit.PayloadTypes[0] != descriptor {
		t.Errorf("payload = %v %v", exit.Payload, exit.PayloadTypes)
	}
	joined := Suppress(exit, &ExitRequest{Target: target})
	if joined.Payload[0] != "payload" || len(exit.Suppressed) != 0 {
		t.Errorf("suppression edited the primary or lost its payload: %+v", joined)
	}
}

func TestCaptureExitOnlyForResolvedDiagnosticTarget(t *testing.T) {
	ordinary := &ExitRequest{Target: &ExitTarget{}}
	if got := CaptureExit(ordinary); got != ordinary || got.TraceCaptured || len(got.Trace) != 0 {
		t.Fatal("ordinary abort captured diagnostics")
	}
	if allocations := testing.AllocsPerRun(100, func() { CaptureExit(ordinary) }); allocations != 0 {
		t.Fatalf("ordinary abort capture check allocates: %v", allocations)
	}
	target := &ExitTarget{CaptureTrace: true}
	proxy := DeferredExitTarget(func() *ExitTarget { return target })
	diagnostic := &ExitRequest{Target: ResolveExitTarget(proxy)}
	if got := CaptureExit(diagnostic); got != diagnostic || !got.TraceCaptured {
		t.Fatal("diagnostic policy lost through deferred target")
	}
	// A Go test has no Fango frames, but a requested empty capture is present.
	if frames, present := SnapshotFailure(diagnostic).Trace(); !present || len(frames) != 0 {
		t.Fatalf("empty requested trace: %v %v", frames, present)
	}
	diagnostic.Trace = []Frame{{Function: "Main.original", Line: 12}}
	forwarded := CaptureExit(Suppress(diagnostic, ordinary))
	if !forwarded.TraceCaptured || len(forwarded.Trace) != 1 || forwarded.Trace[0].Function != "Main.original" {
		t.Fatal("propagation or suppression recaptured the failure")
	}
}

func TestDeferredExitTargetResolvesAtFailureTime(t *testing.T) {
	first, second := &ExitTarget{}, &ExitTarget{}
	current := first
	proxy := DeferredExitTarget(func() *ExitTarget { return current })
	forward := DeferredExitTarget(func() *ExitTarget { return proxy })
	request := &ExitRequest{Target: ResolveExitTarget(forward)}
	current = second
	if request.Target != first || ResolveExitTarget(forward) != second {
		t.Fatal("an existing exit followed a later row binding")
	}
}

func TestRequireNormalNeverDiscardsAnExit(t *testing.T) {
	if got := RequireNormal(Normal(42)); got != 42 {
		t.Fatalf("got %d", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("discarded a non-normal outcome")
		}
	}()
	RequireNormal(Propagate[int](&ExitRequest{Target: &ExitTarget{}}))
}
