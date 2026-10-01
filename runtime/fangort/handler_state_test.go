package fangort

import (
	"sync"
	"testing"
)

func TestHandlerStatePublishesWholeValues(t *testing.T) {
	type pair struct{ left, right int }
	state := NewHandlerState(pair{})
	state.Share()
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			for j := range 1000 {
				n := i*1000 + j
				state.Store(pair{n, -n})
				got := state.Snapshot()
				if got.left != -got.right {
					t.Errorf("torn state: %+v", got)
					return
				}
			}
		})
	}
	workers.Wait()
}

func TestHandlerStateDoesNotSerializeOperations(t *testing.T) {
	state := NewHandlerState(0)
	state.Share()
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			snapshot := state.Snapshot()
			ready <- struct{}{}
			<-release
			state.Store(snapshot + 1)
		})
	}
	<-ready
	<-ready
	close(release)
	workers.Wait()
	if got := state.Snapshot(); got != 1 {
		t.Fatalf("both operations read zero; want last-writer-wins value 1, got %d", got)
	}
}

func TestHandlerStateExplicitSerialization(t *testing.T) {
	state := NewHandlerState(0)
	state.Share()
	var operation sync.Mutex
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 1000 {
				operation.Lock()
				state.Store(state.Snapshot() + 1)
				operation.Unlock()
			}
		})
	}
	workers.Wait()
	if got := state.Snapshot(); got != 8000 {
		t.Fatalf("lost explicitly serialized update: %d", got)
	}
}

// An unpublished cell is plain storage; publication is idempotent and switches
// the cell to synchronized access without losing its value.
func TestHandlerStateShareIsIdempotentAndKeepsValue(t *testing.T) {
	cell := NewHandlerState(1)
	cell.Store(2)
	if cell.shared || cell.Snapshot() != 2 {
		t.Fatal("unshared cell misbehaved")
	}
	cell.Share()
	cell.Share()
	if !cell.shared || cell.Snapshot() != 2 {
		t.Fatal("publication lost the value")
	}
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			for range 1000 {
				cell.Store(cell.Snapshot() + i)
			}
		})
	}
	workers.Wait()
	if cell.Snapshot() < 2 {
		t.Fatal("published cell lost its value")
	}
}
