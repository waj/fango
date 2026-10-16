package native

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/waj/fango/runtime/fangort"
)

// atomicallyGo runs body until it commits, as the STM handler does.
func atomicallyGo(body func(*stmLog) bool) {
	for {
		log := beginSTM()
		if body(log) && log.commit() {
			return
		}
	}
}

func TestSTMReadsOwnWritesAndPublishesOnCommit(t *testing.T) {
	log := beginSTM()
	v := log.new(1)
	if r := log.read(v); !r.Fresh || r.Value != 1 {
		t.Fatalf("new variable read %+v", r)
	}
	log.write(v, 2)
	if r := log.read(v); r.Value != 2 {
		t.Fatalf("own write read %+v", r)
	}
	other := beginSTM()
	if r := other.read(v); r.Value != 1 {
		t.Fatalf("uncommitted write visible: %+v", r)
	}
	if !log.commit() {
		t.Fatal("uncontended commit failed")
	}
	if r := beginSTM().read(v); r.Value != 2 {
		t.Fatalf("committed write not visible: %+v", r)
	}
}

func TestSTMStaleReadAndValidationConflict(t *testing.T) {
	setup := beginSTM()
	a, b := setup.new(0), setup.new(0)
	reader := beginSTM()
	if r := reader.read(a); !r.Fresh {
		t.Fatal("first read stale")
	}
	writer := beginSTM()
	writer.write(b, 1)
	if !writer.commit() {
		t.Fatal("writer conflicted")
	}
	// A later commit published b: reading it would mix snapshots.
	if r := reader.read(b); r.Fresh {
		t.Fatalf("stale read answered %+v", r)
	}
	validated := beginSTM()
	validated.read(a)
	validated.write(b, 2)
	interloper := beginSTM()
	interloper.write(a, 3)
	interloper.commit()
	if validated.commit() {
		t.Fatal("commit ignored a changed read")
	}
	readOnly := beginSTM()
	readOnly.read(a)
	interloper = beginSTM()
	interloper.write(a, 4)
	interloper.commit()
	if !readOnly.commit() {
		t.Fatal("read-only commit failed")
	}
}

func TestSTMConcurrentTransfersPreserveTotal(t *testing.T) {
	setup := beginSTM()
	accounts := make([]*stmVar, 8)
	for i := range accounts {
		accounts[i] = setup.new(int64(100))
	}
	var workers sync.WaitGroup
	for w := range 8 {
		workers.Go(func() {
			for i := range 1000 {
				from, to := accounts[(w+i)%8], accounts[(w*3+i*5+1)%8]
				atomicallyGo(func(log *stmLog) bool {
					f := log.read(from)
					if !f.Fresh {
						return false
					}
					log.write(from, f.Value.(int64)-1)
					g := log.read(to)
					if !g.Fresh {
						return false
					}
					log.write(to, g.Value.(int64)+1)
					return true
				})
			}
		})
	}
	workers.Wait()
	var total int64
	snapshot := beginSTM()
	for _, account := range accounts {
		total += snapshot.read(account).Value.(int64)
	}
	if total != 800 {
		t.Fatalf("total %d, want 800", total)
	}
}

func (v *stmVar) waiterCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.waiters)
}

func TestSTMAwaitWakesOnCommitToARead(t *testing.T) {
	setup := beginSTM()
	read, unrelated := setup.new(0), setup.new(0)
	scope := fangort.NewAsyncRoot(t.Context())
	blocked := beginSTM()
	blocked.read(read)
	woke := make(chan bool)
	go func() { woke <- blocked.await(scope) }()
	for read.waiterCount() == 0 {
		runtime.Gosched()
	}
	other := beginSTM()
	other.write(unrelated, 1)
	other.commit()
	select {
	case <-woke:
		t.Fatal("woken by an unrelated commit")
	case <-time.After(10 * time.Millisecond):
	}
	writer := beginSTM()
	writer.write(read, 1)
	writer.commit()
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
	scope := fangort.NewAsyncRoot(t.Context())
	for range 2000 {
		v := beginSTM().new(0)
		blocked := beginSTM()
		blocked.read(v)
		done := make(chan bool)
		go func() { done <- blocked.await(scope) }()
		writer := beginSTM()
		writer.write(v, 1)
		writer.commit()
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
	setup := beginSTM()
	a, b := setup.new(0), setup.new(0)
	scope := fangort.NewAsyncRoot(t.Context())
	blocked := beginSTM()
	blocked.read(a)
	blocked.read(b)
	done := make(chan bool)
	go func() { done <- blocked.await(scope) }()
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
	if beginSTM().await(scope) {
		t.Fatal("wait in a cancelled scope reported a wake")
	}
}

func TestSTMRollbackDiscardsWritesAndKeepsReads(t *testing.T) {
	setup := beginSTM()
	a, b, c := setup.new(0), setup.new(0), setup.new(0)
	log := beginSTM()
	log.write(a, 1)
	point := log.savepoint()
	log.write(a, 2)
	log.write(b, 2)
	log.read(c)
	log.rollback(point)
	if r := log.read(a); r.Value != 1 {
		t.Fatalf("rolled-back write still visible: %+v", r)
	}
	if r := log.read(b); r.Value != 0 {
		t.Fatalf("rolled-back variable read %+v", r)
	}
	writer := beginSTM()
	writer.write(c, 5)
	writer.commit()
	if log.commit() {
		t.Fatal("a read made before the rollback was not validated")
	}
}

// Commits whose read and write sets are disjoint: one variable per goroutine.
// They share only the clock increment; scaling with -cpu shows how much they
// still contend.
func BenchmarkSTMDisjointCommits(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		v := beginSTM().new(int64(0))
		for pb.Next() {
			log := beginSTM()
			r := log.read(v)
			log.write(v, r.Value.(int64)+1)
			if !log.commit() {
				b.Fatal("disjoint commit conflicted")
			}
		}
	})
}

// The same attempts without writes, which commit without the lock: the
// control for BenchmarkSTMDisjointCommits.
func BenchmarkSTMReadOnlyCommits(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		v := beginSTM().new(int64(0))
		for pb.Next() {
			log := beginSTM()
			log.read(v)
			if !log.commit() {
				b.Fatal("read-only commit conflicted")
			}
		}
	})
}
