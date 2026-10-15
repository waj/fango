package fangort

import (
	"sync"
	"testing"
)

func TestAtomicCellRejectsStaleVersion(t *testing.T) {
	cell := NewAtomicCell(1)
	stale := cell.Load()
	cell.Store(1)
	if stale.Replace(2) {
		t.Fatal("a version replaced by an equal value was accepted")
	}
	current := cell.Load()
	if !cell.CompareAndSwap(current, 3) || cell.Load().Value != 3 {
		t.Fatalf("current version rejected: %v", cell.Load().Value)
	}
	if old := cell.Swap(4); old != 3 || cell.Load().Value != 4 {
		t.Fatalf("swap answered %v, left %v", old, cell.Load().Value)
	}
}

func TestAtomicCellUpdatesAreNotLost(t *testing.T) {
	cell := NewAtomicCell(0)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 1000 {
				for {
					current := cell.Load()
					if current.Replace(current.Value.(int) + 1) {
						break
					}
				}
			}
		})
	}
	workers.Wait()
	if got := cell.Load().Value; got != 8000 {
		t.Fatalf("got %v, want 8000", got)
	}
}
