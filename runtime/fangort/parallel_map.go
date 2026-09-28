package fangort

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// ParallelMap preserves order and bounds running callbacks by GOMAXPROCS.
// The compiler accepts only pure callbacks; no task handles escape this call.
func ParallelMap[A, B any](fn func(A) B, source List[A]) List[B] {
	var input []A
	for !source.IsEmpty() {
		input = append(input, source.Head())
		source = source.Tail()
	}
	if len(input) == 0 {
		return ListNil[B]()
	}
	workers := min(runtime.GOMAXPROCS(0), len(input))
	output := make([]B, len(input))
	var next atomic.Int64
	var done sync.WaitGroup
	work := func() {
		defer done.Done()
		for {
			i := int(next.Add(1)) - 1
			if i >= len(input) {
				return
			}
			output[i] = fn(input[i])
		}
	}
	done.Add(workers)
	for range workers - 1 {
		go work()
	}
	work()
	done.Wait()
	result := ListNil[B]()
	for i := len(output) - 1; i >= 0; i-- {
		result = ListCons(output[i], result)
	}
	return result
}
