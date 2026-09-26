package fangort

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Tasks own their execution; only immutable inputs/results cross the source boundary.
type TaskContext struct{ context.Context }
type TaskScope struct {
	mu       sync.Mutex
	closed   bool
	drained  chan struct{}
	children []*Task
}
type Task struct {
	done   chan struct{}
	cancel context.CancelFunc
	value  any
	fault  string
	status int64 // 0 success, 1 cancellation, 2 panic, 3 closed scope
}

func NewTaskScope() *TaskScope { return &TaskScope{drained: make(chan struct{})} }
func SpawnTask(scope *TaskScope, run func(*TaskContext) any) *Task {
	return SpawnTaskIn(scope, context.Background(), run)
}

// SpawnTaskIn attaches host evaluation cancellation; source task contexts are independent.
func SpawnTaskIn(scope *TaskScope, parent context.Context, run func(*TaskContext) any) *Task {
	ctx, cancel := context.WithCancel(parent)
	task := &Task{done: make(chan struct{}), cancel: cancel}
	scope.mu.Lock()
	if scope.closed {
		scope.mu.Unlock()
		task.status = 3
		cancel()
		close(task.done)
		return task
	}
	scope.children = append(scope.children, task)
	scope.mu.Unlock()
	go func() {
		defer func() {
			if p := recover(); p != nil {
				task.status = 2
				task.fault = fmt.Sprint(p)
			}
			cancel()
			close(task.done)
		}()
		task.value = run(&TaskContext{ctx})
		if ctx.Err() != nil {
			task.status = 1
			task.value = nil
		}
	}()
	return task
}
func (scope *TaskScope) Join() {
	scope.mu.Lock()
	children := append([]*Task(nil), scope.children...)
	scope.mu.Unlock()
	for _, task := range children {
		<-task.done
	}
}
func (scope *TaskScope) Close() {
	scope.mu.Lock()
	if scope.closed {
		drained := scope.drained
		scope.mu.Unlock()
		<-drained
		return
	}
	scope.closed = true
	children := scope.children
	scope.children = nil
	scope.mu.Unlock()
	for _, task := range children {
		task.cancel()
	}
	for _, task := range children {
		<-task.done
	}
	close(scope.drained)
}
func (task *Task) Cancel() { task.cancel() }
func (task *Task) Wait()   { <-task.done }
func (task *Task) WaitIn(ctx *TaskContext) bool {
	select {
	case <-task.done:
		return true
	default:
	}
	select {
	case <-task.done:
		return true
	case <-ctx.Done():
		return false
	}
}
func (task *Task) Status() int64 { task.Wait(); return task.status }
func (task *Task) Fault() string { task.Wait(); return task.fault }
func (task *Task) Value() any {
	task.Wait()
	if task.status != 0 {
		panic("task has no successful result")
	}
	return task.value
}
func TaskSleep(ctx *TaskContext, milliseconds int64) bool {
	if ctx.Err() != nil {
		return false
	}
	if milliseconds <= 0 {
		return true
	}
	// Avoid overflowing time.Duration for an untrusted source integer.
	if milliseconds > int64((1<<63-1)/time.Millisecond) {
		milliseconds = int64((1<<63 - 1) / time.Millisecond)
	}
	timer := time.NewTimer(time.Duration(milliseconds) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
