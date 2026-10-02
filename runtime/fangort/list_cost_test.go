package fangort

import "testing"

// Compare accessor and direct-node traversal, and recursive, forward-building,
// and accumulate/reverse mapping. Historical chunk measurements remain in Git.

const benchN = 100000

var sinkI int64

func mkList() List[int64] {
	l := ListNil[int64]()
	for i := int64(1); i <= benchN; i++ {
		l = ListCons(i, l)
	}
	return l
}

// Opaque callbacks: inside a generated worker the callback is a parameter of
// unknown identity, so Go can neither inline it nor stack-allocate what it
// returns. Package vars reproduce that.
var plainCB = func(x int64) int64 { return x }
var curriedCB = func(x int64) func(int64) int64 {
	return func(acc int64) int64 { return x + acc }
}

// --- traversal only, no callback -------------------------------------------

func BenchmarkSumSlice(b *testing.B) {
	xs := make([]int64, 0, benchN)
	for i := int64(benchN); i >= 1; i-- {
		xs = append(xs, i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var acc int64
		for _, x := range xs {
			acc += x
		}
		sinkI = acc
	}
}

func BenchmarkSumAccessors(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var acc int64
		for c := l; !c.IsEmpty(); c = c.Tail() {
			acc += c.Head()
		}
		sinkI = acc
	}
}

func BenchmarkSumNodeWalk(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var acc int64
		for n := l.node; n != nil; n = n.tail.node {
			acc += n.head
		}
		sinkI = acc
	}
}

// --- with an opaque callback -----------------------------------------------

func BenchmarkFoldAccessorsPlain(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var acc int64
		for c := l; !c.IsEmpty(); c = c.Tail() {
			acc += plainCB(c.Head())
		}
		sinkI = acc
	}
}

func BenchmarkFoldNodeWalkPlain(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var acc int64
		for n := l.node; n != nil; n = n.tail.node {
			acc += plainCB(n.head)
		}
		sinkI = acc
	}
}

func BenchmarkFoldAccessorsCurried(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var acc int64
		for c := l; !c.IsEmpty(); c = c.Tail() {
			acc = curriedCB(c.Head())(acc)
		}
		sinkI = acc
	}
}

func BenchmarkFoldNodeWalkCurried(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var acc int64
		for n := l.node; n != nil; n = n.tail.node {
			acc = curriedCB(n.head)(acc)
		}
		sinkI = acc
	}
}

// --- map: recursion + accessors, versus one forward forward pass --------

// What Fango emits today: non-tail recursion through the accessors.
func mapRec(f func(int64) int64, l List[int64]) List[int64] {
	if l.IsEmpty() {
		return ListNil[int64]()
	}
	return ListCons(f(l.Head()), mapRec(f, l.Tail()))
}

var sinkL List[int64]

func BenchmarkMapRecursive(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sinkL = mapRec(plainCB, l)
	}
}

func BenchmarkMapForward(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sinkL = ListMap(plainCB, l)
	}
}

func TestMapForwardMatchesMapRec(t *testing.T) {
	for _, n := range []int{0, 1, 32 - 1, 32, 32 + 1, 500} {
		l := ListNil[int64]()
		for i := int64(1); i <= int64(n); i++ {
			l = ListCons(i, l)
		}
		for _, src := range []List[int64]{l, l.Tail()} {
			if src.IsEmpty() && n > 1 {
				t.Fatal("tail of a non-trivial list is empty")
			}
			want, got := mapRec(plainCB, src), ListMap(plainCB, src)
			if !listsEqual(func(a, b int64) bool { return a == b }, want, got) {
				t.Fatalf("n=%d: forward map disagrees with the recursive one", n)
			}
		}
	}
}

// What stdlib List.map compiles to today: a tail-recursive accumulator loop,
// then a reverse loop. Two passes, two lists.
func mapAccumReverse(f func(int64) int64, l List[int64]) List[int64] {
	acc := ListNil[int64]()
	for c := l; !c.IsEmpty(); c = c.Tail() {
		acc = ListCons(f(c.Head()), acc)
	}
	out := ListNil[int64]()
	for c := acc; !c.IsEmpty(); c = c.Tail() {
		out = ListCons(c.Head(), out)
	}
	return out
}

func BenchmarkMapAccumReverse(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sinkL = mapAccumReverse(plainCB, l)
	}
}

func TestMapAccumReverseMatchesListMap(t *testing.T) {
	for _, n := range []int{0, 1, 32 - 1, 32, 32 + 1, 500} {
		l := ListNil[int64]()
		for i := int64(1); i <= int64(n); i++ {
			l = ListCons(i, l)
		}
		eq := func(a, b int64) bool { return a == b }
		if !listsEqual(eq, mapAccumReverse(plainCB, l), ListMap(plainCB, l)) {
			t.Fatalf("n=%d: one-pass map disagrees with accumulate-and-reverse", n)
		}
	}
}
