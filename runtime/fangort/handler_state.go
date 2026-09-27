package fangort

import "sync"

// HandlerState serializes an operation's snapshot, evaluation, and commit.
// A task-local fork takes a shallow snapshot; values stored in the state are
// immutable Fango values but may themselves contain shared native references.
type HandlerState[T any] struct {
	mu    sync.Mutex
	Value T // accessed only while holding the operation lock
}

func NewHandlerState[T any](value T) *HandlerState[T] {
	return &HandlerState[T]{Value: value}
}

func (s *HandlerState[T]) Lock()   { s.mu.Lock() }
func (s *HandlerState[T]) Unlock() { s.mu.Unlock() }

func (s *HandlerState[T]) Snapshot() T {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Value
}
