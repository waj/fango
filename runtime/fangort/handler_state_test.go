package fangort

import (
	"runtime"
	"sync"
	"testing"
)

func TestHandlerStateConcurrentOperation(t *testing.T) {
	state := NewHandlerState(0)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 1000 {
				state.Lock()
				snapshot := state.Value
				runtime.Gosched()
				state.Value = snapshot + 1
				state.Unlock()
			}
		})
	}
	workers.Wait()
	if got := state.Snapshot(); got != 8000 {
		t.Fatalf("lost operation update: %d", got)
	}
}
