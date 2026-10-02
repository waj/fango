package fangort

// List is an immutable cons list. Construction never writes to a published
// node, so independent branches can share a tail without synchronization.
// The zero value is empty. The uncomparable field prevents accidental Go
// identity equality; Fango equality is structural.
type List[T any] struct {
	_    [0]func()
	node *listNode[T]
}

type listNode[T any] struct {
	head T
	tail List[T]
}

func ListNil[T any]() List[T] { return List[T]{} }

// ListCons allocates one immutable node and shares the existing tail.
func ListCons[T any](head T, tail List[T]) List[T] {
	return List[T]{node: &listNode[T]{head: head, tail: tail}}
}

func (l List[T]) IsEmpty() bool { return l.node == nil }
func (l List[T]) Head() T       { return l.node.head }
func (l List[T]) Tail() List[T] {
	if l.node == nil {
		return ListNil[T]()
	}
	return l.node.tail
}

// ListMap visits elements in source order and builds a private result spine.
// No result node is published until every callback has completed. Callback
// failures therefore cannot expose partially initialized storage.
func ListMap[A, B any](fn func(A) B, source List[A]) List[B] {
	var result List[B]
	next := &result
	for current := source; !current.IsEmpty(); current = current.Tail() {
		node := &listNode[B]{head: fn(current.Head())}
		*next = List[B]{node: node}
		next = &node.tail
	}
	return result
}
