package fangort

import "testing"

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
