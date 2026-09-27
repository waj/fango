package native

import (
	"sync"
	"testing"
)

func TestSharedReferencePublishesWholeValues(t *testing.T) {
	type pair struct{ left, right int }
	r := New(pair{})
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Go(func() {
			for range 1000 {
				Write(r, pair{worker, worker})
				p := Read(r).(pair)
				if p.left != p.right {
					t.Errorf("torn value: %+v", p)
				}
			}
		})
	}
	workers.Wait()
}
