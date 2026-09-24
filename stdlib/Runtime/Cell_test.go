package native

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestCellSinglePublicationAndMultipleReaders(t *testing.T) {
	cell := CellNew()
	left, right := CellReader(cell), CellReader(cell)
	if CellReady(left) || CellReady(right) {
		t.Fatal("new cell is ready")
	}
	var published atomic.Int32
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			if CellPublish(cell, value) {
				published.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if published.Load() != 1 {
		t.Fatalf("published %d times", published.Load())
	}
	if !CellReady(left) || !CellReady(right) || CellRead(left) != CellRead(right) {
		t.Fatal("readers disagree")
	}
	value := CellRead(left)
	if CellPublish(cell, -1) || CellRead(right) != value {
		t.Fatal("duplicate publication replaced the value")
	}
}
