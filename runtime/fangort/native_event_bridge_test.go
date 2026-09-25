package fangort

import "testing"

func TestNativeEventBridgeAdmissionAndNotifications(t *testing.T) {
	b := NewNativeEventBridge(1)
	first := b.Reserve()
	if first == 0 || b.Reserve() != 0 || b.Available() {
		t.Fatal("bridge exceeded its admission capacity")
	}
	b.Notify(first)
	b.Notify(first)
	if !b.Ready(first) || b.Take() != first || b.Take() != -1 {
		t.Fatal("completion was lost or delivered twice")
	}
	if b.Available() {
		t.Fatal("completed request released admission before drain")
	}
	b.Release(first)
	if b.Take() != 0 || !b.Available() {
		t.Fatal("drain did not wake capacity waiters")
	}
	second := b.Reserve()
	if second <= first {
		t.Fatal("bridge reused a stale ticket")
	}
	b.Notify(first)
	if b.Take() != -1 || b.Ready(second) {
		t.Fatal("stale notification reached a new request")
	}
	b.Release(second)
	b.Close()
	if b.Reserve() != 0 || b.Wait() != 0 {
		// Release queued a capacity notification before closure.
		t.Fatal("unexpected closed bridge behavior")
	}
}

func TestNativeEventBridgeInterruptWinsQueuedReadiness(t *testing.T) {
	b := NewNativeEventBridge(1)
	ticket := b.Reserve()
	b.Notify(ticket)
	if !InterruptNativeBridges() || b.Take() != -2 || b.Wait() != -2 {
		t.Fatal("queued readiness hid cancellation")
	}
	b.Release(ticket)
	b.Close()
}
