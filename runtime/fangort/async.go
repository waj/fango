package fangort

import (
	"context"
	"sync"
	"time"
)

// AsyncCompletion contains an ordinary value or cooperative cancellation.
// Payloads are sealed by the compiler boundary; the runtime never interprets them.
// A Go panic is not an AsyncCompletion and is deliberately not recovered here.
type AsyncCompletion struct {
	Value     any
	Cancelled bool
}

// AsyncScope owns only its children, not the resources their closures reference.
// Each task has its own child scope, so cancellation follows the task tree.
type AsyncScope struct {
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	children   map[*AsyncTask]struct{}
	taskOwner  *AsyncScope
	closing    bool
	finishOnce sync.Once
	finished   chan struct{}
	completion AsyncCompletion
}

type AsyncTask struct {
	scope   *AsyncScope
	owner   *AsyncScope
	done    chan struct{}
	result  AsyncCompletion // published by closing done
	mu      sync.Mutex
	waiters map[*asyncSelection]int
}

func NewAsyncScope(parent *AsyncScope) *AsyncScope {
	ctx := context.Background()
	if parent != nil {
		ctx = parent.ctx
	}
	scope := NewAsyncRoot(ctx)
	if parent != nil {
		scope.taskOwner = parent.taskOwner
	}
	return scope
}

// NewAsyncRoot connects a source runner to its host evaluation.
func NewAsyncRoot(ctx context.Context) *AsyncScope {
	ctx, cancel := context.WithCancel(ctx)
	scope := &AsyncScope{ctx: ctx, cancel: cancel, children: make(map[*AsyncTask]struct{}), finished: make(chan struct{})}
	scope.taskOwner = scope
	return scope
}

func (s *AsyncScope) Context() context.Context { return s.ctx }
func (s *AsyncScope) Cancel()                  { s.cancel() }
func (s *AsyncScope) Cancelled() bool          { return s.ctx.Err() != nil }

// CancelTaskOwner propagates observed cancellation through the executing task,
// including observation inside a nested lifetime scope.
func (s *AsyncScope) CancelTaskOwner() { s.taskOwner.Cancel() }

// SpawnAsync registers the child before starting it. The callback must run its
// source cleanup before returning; Finish then drains all of that child's tasks.
func SpawnAsync(owner *AsyncScope, body func(*AsyncScope) AsyncCompletion) *AsyncTask {
	child := NewAsyncScope(owner)
	child.taskOwner = child
	task := &AsyncTask{scope: child, owner: owner, done: make(chan struct{})}
	owner.mu.Lock()
	if owner.closing {
		owner.mu.Unlock()
		child.Cancel()
		task.owner = nil
		task.result.Cancelled = true
		close(task.done)
		return task
	}
	owner.children[task] = struct{}{}
	owner.mu.Unlock()
	go func() { task.publish(child.Finish(body(child))) }()
	return task
}

func (t *AsyncTask) publish(result AsyncCompletion) {
	if t.owner != nil {
		t.owner.mu.Lock()
		defer t.owner.mu.Unlock()
		delete(t.owner.children, t)
		t.owner = nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.result = result
	close(t.done)
	for selection, index := range t.waiters {
		selection.report(index)
	}
	t.waiters = nil
}

func (t *AsyncTask) Cancel() {
	if t.scope != nil {
		t.scope.Cancel()
	}
}

// Wait is an observation independent of any source Async runner.
func (t *AsyncTask) Wait() AsyncCompletion {
	<-t.done
	return t.result
}

// Await stops waiting when the observer is cancelled. It does not observe the
// target's outcome on that path, and does not implicitly cancel the target.
func (t *AsyncTask) Await(observer *AsyncScope) AsyncCompletion {
	result, _ := t.AwaitObserved(observer)
	return result
}

// AwaitObserved reports whether the target was delivered. Cancellation after
// selecting a completed target cannot retroactively hide that observation.
func (t *AsyncTask) AwaitObserved(observer *AsyncScope) (AsyncCompletion, bool) {
	if observer.Cancelled() {
		return AsyncCompletion{Cancelled: true}, false
	}
	select {
	case <-t.done:
		return t.result, true
	case <-observer.ctx.Done():
		return AsyncCompletion{Cancelled: true}, false
	}
}

type asyncSelection struct{ ready chan int }

func (s *asyncSelection) report(index int) {
	select {
	case s.ready <- index:
	default:
	}
}

// AwaitAnyAsync observes one terminal completion without adopting its task.
// Empty input returns -1; waiter cancellation returns -2. Selection among
// simultaneously ready completions is intentionally unspecified.
func AwaitAnyAsync(observer *AsyncScope, tasks []*AsyncTask) int {
	if observer.Cancelled() {
		return -2
	}
	if len(tasks) == 0 {
		return -1
	}
	selection := &asyncSelection{ready: make(chan int, 1)}
	defer func() {
		for _, task := range tasks {
			task.mu.Lock()
			delete(task.waiters, selection)
			task.mu.Unlock()
		}
	}()
	for i, task := range tasks {
		task.mu.Lock()
		select {
		case <-task.done:
			selection.report(i)
		default:
			if task.waiters == nil {
				task.waiters = make(map[*asyncSelection]int)
			}
			task.waiters[selection] = i
		}
		task.mu.Unlock()
	}
	select {
	case index := <-selection.ready:
		return index
	case <-observer.ctx.Done():
		return -2
	}
}

// Finish is called once by a scope's owner after its body and cleanup finish.
// Every exit cancels unfinished children and drains their cleanup before any
// result is returned; their values do not change the scope outcome.
func (s *AsyncScope) Finish(body AsyncCompletion) AsyncCompletion {
	s.finishOnce.Do(func() {
		s.completion = s.finish(body)
		close(s.finished)
	})
	if s.completion.Cancelled {
		return s.completion
	}
	return body
}

func (s *AsyncScope) Completion() AsyncCompletion {
	<-s.finished
	return s.completion
}

func (s *AsyncScope) finish(body AsyncCompletion) AsyncCompletion {
	s.mu.Lock()
	s.closing = true
	children := make([]*AsyncTask, 0, len(s.children))
	for child := range s.children {
		children = append(children, child)
	}
	s.mu.Unlock()
	if body.Cancelled {
		s.Cancel()
	}
	for _, child := range children {
		child.Cancel()
	}
	for _, child := range children {
		<-child.done
	}
	if s.Cancelled() {
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

// NewAsyncValue prepares a typed result before its owner publishes completion.
func NewAsyncValue(value any) *AsyncTask {
	return &AsyncTask{done: make(chan struct{}), result: AsyncCompletion{Value: value}}
}

func PublishAsyncValue(owner *AsyncScope, task *AsyncTask) {
	result := task.result
	owner.mu.Lock()
	if owner.closing {
		result = AsyncCompletion{Cancelled: true}
	}
	owner.mu.Unlock()
	task.publish(result)
}

func (t *AsyncTask) Result() AsyncCompletion { <-t.done; return t.result }
