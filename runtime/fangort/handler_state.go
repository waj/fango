package fangort

import "sync"

// HandlerState publishes complete state values between calls. While exactly
// one goroutine can reach the cell, Snapshot and Store are plain accesses.
// Share marks the cell before its activation is handed to another task; from
// then on each Snapshot and Store is individually synchronized. Evaluating a
// clause between them is not atomic and may overwrite another operation's
// update. Operation-level serialization belongs to the handler, not this cell.
type HandlerState[T any] struct {
	// shared is monotonic. It is written only by the sole owner, before the
	// goroutine that will also reach the cell starts, so no access races it.
	shared bool
	mu     sync.Mutex
	value  T
}

func NewHandlerState[T any](value T) *HandlerState[T] {
	return &HandlerState[T]{value: value}
}

// Share publishes the cell. The test avoids a write when the cell is already
// shared, which is the only moment another goroutine could be reading it.
func (s *HandlerState[T]) Share() {
	if !s.shared {
		s.shared = true
	}
}

// Snapshot and Store stay within Go's inlining budget so that an unshared cell
// costs a field access inside the clause. The locked paths are kept apart: with
// them inline, each access is a call.
func (s *HandlerState[T]) Snapshot() T {
	if !s.shared {
		return s.value
	}
	return s.sharedSnapshot()
}

func (s *HandlerState[T]) Store(value T) {
	if !s.shared {
		s.value = value
		return
	}
	s.sharedStore(value)
}

//go:noinline
func (s *HandlerState[T]) sharedSnapshot() T {
	s.mu.Lock()
	value := s.value
	s.mu.Unlock()
	return value
}

//go:noinline
func (s *HandlerState[T]) sharedStore(value T) {
	s.mu.Lock()
	s.value = value
	s.mu.Unlock()
}
