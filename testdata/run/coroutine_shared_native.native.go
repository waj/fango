package native

import "sync"

type cell struct {
	mu     sync.Mutex
	value  int64
	closed bool
}

func New(value int64) any { return &cell{value: value} }
func Increment(handle any) {
	c := handle.(*cell)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		panic("increment after release")
	}
	c.value++
}
func Read(handle any) int64 {
	c := handle.(*cell)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		panic("read after release")
	}
	return c.value
}
func Release(handle any) {
	c := handle.(*cell)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		panic("duplicate release")
	}
	c.closed = true
	FangoHost.WriteOutput([]byte("released\n"))
}
