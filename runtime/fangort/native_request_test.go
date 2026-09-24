package fangort

import (
	"sync"
	"sync/atomic"
	"testing"
)

func requestCounts(t *testing.T, h *NativeRequestHost, live, registered int64) {
	t.Helper()
	l, r := h.Counts()
	if l != live || r != registered {
		t.Fatalf("counts = %d/%d, want %d/%d", l, r, live, registered)
	}
}

func TestNativeRequestPublicationAndAdmission(t *testing.T) {
	h := NewNativeRequestHost(1)
	r := h.Reserve()
	if !r.Admitted() || h.Reserve().Admitted() {
		t.Fatal("capacity")
	}
	if !r.Begin(nil) || r.Begin(nil) {
		t.Fatal("registration must start once")
	}
	if !r.Complete() || r.Complete() {
		t.Fatal("publication must happen once")
	}
	if h.Claim(r) {
		t.Fatal("delivered before native quiescence")
	}
	r.Done()
	r.Done()
	requestCounts(t, h, 0, 1)
	if NewNativeRequestHost(1).Claim(r) {
		t.Fatal("foreign driver delivered callback")
	}
	if !h.Claim(r) || h.Claim(r) {
		t.Fatal("delivery must happen once")
	}
	requestCounts(t, h, 0, 0)
	r = h.Reserve()
	h.Close()
	if r.Begin(nil) || r.Complete() || h.Reserve().Admitted() {
		t.Fatal("closed scope admitted work")
	}
	requestCounts(t, h, 0, 0)
}

func TestNativeRequestDrainWaitsForWorkerAndCancellation(t *testing.T) {
	h := NewNativeRequestHost(1)
	r := h.Reserve()
	cancelEntered, cancelReturn := make(chan struct{}), make(chan struct{})
	var cancelled atomic.Int64
	r.Begin(func() { cancelled.Add(1); close(cancelEntered); <-cancelReturn })
	cancelledReturn := make(chan struct{})
	go func() { r.Cancel(); close(cancelledReturn) }()
	<-cancelEntered
	if r.Complete() {
		t.Fatal("cancelled delivery")
	}
	r.Done()
	requestCounts(t, h, 1, 1)
	drained := make(chan struct{})
	go func() { h.Close(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("released while cancellation still uses retained data")
	default:
	}
	close(cancelReturn)
	<-cancelledReturn
	<-drained
	if cancelled.Load() != 1 {
		t.Fatal("duplicate cancellation")
	}
	requestCounts(t, h, 0, 0)
}

func TestNativeRequestConcurrentNotificationsAndClose(t *testing.T) {
	for range 50 {
		h := NewNativeRequestHost(4)
		var workers sync.WaitGroup
		var publications atomic.Int64
		for range 4 {
			r := h.Reserve()
			stop := make(chan struct{})
			r.Begin(func() { close(stop) })
			workers.Add(1)
			go func() {
				defer workers.Done()
				var notify sync.WaitGroup
				for range 8 {
					notify.Add(1)
					go func() {
						defer notify.Done()
						if r.Complete() {
							publications.Add(1)
						}
					}()
				}
				notify.Wait()
				<-stop
				r.Done()
			}()
		}
		h.Close()
		workers.Wait()
		if publications.Load() > 4 {
			t.Fatal("duplicate readiness")
		}
		requestCounts(t, h, 0, 0)
	}
}

func TestNativeRequestPartialRegistrationCleanup(t *testing.T) {
	h := NewNativeRequestHost(2)
	_ = h.Reserve() // Source acquisition failed before Begin.
	r := h.Reserve()
	stop := make(chan struct{})
	r.Begin(func() { close(stop) })
	go func() { <-stop; r.Done() }() // Source acquisition failed after Begin.
	h.Close()
	requestCounts(t, h, 0, 0)
	if NewNativeRequestHost(-1).Reserve().Admitted() {
		t.Fatal("negative admission")
	}
}
