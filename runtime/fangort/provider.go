package fangort

import "sync"

// ResourceProvider is the identity of one service runner. Tokens are fresh
// objects compared by identity, never integers. A real provider owns native
// resources and closes those still open when it retires; a virtual provider
// only proves which runner created a Fango-side resource value.
type ResourceProvider struct {
	mu        sync.Mutex
	active    bool
	real      bool
	resources []*OwnedResource
}

// OwnedResource is a native resource of a real provider. Label names it in
// errors: a path for files, an address for sockets.
type OwnedResource struct {
	owner   *ResourceProvider
	Label   string
	Value   any
	closed  bool // protected by owner.mu
	release func() error
}

// ResourceError reports a provider check. ClassifyIOError and
// ClassifyNetError keep its label as the error's path or address.
type ResourceError struct {
	Label, Problem string
}

func (e *ResourceError) Error() string {
	if e.Label == "" {
		return e.Problem
	}
	return e.Label + ": " + e.Problem
}

func NewResourceProvider(real bool) *ResourceProvider {
	return &ResourceProvider{active: true, real: real}
}

// CheckedProvider projects a provider token crossing the native boundary.
func CheckedProvider(value any) (*ResourceProvider, error) {
	p, ok := value.(*ResourceProvider)
	if !ok || p == nil {
		return nil, &ResourceError{Problem: "invalid provider"}
	}
	return p, nil
}

// Status reports whether the provider may still be used.
func (p *ResourceProvider) Status(label string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active {
		return &ResourceError{Label: label, Problem: "closed provider"}
	}
	return nil
}

// Retire makes the provider unusable and releases its open resources in
// reverse acquisition order, answering the first release failure.
func (p *ResourceProvider) Retire() error {
	p.mu.Lock()
	p.active = false
	resources := p.resources
	p.resources = nil
	for _, r := range resources {
		r.closed = true
	}
	p.mu.Unlock()
	var first error
	for i := len(resources) - 1; i >= 0; i-- {
		if err := resources[i].release(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Adopt makes a newly acquired native resource owned by an active real
// provider. Acquisition runs without the provider lock, since a dial may block;
// a resource acquired while the provider retired is released at once.
func (p *ResourceProvider) Adopt(label string, acquire func() (any, func() error, error)) (*OwnedResource, error) {
	if err := p.Status(label); err != nil {
		return nil, err
	}
	if !p.real {
		return nil, &ResourceError{Label: label, Problem: "system provider expected"}
	}
	value, release, err := acquire()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if !p.active {
		p.mu.Unlock()
		_ = release()
		return nil, &ResourceError{Label: label, Problem: "closed provider"}
	}
	r := &OwnedResource{owner: p, Label: label, Value: value, release: release}
	p.resources = append(p.resources, r)
	p.mu.Unlock()
	return r, nil
}

// Use validates a resource before its native value is projected: it must
// belong to this active provider and, unless closing, still be open. Closing
// marks it closed and forgets it, and a repeated close is accepted so the
// caller's idempotent release can run again. The noun names the resource kind
// in errors.
func (p *ResourceProvider) Use(value any, noun string, closing bool) (*OwnedResource, error) {
	r, ok := value.(*OwnedResource)
	if !ok || r == nil {
		return nil, &ResourceError{Problem: "invalid " + noun}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.owner != p {
		return nil, &ResourceError{Label: r.Label, Problem: "foreign " + noun}
	}
	if !p.active {
		return nil, &ResourceError{Label: r.Label, Problem: "closed provider"}
	}
	if r.closed && !closing {
		return nil, &ResourceError{Label: r.Label, Problem: "closed " + noun}
	}
	if closing && !r.closed {
		r.closed = true
		p.forget(r)
	}
	return r, nil
}

func (p *ResourceProvider) forget(r *OwnedResource) {
	for i, resource := range p.resources {
		if resource == r {
			copy(p.resources[i:], p.resources[i+1:])
			p.resources[len(p.resources)-1] = nil
			p.resources = p.resources[:len(p.resources)-1]
			return
		}
	}
}

// ResourceLabel reads an owned resource's immutable label without a lock.
func ResourceLabel(value any) string {
	if r, ok := value.(*OwnedResource); ok && r != nil {
		return r.Label
	}
	return ""
}

// OpenCount reports how many resources the provider still tracks.
func (p *ResourceProvider) OpenCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.resources)
}

// Retired answers an already closed resource of this provider, for an
// infallible acquisition attempted after retirement; using it reports the
// closed provider.
func (p *ResourceProvider) Retired(label string) *OwnedResource {
	return &OwnedResource{owner: p, Label: label, closed: true, release: func() error { return nil }}
}
