package fangort

import "sync"

// HandlerState publishes complete state values between calls. Snapshot and
// Store are individually synchronized; evaluating a clause between them is
// not atomic and may overwrite another operation's update. Operation-level
// serialization belongs to the handler, not this cell.
type HandlerState[T any] struct {
	mu    sync.Mutex
	value T
}

func NewHandlerState[T any](value T) *HandlerState[T] {
	return &HandlerState[T]{value: value}
}

func (s *HandlerState[T]) Snapshot() T {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

func (s *HandlerState[T]) Store(value T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
}
