package fangort

import "sync"

// NativeEventBridge is a bounded source of scalar completion notifications for
// one cooperative driver. Native workers never receive the driver's Fango state.
type NativeEventBridge struct {
	mu             sync.Mutex
	cond           *sync.Cond
	capacity       int64
	next           int64
	active         map[int64]bool
	queued         []int64
	capacityQueued bool
	closed         bool
	interrupted    bool
}

var eventBridges = struct {
	sync.Mutex
	active map[*NativeEventBridge]bool
}{active: make(map[*NativeEventBridge]bool)}

func NewNativeEventBridge(capacity int64) *NativeEventBridge {
	if capacity < 0 {
		capacity = 0
	}
	b := &NativeEventBridge{capacity: capacity, active: make(map[int64]bool)}
	b.cond = sync.NewCond(&b.mu)
	eventBridges.Lock()
	eventBridges.active[b] = true
	eventBridges.Unlock()
	return b
}

// InterruptNativeBridges wakes every active cooperative driver. It is called
// by the interpreter worker on Ctrl-C, without touching evaluator state.
func InterruptNativeBridges() bool {
	eventBridges.Lock()
	bridges := make([]*NativeEventBridge, 0, len(eventBridges.active))
	for bridge := range eventBridges.active {
		bridges = append(bridges, bridge)
	}
	eventBridges.Unlock()
	for _, bridge := range bridges {
		bridge.Interrupt()
	}
	return len(bridges) != 0
}

func (b *NativeEventBridge) Interrupt() {
	b.mu.Lock()
	b.interrupted = true
	b.cond.Broadcast()
	b.mu.Unlock()
}

// Reserve returns zero when all native slots are occupied.
func (b *NativeEventBridge) Reserve() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || int64(len(b.active)) >= b.capacity {
		return 0
	}
	b.next++
	b.active[b.next] = true
	return b.next
}

func (b *NativeEventBridge) Available() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closed && int64(len(b.active)) < b.capacity
}

func (b *NativeEventBridge) Ready(ticket int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	active, ok := b.active[ticket]
	return ok && !active
}

// Notify is idempotent. A ticket is queued at most once, including when native
// work finishes before its Fango task registers a wait.
func (b *NativeEventBridge) Notify(ticket int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || !b.active[ticket] {
		return
	}
	b.active[ticket] = false
	b.queued = append(b.queued, ticket)
	b.cond.Signal()
}

// Release frees admission only after the request has drained. Zero announces
// capacity to parked submitters; stale ticket notifications are harmless.
func (b *NativeEventBridge) Release(ticket int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.active[ticket]; !ok {
		return
	}
	delete(b.active, ticket)
	for i := 0; i < len(b.queued); {
		if b.queued[i] == ticket {
			copy(b.queued[i:], b.queued[i+1:])
			b.queued = b.queued[:len(b.queued)-1]
		} else {
			i++
		}
	}
	if !b.closed {
		if !b.capacityQueued {
			b.queued = append(b.queued, 0)
			b.capacityQueued = true
			b.cond.Signal()
		}
	}
}

func (b *NativeEventBridge) Take() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.takeLocked()
}

func (b *NativeEventBridge) takeLocked() int64 {
	if b.interrupted {
		return -2
	}
	if len(b.queued) == 0 {
		return -1
	}
	ticket := b.queued[0]
	b.queued[0] = 0
	b.queued = b.queued[1:]
	if ticket == 0 {
		b.capacityQueued = false
	}
	return ticket
}

func (b *NativeEventBridge) Wait() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.queued) == 0 && !b.closed && !b.interrupted {
		b.cond.Wait()
	}
	return b.takeLocked()
}

func (b *NativeEventBridge) Close() {
	eventBridges.Lock()
	delete(eventBridges.active, b)
	eventBridges.Unlock()
	b.mu.Lock()
	b.closed = true
	b.cond.Broadcast()
	b.mu.Unlock()
}
