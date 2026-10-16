package fangort

import (
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
)

// Software transactional memory for the STM library, after TL2 with a global
// version clock. An attempt records the clock when it begins and treats any
// variable published at a later version as a conflict, so every attempt reads
// one consistent snapshot. Writes are buffered in the attempt's log; a writing
// commit locks the variables it writes, in a fixed order, then validates its
// reads and publishes its writes, so commits with disjoint write sets proceed
// in parallel. Reads and read-only commits take no lock.
//
// Values are opaque payload tokens: the runtime stores and returns them
// without inspecting them.

// stmClock is the version of the latest writing commit.
var stmClock atomic.Uint64

// stmIDs numbers variables in creation order, the order commits lock them in.
var stmIDs atomic.Uint64

// tvarNode is one immutable published value of a TVar.
type tvarNode struct {
	value   any
	version uint64
}

// TVar is a transactional variable. Its identity distinguishes it.
type TVar struct {
	current atomic.Pointer[tvarNode]
	id      uint64
	// locked is held by the one commit publishing this variable.
	locked  atomic.Bool
	mu      sync.Mutex // guards waiters
	waiters map[*stmWaiter]struct{}
}

// stmWaiter is one blocked attempt, registered on every variable it read.
type stmWaiter struct{ ready chan struct{} }

type stmWrite struct {
	tvar  *TVar
	value any
}

// STMLog is one attempt's private record. Only the task running the attempt
// touches it, so it needs no synchronization.
type STMLog struct {
	readVersion uint64
	reads       map[*TVar]struct{}
	writes      []stmWrite // append-only; a savepoint is a length
	latest      map[*TVar]int
}

// STMRead answers one read: the value, or a conflict when the variable was
// published after the attempt began.
type STMRead struct {
	Value any
	Fresh bool
}

// BeginSTM starts an attempt at the current clock.
func BeginSTM() *STMLog {
	return &STMLog{readVersion: stmClock.Load(), reads: make(map[*TVar]struct{}), latest: make(map[*TVar]int)}
}

// Read answers the attempt's newest buffered write to v, or its published
// value if that is no newer than the attempt and no commit is publishing it.
func (l *STMLog) Read(v *TVar) STMRead {
	if index, ok := l.latest[v]; ok {
		return STMRead{Value: l.writes[index].value, Fresh: true}
	}
	// A commit locks v before taking its version from the clock and unlocks
	// it after publishing. An attempt that began after that version was
	// taken therefore finds v locked or already published; reading the old
	// node instead would mix two snapshots.
	if v.locked.Load() {
		return STMRead{}
	}
	node := v.current.Load()
	if node.version > l.readVersion {
		return STMRead{}
	}
	l.reads[v] = struct{}{}
	return STMRead{Value: node.value, Fresh: true}
}

// Write buffers value for v until commit.
func (l *STMLog) Write(v *TVar, value any) {
	l.latest[v] = len(l.writes)
	l.writes = append(l.writes, stmWrite{tvar: v, value: value})
}

// New creates a variable with value already published at version 0. Nothing
// else can reach it before the attempt commits, and an attempt that restarts
// leaves it unreachable.
func (l *STMLog) New(value any) *TVar {
	v := &TVar{id: stmIDs.Add(1)}
	v.current.Store(&tvarNode{value: value})
	return v
}

// Savepoint marks the attempt's writes so far.
func (l *STMLog) Savepoint() int64 { return int64(len(l.writes)) }

// Rollback discards the writes made since point. Reads are kept: they still
// decide whether the attempt commits and what a retry waits on.
func (l *STMLog) Rollback(point int64) {
	clear(l.latest)
	l.writes = l.writes[:point]
	for index, write := range l.writes {
		l.latest[write.tvar] = index
	}
}

// Commit publishes the attempt's writes, or reports a conflict when a variable
// it read has been published since it began or is being published now. An
// attempt that only read commits at once: its reads already proved the
// snapshot consistent.
//
// A writing commit locks its write set in variable order, so concurrent
// commits never wait on each other in a cycle; a lock is held only while its
// owner validates and publishes. It then takes the next clock version,
// validates its reads, publishes each final write at that version, and
// unlocks. The version is taken after locking, so an attempt that begins
// later finds every written variable locked or already published, and one
// that began earlier rejects the new nodes as stale.
func (l *STMLog) Commit() bool {
	if len(l.writes) == 0 {
		return true
	}
	written := make([]*TVar, 0, len(l.latest))
	for v := range l.latest {
		written = append(written, v)
	}
	slices.SortFunc(written, func(a, b *TVar) int {
		if a.id < b.id {
			return -1
		}
		return 1
	})
	for _, v := range written {
		for !v.locked.CompareAndSwap(false, true) {
			runtime.Gosched()
		}
	}
	unlock := func() {
		for _, v := range written {
			v.locked.Store(false)
		}
	}
	version := stmClock.Add(1)
	// With no commit between the attempt's start and this one, nothing it
	// read can have changed.
	if version != l.readVersion+1 {
		for v := range l.reads {
			if _, mine := l.latest[v]; v.locked.Load() && !mine || v.current.Load().version > l.readVersion {
				unlock()
				return false
			}
		}
	}
	for _, v := range written {
		v.current.Store(&tvarNode{value: l.writes[l.latest[v]].value, version: version})
	}
	unlock()
	for _, v := range written {
		v.wake()
	}
	return true
}

// wake signals and removes every waiter registered on v.
func (v *TVar) wake() {
	v.mu.Lock()
	for w := range v.waiters {
		select {
		case w.ready <- struct{}{}:
		default:
		}
	}
	v.waiters = nil
	v.mu.Unlock()
}

// Await blocks a retried attempt until another commit publishes a variable
// it read, answering true, or until scope is cancelled, answering false. An
// attempt that read nothing waits for cancellation alone. The waiter is
// withdrawn from every variable on return, whether woken or cancelled.
func (l *STMLog) Await(scope *AsyncScope) bool {
	if scope.Cancelled() {
		return false
	}
	w := &stmWaiter{ready: make(chan struct{}, 1)}
	for v := range l.reads {
		v.mu.Lock()
		if v.waiters == nil {
			v.waiters = make(map[*stmWaiter]struct{})
		}
		v.waiters[w] = struct{}{}
		v.mu.Unlock()
	}
	defer func() {
		for v := range l.reads {
			v.mu.Lock()
			delete(v.waiters, w)
			v.mu.Unlock()
		}
	}()
	// A commit that published before the registration above woke nobody;
	// its newer version is visible now. One that publishes later finds the
	// waiter registered.
	for v := range l.reads {
		if v.current.Load().version > l.readVersion {
			return true
		}
	}
	select {
	case <-w.ready:
		return true
	case <-scope.ctx.Done():
		return false
	}
}
