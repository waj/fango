package fangort

import (
	"runtime"
	"sync/atomic"
	"testing"
)

func TestParallelMapOrderAndBound(t *testing.T) {
	input := ListNil[int]()
	for i := 99; i >= 0; i-- {
		input = ListCons(i, input)
	}
	var running, peak atomic.Int64
	result := ParallelMap(func(i int) int {
		active := running.Add(1)
		for previous := peak.Load(); active > previous && !peak.CompareAndSwap(previous, active); previous = peak.Load() {
		}
		runtime.Gosched()
		running.Add(-1)
		return i * 2
	}, input)
	for i := 0; i < 100; i++ {
		if result.IsEmpty() || result.Head() != i*2 {
			t.Fatalf("bad result at %d", i)
		}
		result = result.Tail()
	}
	if !result.IsEmpty() || peak.Load() > int64(runtime.GOMAXPROCS(0)) {
		t.Fatal("order or worker bound violated")
	}
	if !ParallelMap(func(i int) int { t.Fatal("empty callback"); return i }, ListNil[int]()).IsEmpty() {
		t.Fatal("empty mapping")
	}
}
