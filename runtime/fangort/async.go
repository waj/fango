package fangort

import (
	"context"
	"sync"
	"time"
)

// AsyncCompletion separates application failure from cooperative cancellation.
// Payloads are sealed by the compiler boundary; the runtime never interprets them.
// A Go panic is not an AsyncCompletion and is deliberately not recovered here.
type AsyncCompletion struct {
	Value     any
	Failure   any
	Failed    bool
	Cancelled bool
}

// AsyncScope owns only its children, not the resources their closures reference.
// Each task has its own child scope, so cancellation follows the task tree.
type AsyncScope struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	children []*AsyncTask
	closing  bool
}

type AsyncTask struct {
	scope    *AsyncScope
	owner    *AsyncScope
	done     chan struct{}
	result   AsyncCompletion // published by closing done
	observed bool            // guarded by owner.mu; detached completed tasks need no observation
}

func NewAsyncScope(parent *AsyncScope) *AsyncScope {
	ctx := context.Background()
	if parent != nil {
		ctx = parent.ctx
	}
	ctx, cancel := context.WithCancel(ctx)
	return &AsyncScope{ctx: ctx, cancel: cancel}
}

func (s *AsyncScope) Context() context.Context { return s.ctx }
func (s *AsyncScope) Cancel()                  { s.cancel() }
func (s *AsyncScope) Cancelled() bool          { return s.ctx.Err() != nil }

// SpawnAsync registers the child before starting it. The callback must run its
// source cleanup before returning; Finish then drains all of that child's tasks.
func SpawnAsync(owner *AsyncScope, body func(*AsyncScope) AsyncCompletion) *AsyncTask {
	child := NewAsyncScope(owner)
	task := &AsyncTask{owner: owner, scope: child, done: make(chan struct{})}
	owner.mu.Lock()
	if owner.closing {
		owner.mu.Unlock()
		child.Cancel()
		task.result.Cancelled = true
		close(task.done)
		return task
	}
	owner.children = append(owner.children, task)
	owner.mu.Unlock()
	go func() { task.publish(child.Finish(body(child))) }()
	return task
}

func (t *AsyncTask) publish(result AsyncCompletion) {
	t.result = result
	if result.Failed && t.owner != nil {
		// A failure cancels siblings, but leaves the parent's body independent.
		// The parent may observe and handle the failure.
		t.owner.mu.Lock()
		for _, sibling := range t.owner.children {
			if sibling != t {
				sibling.Cancel()
			}
		}
		t.owner.mu.Unlock()
	}
	close(t.done)
}

func CompletedAsync(owner *AsyncScope, result AsyncCompletion) *AsyncTask {
	task := &AsyncTask{owner: owner, done: make(chan struct{})}
	owner.mu.Lock()
	if owner.closing {
		result = AsyncCompletion{Cancelled: true}
	} else {
		owner.children = append(owner.children, task)
	}
	owner.mu.Unlock()
	task.publish(result)
	return task
}

func (t *AsyncTask) Cancel() {
	if t.scope != nil {
		t.scope.Cancel()
	}
}

func (t *AsyncTask) observe() AsyncCompletion {
	if t.owner != nil {
		t.owner.mu.Lock()
		t.observed = true
		t.owner.mu.Unlock()
	}
	return t.result
}

// Wait is an observation independent of any source Async runner.
func (t *AsyncTask) Wait() AsyncCompletion {
	<-t.done
	return t.observe()
}

// Await stops waiting when the observer is cancelled. It does not observe the
// target's outcome on that path, and does not implicitly cancel the target.
func (t *AsyncTask) Await(observer *AsyncScope) AsyncCompletion {
	if observer.Cancelled() {
		return AsyncCompletion{Cancelled: true}
	}
	select {
	case <-t.done:
		return t.observe()
	case <-observer.ctx.Done():
		return AsyncCompletion{Cancelled: true}
	}
}

// Finish is called once by a scope's owner after its body and cleanup finish.
// Body failure wins; otherwise the earliest submitted, unobserved failed child
// wins. All children finish before any result is returned.
func (s *AsyncScope) Finish(body AsyncCompletion) AsyncCompletion {
	s.mu.Lock()
	s.closing = true
	children := append([]*AsyncTask(nil), s.children...)
	s.mu.Unlock()
	if body.Failed || body.Cancelled {
		s.Cancel()
	}
	for _, child := range children {
		<-child.done
	}
	s.mu.Lock()
	if !body.Failed {
		for _, child := range children {
			if child.result.Failed && !child.observed {
				child.observed = true
				body = AsyncCompletion{Failed: true, Failure: child.result.Failure}
				break
			}
		}
	}
	s.mu.Unlock()
	if !body.Failed && s.Cancelled() {
		body = AsyncCompletion{Cancelled: true}
	}
	s.Cancel() // release the context link after choosing the stable result
	return body
}

func AsyncSleep(scope *AsyncScope, milliseconds int64) bool {
	if scope.Cancelled() {
		return false
	}
	if milliseconds <= 0 {
		return true
	}
	if milliseconds > int64((1<<63-1)/time.Millisecond) {
		milliseconds = int64((1<<63 - 1) / time.Millisecond)
	}
	timer := time.NewTimer(time.Duration(milliseconds) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-scope.ctx.Done():
		return false
	case <-timer.C:
		return !scope.Cancelled()
	}
}
