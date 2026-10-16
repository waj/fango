package fangort

import (
	"sync"
	"testing"
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
