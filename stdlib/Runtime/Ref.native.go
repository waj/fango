package native

import "sync"

// Payloads are opaque typed runtime tokens, never inspected by the sidecar.
// Individual accesses are synchronized; a read followed by a write is not
// an atomic read-modify-write operation.
type reference struct {
	mu    sync.RWMutex
	value any
}

func New(value any) any { return &reference{value: value} }
func Read(handle any) any {
	r := handle.(*reference)
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.value
}
func Write(handle, value any) {
	r := handle.(*reference)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.value = value
}
