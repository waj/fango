package fangort

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

// atomicallyGo runs body until it commits, as the STM handler does.
func atomicallyGo(body func(*STMLog) bool) {
	for {
		log := BeginSTM()
		if body(log) && log.Commit() {
			return
		}
	}
}

func TestSTMReadsOwnWritesAndPublishesOnCommit(t *testing.T) {
	log := BeginSTM()
	v := log.New(1)
	if r := log.Read(v); !r.Fresh || r.Value != 1 {
		t.Fatalf("new variable read %+v", r)
	}
	log.Write(v, 2)
	if r := log.Read(v); r.Value != 2 {
		t.Fatalf("own write read %+v", r)
	}
	other := BeginSTM()
	if r := other.Read(v); r.Value != 1 {
		t.Fatalf("uncommitted write visible: %+v", r)
	}
	if !log.Commit() {
		t.Fatal("uncontended commit failed")
	}
	if r := BeginSTM().Read(v); r.Value != 2 {
		t.Fatalf("committed write not visible: %+v", r)
	}
}

func TestSTMStaleReadAndValidationConflict(t *testing.T) {
	setup := BeginSTM()
	a, b := setup.New(0), setup.New(0)
	reader := BeginSTM()
	if r := reader.Read(a); !r.Fresh {
		t.Fatal("first read stale")
	}
	writer := BeginSTM()
	writer.Write(b, 1)
	if !writer.Commit() {
		t.Fatal("writer conflicted")
	}
	// A later commit published b: reading it would mix snapshots.
	if r := reader.Read(b); r.Fresh {
		t.Fatalf("stale read answered %+v", r)
	}
	validated := BeginSTM()
	validated.Read(a)
	validated.Write(b, 2)
	interloper := BeginSTM()
	interloper.Write(a, 3)
	interloper.Commit()
	if validated.Commit() {
		t.Fatal("commit ignored a changed read")
	}
	readOnly := BeginSTM()
	readOnly.Read(a)
	interloper = BeginSTM()
	interloper.Write(a, 4)
	interloper.Commit()
	if !readOnly.Commit() {
		t.Fatal("read-only commit failed")
	}
}

func TestSTMConcurrentTransfersPreserveTotal(t *testing.T) {
	setup := BeginSTM()
	accounts := make([]*TVar, 8)
	for i := range accounts {
		accounts[i] = setup.New(int64(100))
	}
	var workers sync.WaitGroup
	for w := range 8 {
		workers.Go(func() {
			for i := range 1000 {
				from, to := accounts[(w+i)%8], accounts[(w*3+i*5+1)%8]
				atomicallyGo(func(log *STMLog) bool {
					f := log.Read(from)
					if !f.Fresh {
						return false
					}
					log.Write(from, f.Value.(int64)-1)
					g := log.Read(to)
					if !g.Fresh {
						return false
					}
					log.Write(to, g.Value.(int64)+1)
					return true
				})
			}
		})
	}
	workers.Wait()
	var total int64
	snapshot := BeginSTM()
	for _, account := range accounts {
		total += snapshot.Read(account).Value.(int64)
	}
	if total != 800 {
		t.Fatalf("total %d, want 800", total)
	}
}

func (v *TVar) waiterCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.waiters)
}

func TestSTMAwaitWakesOnCommitToARead(t *testing.T) {
	setup := BeginSTM()
	read, unrelated := setup.New(0), setup.New(0)
	scope := NewAsyncRoot(t.Context())
	blocked := BeginSTM()
	blocked.Read(read)
	woke := make(chan bool)
	go func() { woke <- blocked.Await(scope) }()
	for read.waiterCount() == 0 {
		runtime.Gosched()
	}
	other := BeginSTM()
	other.Write(unrelated, 1)
	other.Commit()
	select {
	case <-woke:
		t.Fatal("woken by an unrelated commit")
	case <-time.After(10 * time.Millisecond):
	}
	writer := BeginSTM()
	writer.Write(read, 1)
	writer.Commit()
	if !<-woke {
		t.Fatal("wait reported cancellation")
	}
	if n := read.waiterCount(); n != 0 {
		t.Fatalf("%d waiters left", n)
	}
}

// A commit landing between the attempt's read and its registration must
// still wake it: the registration rechecks versions after registering.
func TestSTMAwaitDoesNotLoseRacingCommit(t *testing.T) {
	scope := NewAsyncRoot(t.Context())
	for range 2000 {
		v := BeginSTM().New(0)
		blocked := BeginSTM()
		blocked.Read(v)
		done := make(chan bool)
		go func() { done <- blocked.Await(scope) }()
		writer := BeginSTM()
		writer.Write(v, 1)
		writer.Commit()
		select {
		case ok := <-done:
			if !ok {
				t.Fatal("wait reported cancellation")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("racing commit lost")
		}
	}
}

func TestSTMCancelledAwaitLeavesNoRegistration(t *testing.T) {
	setup := BeginSTM()
	a, b := setup.New(0), setup.New(0)
	scope := NewAsyncRoot(t.Context())
	blocked := BeginSTM()
	blocked.Read(a)
	blocked.Read(b)
	done := make(chan bool)
	go func() { done <- blocked.Await(scope) }()
	for a.waiterCount() == 0 || b.waiterCount() == 0 {
		runtime.Gosched()
	}
	scope.Cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("cancelled wait reported a wake")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled wait did not return")
	}
	if a.waiterCount()+b.waiterCount() != 0 {
		t.Fatal("cancelled waiter left a registration")
	}
	if BeginSTM().Await(scope) {
		t.Fatal("wait in a cancelled scope reported a wake")
	}
}

func TestSTMRollbackDiscardsWritesAndKeepsReads(t *testing.T) {
	setup := BeginSTM()
	a, b, c := setup.New(0), setup.New(0), setup.New(0)
	log := BeginSTM()
	log.Write(a, 1)
	point := log.Savepoint()
	log.Write(a, 2)
	log.Write(b, 2)
	log.Read(c)
	log.Rollback(point)
	if r := log.Read(a); r.Value != 1 {
		t.Fatalf("rolled-back write still visible: %+v", r)
	}
	if r := log.Read(b); r.Value != 0 {
		t.Fatalf("rolled-back variable read %+v", r)
	}
	writer := BeginSTM()
	writer.Write(c, 5)
	writer.Commit()
	if log.Commit() {
		t.Fatal("a read made before the rollback was not validated")
	}
}

// Commits whose read and write sets are disjoint: one variable per goroutine.
// With a global commit mutex these serialize; scaling with -cpu shows the
// contention STM7 measures.
func BenchmarkSTMDisjointCommits(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		v := BeginSTM().New(int64(0))
		for pb.Next() {
			log := BeginSTM()
			r := log.Read(v)
			log.Write(v, r.Value.(int64)+1)
			if !log.Commit() {
				b.Fatal("disjoint commit conflicted")
			}
		}
	})
}

// The same attempts without writes, which commit without the lock: the
// control for BenchmarkSTMDisjointCommits.
func BenchmarkSTMReadOnlyCommits(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		v := BeginSTM().New(int64(0))
		for pb.Next() {
			log := BeginSTM()
			log.Read(v)
			if !log.Commit() {
				b.Fatal("read-only commit conflicted")
			}
		}
	})
}
