package fangort

import "testing"

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
