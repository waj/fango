package fangort

// A handler clause for a polymorphic operation runs with its operation-local
// type variables erased to Go any, so a value it builds, such as Box [x] with
// x : a, has the Go type of Box (List any) while its descriptor names the
// caller's type. A descriptor match proves the source type; Rebuild only
// changes the Go representation to the one expected where the value is read.

// Rebuilder converts a value of its descriptor's nominal type, at any Go
// instantiation, into the instantiation it was constructed for. arguments
// are the descriptor's own type arguments.
type Rebuilder interface {
	Rebuild(source any, arguments []*TypeDescriptor) any
}

// Constructed exposes a generated constructor's index and fields
// independently of the instantiation of its type parameters.
type Constructed interface {
	FangoFields() (int, []any)
}

// Rebuild returns source at the Go representation of A. The caller has
// checked that source's descriptor equals descriptor. A rebuilder decides
// first: a marker interface admits every instantiation of its constructors,
// so asserting to A alone cannot tell them apart.
func Rebuild[A any](descriptor *TypeDescriptor, source any) A {
	if descriptor != nil && descriptor.rebuild != nil {
		source = descriptor.rebuild.Rebuild(source, descriptor.arguments)
	}
	if value, ok := source.(A); ok {
		return value
	}
	return RebuildMismatch().(A)
}

// RebuildMismatch reports a payload whose representation contradicts its
// descriptor match: the compiler packaged an invalid proof.
func RebuildMismatch() any {
	panic("fango: payload disagrees with its checked type descriptor")
}

// ListRebuilder rebuilds a List element by element.
type ListRebuilder[T any] struct{}

func (ListRebuilder[T]) Rebuild(source any, arguments []*TypeDescriptor) any {
	if value, ok := source.(List[T]); ok {
		return value
	}
	items, ok := source.(interface{ erasedItems() []any })
	if !ok {
		return RebuildMismatch()
	}
	var result List[T]
	next := &result
	for _, item := range items.erasedItems() {
		node := &listNode[T]{head: Rebuild[T](arguments[0], item)}
		*next = List[T]{node: node}
		next = &node.tail
	}
	return result
}

func (l List[T]) erasedItems() []any {
	var items []any
	for current := l; !current.IsEmpty(); current = current.Tail() {
		items = append(items, current.Head())
	}
	return items
}
