package fangort

import (
	"sync"
	"sync/atomic"
)

// Software transactional memory for the STM library, after TL2 with a global
// version clock. An attempt records the clock when it begins and treats any
// variable published at a later version as a conflict, so every attempt reads
// one consistent snapshot. Writes are buffered in the attempt's log; a writing
// commit validates the reads and publishes the writes under one mutex. Reads
// and read-only commits take no lock.
//
// Values are opaque payload tokens: the runtime stores and returns them
// without inspecting them.

// stmClock is the version of the latest writing commit.
var stmClock atomic.Uint64

// stmCommit is held by one writing commit at a time.
var stmCommit sync.Mutex

// tvarNode is one immutable published value of a TVar.
type tvarNode struct {
	value   any
	version uint64
}

// TVar is a transactional variable. Its identity distinguishes it.
type TVar struct {
	current atomic.Pointer[tvarNode]
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
// value if that is no newer than the attempt.
func (l *STMLog) Read(v *TVar) STMRead {
	if index, ok := l.latest[v]; ok {
		return STMRead{Value: l.writes[index].value, Fresh: true}
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
	v := &TVar{}
	v.current.Store(&tvarNode{value: value})
	return v
}

// Commit publishes the attempt's writes, or reports a conflict when a variable
// it read has been published since it began. An attempt that only read
// commits at once: its reads already proved the snapshot consistent.
func (l *STMLog) Commit() bool {
	if len(l.writes) == 0 {
		return true
	}
	stmCommit.Lock()
	for v := range l.reads {
		if v.current.Load().version > l.readVersion {
			stmCommit.Unlock()
			return false
		}
	}
	// Publish every node before advancing the clock: an attempt that begins
	// later sees all of them, and one that began earlier either sees old nodes
	// or rejects the new ones as stale.
	version := stmClock.Load() + 1
	for v, index := range l.latest {
		v.current.Store(&tvarNode{value: l.writes[index].value, version: version})
	}
	stmClock.Store(version)
	stmCommit.Unlock()
	for v := range l.latest {
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
