package fangort

import "testing"

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
