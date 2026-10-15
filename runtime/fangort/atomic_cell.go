package fangort

import "sync/atomic"

// AtomicVersion is one published value of an AtomicCell. Every write
// allocates a fresh version, so CompareAndSwap compares identities rather
// than values: a value type needs no equality, and a cell that returns to an
// earlier value still rejects a stale version. A version remembers its cell,
// so a commit can name the version alone.
type AtomicVersion struct {
	Value any
	cell  *AtomicCell
}

// Replace publishes value only if v is still its cell's current version.
func (v *AtomicVersion) Replace(value any) bool { return v.cell.CompareAndSwap(v, value) }

// AtomicCell holds a value that concurrent goroutines replace without a lock.
// A read-modify-write loads a version, computes from its Value, and commits
// with CompareAndSwap, retrying from a fresh Load when another write won.
type AtomicCell struct{ current atomic.Pointer[AtomicVersion] }

func NewAtomicCell(value any) *AtomicCell {
	c := &AtomicCell{}
	c.Store(value)
	return c
}

func (c *AtomicCell) Load() *AtomicVersion { return c.current.Load() }

// CompareAndSwap publishes value only if old is still the current version.
func (c *AtomicCell) CompareAndSwap(old *AtomicVersion, value any) bool {
	return c.current.CompareAndSwap(old, &AtomicVersion{Value: value, cell: c})
}

func (c *AtomicCell) Store(value any) { c.current.Store(&AtomicVersion{Value: value, cell: c}) }

// Swap publishes value and answers the value it replaced.
func (c *AtomicCell) Swap(value any) any {
	return c.current.Swap(&AtomicVersion{Value: value, cell: c}).Value
}
