package fangort

import "testing"

// What the list representation costs, and which of those costs a chunk-aware
// implementation can actually remove. These arbitrate the open decisions in
// doc/roadmap-calls.md and doc/roadmap-list.md, so they live here rather than
// being re-derived each time the question comes up.
//
// The headline: chunk-awareness is worth little on its own once a per-element
// callback is in the loop, and worth a great deal when it replaces recursion.
// mapChunk is the prototype of the planned native, kept honest by
// TestMapChunkMatchesMapRec.

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

func BenchmarkSumChunkWalk(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var acc int64
		for n, off := l.node, l.off; n != nil; n, off = n.next.node, n.next.off {
			for _, x := range n.elems[off:] {
				acc += x
			}
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

func BenchmarkFoldChunkWalkPlain(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var acc int64
		for n, off := l.node, l.off; n != nil; n, off = n.next.node, n.next.off {
			for _, x := range n.elems[off:] {
				acc += plainCB(x)
			}
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

func BenchmarkFoldChunkWalkCurried(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var acc int64
		for n, off := l.node, l.off; n != nil; n, off = n.next.node, n.next.off {
			for _, x := range n.elems[off:] {
				acc = curriedCB(x)(acc)
			}
		}
		sinkI = acc
	}
}

// --- map: recursion + accessors, versus one forward chunk-wise pass --------

// What fango emits today: non-tail recursion through the accessors.
func mapRec(f func(int64) int64, l List[int64]) List[int64] {
	if l.IsEmpty() {
		return ListNil[int64]()
	}
	return ListCons(f(l.Head()), mapRec(f, l.Tail()))
}

// A chunk-aware native: mirror each source chunk into a fresh one, filling it
// head-to-tail so the callback still runs in element order, and link forward.
// No recursion, no intermediate buffer, same chunk count as the source.
func mapChunk(f func(int64) int64, l List[int64]) List[int64] {
	if l.node == nil {
		return List[int64]{}
	}
	fill := func(src *chunk[int64], off int) *chunk[int64] {
		dst := &chunk[int64]{lo: off}
		for i := off; i < listChunk; i++ {
			dst.elems[i] = f(src.elems[i])
		}
		return dst
	}
	head := fill(l.node, l.off)
	cur := head
	for src := l.node.next; src.node != nil; src = src.node.next {
		dst := fill(src.node, src.off)
		cur.next = List[int64]{node: dst, off: src.off}
		cur = dst
	}
	return List[int64]{node: head, off: l.off}
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

func BenchmarkMapChunkForward(b *testing.B) {
	l := mkList()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sinkL = mapChunk(plainCB, l)
	}
}

func TestMapChunkMatchesMapRec(t *testing.T) {
	for _, n := range []int{0, 1, listChunk - 1, listChunk, listChunk + 1, 500} {
		l := ListNil[int64]()
		for i := int64(1); i <= int64(n); i++ {
			l = ListCons(i, l)
		}
		for _, src := range []List[int64]{l, l.Tail()} {
			if src.IsEmpty() && n > 1 {
				t.Fatal("tail of a non-trivial list is empty")
			}
			want, got := mapRec(plainCB, src), mapChunk(plainCB, src)
			if !ListEq(func(a, b int64) bool { return a == b }, want, got) {
				t.Fatalf("n=%d: chunk-wise map disagrees with the recursive one", n)
			}
		}
	}
}
