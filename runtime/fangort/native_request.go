package fangort

import "sync"

// NativeRequestHost bounds registrations and owns their native quiescence.
// It never contains Fango callbacks, interpreter state, or a NativeHost.
type NativeRequestHost struct {
	mu       sync.Mutex
	limit    int
	closed   bool
	requests map[*NativeRequest]bool
}

// NativeRequest is the only authority a sidecar may retain. Begin must precede
// starting native work, and Done must follow its last access to retained data.
type NativeRequest struct {
	host                                             *NativeRequestHost
	admitted, begun, ready, done, claimed, cancelled bool
	cancel                                           func()
	quiescent                                        chan struct{}
	cancelling                                       bool
	cancelDone                                       chan struct{}
}

func NewNativeRequestHost(limit int64) *NativeRequestHost {
	if limit < 0 {
		limit = 0
	}
	return &NativeRequestHost{limit: int(limit), requests: make(map[*NativeRequest]bool)}
}

func (h *NativeRequestHost) Reserve() *NativeRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := &NativeRequest{host: h}
	if !h.closed && len(h.requests) < h.limit {
		r.admitted = true
		h.requests[r] = true
	}
	return r
}

func (r *NativeRequest) Admitted() bool { return r.admitted }

// Begin installs cancellation before work can publish or outlive registration.
// A rejected Begin must not start work. cancel must not call Fango or FangoHost.
func (r *NativeRequest) Begin(cancel func()) bool {
	h := r.host
	h.mu.Lock()
	defer h.mu.Unlock()
	if !r.admitted || r.begun || r.cancelled || h.closed {
		return false
	}
	r.begun, r.cancel, r.quiescent = true, cancel, make(chan struct{})
	return true
}

// Complete publishes readiness once; it does not imply native quiescence.
func (r *NativeRequest) Complete() bool {
	h := r.host
	h.mu.Lock()
	defer h.mu.Unlock()
	if !r.begun || r.ready || r.cancelled || r.done || h.closed {
		return false
	}
	r.ready = true
	return true
}

// Done acknowledges that no native code will access retained resources again.
// Duplicate and stale acknowledgements are harmless.
func (r *NativeRequest) Done() {
	h := r.host
	h.mu.Lock()
	defer h.mu.Unlock()
	if !r.begun || r.done {
		return
	}
	r.done, r.cancel = true, nil
	close(r.quiescent)
	if (r.cancelled || r.claimed) && !r.cancelling {
		delete(h.requests, r)
	}
}

// Claim grants one delivery, only to the matching driver and after quiescence.
func (h *NativeRequestHost) Claim(r *NativeRequest) bool {
	if r.host != h {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || r.cancelled || r.claimed || !r.ready || !r.done {
		return false
	}
	r.claimed = true
	delete(h.requests, r)
	return true
}

func (r *NativeRequest) Cancel() {
	h := r.host
	h.mu.Lock()
	if r.cancelled || r.claimed {
		h.mu.Unlock()
		return
	}
	r.cancelled = true
	cancel := r.cancel
	r.cancel = nil
	if cancel != nil {
		r.cancelling, r.cancelDone = true, make(chan struct{})
	}
	if (!r.begun || r.done) && !r.cancelling {
		delete(h.requests, r)
	}
	h.mu.Unlock()
	if cancel != nil {
		cancel()
		h.mu.Lock()
		r.cancelling = false
		close(r.cancelDone)
		if r.done {
			delete(h.requests, r)
		}
		h.mu.Unlock()
	}
}

func (r *NativeRequest) Drain() {
	r.Cancel()
	h := r.host
	h.mu.Lock()
	done := r.quiescent
	cancelDone := r.cancelDone
	h.mu.Unlock()
	if done != nil {
		<-done
	}
	if cancelDone != nil {
		<-cancelDone
	}
}

func (h *NativeRequestHost) Close() {
	h.mu.Lock()
	h.closed = true
	requests := make([]*NativeRequest, 0, len(h.requests))
	for r := range h.requests {
		requests = append(requests, r)
	}
	h.mu.Unlock()
	// Revoke every delivery before joining any request.
	for _, r := range requests {
		r.Cancel()
	}
	for _, r := range requests {
		r.Drain()
	}
}

func (h *NativeRequestHost) Counts() (live, registrations int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for r := range h.requests {
		registrations++
		if r.begun && !r.done || r.cancelling {
			live++
		}
	}
	return
}
