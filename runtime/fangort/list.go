package fangort

import "strings"

// The runtime representation of the bundled fango `List` type (see
// doc/roadmap-list.md). `List` is an ordinary ADT to the checker, the deriver,
// and reflection; only the backends know it is stored this way, exactly as
// `Bool` is an ADT that compiles to a native Go bool.
//
// Elements live in fixed-size inline arrays filled downward. A chunk's `lo`
// watermark records how far it has been filled; slots [lo, listChunk) are
// written and frozen, and `lo` only ever decreases. Cons claims the slot below
// the watermark when the list it extends owns the frontier, and otherwise
// starts a fresh chunk pointing at that list, so no cons ever copies.
//
// Persistence: every value has off >= node.lo at creation (a cons sets
// off = lo; a tail sets off+1 > off >= lo; a `next` was a value created
// earlier), and lo only decreases, so off >= node.lo holds forever. A cons
// writing slot i = lo-1 is therefore invisible to every value that already
// existed on that node, because each had off >= i+1. The frontier test is
// exact rather than conservative: when off > node.lo, slot off-1 is already
// published and the copy-free path is correctly refused.
//
// The watermark is a benign non-atomic mutation only because evaluation is
// single-threaded. Under concurrency the frontier claim would have to become a
// compare-and-swap on lo.

// listChunk is the number of elements one chunk holds. Bytes per element
// saturates here, so a larger chunk only doubles the waste a short list and a
// branching cons each pay in full; doc/roadmap-list.md records the sizing.
const listChunk = 32

type chunk[T any] struct {
	lo    int
	elems [listChunk]T
	next  List[T]
}

// List is a fango list value: two words, and the zero value is the empty list.
// The unnamed zero-sized field makes List uncomparable, so an accidental Go
// `==` is a compile error rather than a silently wrong identity comparison —
// fango equality is structural and goes through ListEq.
type List[T any] struct {
	_    [0]func()
	node *chunk[T]
	off  int
}

// ListNil is the empty list. It allocates nothing.
func ListNil[T any]() List[T] { return List[T]{} }

// ListCons prepends head to tail. O(1) worst case; it allocates one chunk per
// listChunk elements when building linearly, one chunk when branching off a
// list that no longer owns its chunk's frontier, and never copies.
func ListCons[T any](head T, tail List[T]) List[T] {
	if n := tail.node; n != nil && tail.off == n.lo && n.lo > 0 {
		n.lo--
		n.elems[n.lo] = head
		return List[T]{node: n, off: n.lo}
	}
	n := &chunk[T]{lo: listChunk - 1, next: tail}
	n.elems[listChunk-1] = head
	return List[T]{node: n, off: listChunk - 1}
}

// IsEmpty reports whether l is Nil.
func (l List[T]) IsEmpty() bool { return l.node == nil }

// Head is the first element. Exhaustiveness checking guarantees callers have
// already discriminated on IsEmpty.
func (l List[T]) Head() T { return l.node.elems[l.off] }

// Tail is everything after Head. O(1), and allocates nothing: the result is a
// value, so generated Go keeps it in locals.
func (l List[T]) Tail() List[T] {
	if l.off+1 < listChunk {
		return List[T]{node: l.node, off: l.off + 1}
	}
	return l.node.next
}

// ListEq is structural equality, the runtime half of the derived eq for List.
// It deliberately has no representation-identity short circuit: two lists that
// share a chunk and offset are still compared element by element, because a
// list holding a NaN is not equal to itself.
func ListEq[T any](eq func(T, T) bool, a, b List[T]) bool {
	for !a.IsEmpty() && !b.IsEmpty() {
		if !eq(a.Head(), b.Head()) {
			return false
		}
		a, b = a.Tail(), b.Tail()
	}
	return a.IsEmpty() && b.IsEmpty()
}

// ListShow renders the structural derived form — `Cons 1 (Cons 2 Nil)`, nested
// field-taking constructors parenthesized — which the interpreter's showCtorVal
// mirrors byte for byte. The bracket display users see comes from the
// handwritten `Show` instance in stdlib/List.fango, not from here.
//
// It is iterative because the nesting is as deep as the list is long.
func ListShow[T any](show func(T, bool) string, v List[T], nested bool) string {
	if v.IsEmpty() {
		return "Nil"
	}
	var b strings.Builder
	opened := 0
	for first := true; !v.IsEmpty(); first = false {
		if nested || !first {
			b.WriteByte('(')
			opened++
		}
		b.WriteString("Cons ")
		b.WriteString(show(v.Head(), true))
		b.WriteByte(' ')
		v = v.Tail()
	}
	b.WriteString("Nil")
	b.WriteString(strings.Repeat(")", opened))
	return b.String()
}

// ListMap applies fn to every element in order, in one forward pass. The
// source's shape is known, so each chunk is mirrored into a fresh one — filled
// head to tail, so an effectful callback still runs in element order — and
// linked as it goes. No recursion, no intermediate list, and the same number
// of chunks as the source.
//
// Writing this in fango costs either a Go frame per element (recursing under
// the constructor) or a second pass and a second list (accumulating and
// reversing). This is the one-pass form neither can express.
func ListMap[A, B any](fn func(A) B, l List[A]) List[B] {
	if l.node == nil {
		return List[B]{}
	}
	mirror := func(src *chunk[A], off int) *chunk[B] {
		dst := &chunk[B]{lo: off}
		for i := off; i < listChunk; i++ {
			dst.elems[i] = fn(src.elems[i])
		}
		return dst
	}
	head := mirror(l.node, l.off)
	cur := head
	for src := l.node.next; src.node != nil; src = src.node.next {
		dst := mirror(src.node, src.off)
		cur.next = List[B]{node: dst, off: src.off}
		cur = dst
	}
	return List[B]{node: head, off: l.off}
}
