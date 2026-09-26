package fangort

import (
	"context"
	"sync"
	"testing"
)

func TestTaskResultsAndPanic(t *testing.T) {
	scope := NewTaskScope()
	defer scope.Close()
	tasks := make([]*Task, 64)
	for i := range tasks {
		tasks[i] = SpawnTask(scope, func(*TaskContext) any { return i })
	}
	scope.Join()
	for i, task := range tasks {
		for range 2 {
			if task.Status() != 0 || task.Value() != i {
				t.Fatalf("task %d: status %d", i, task.Status())
			}
		}
	}
	failed := SpawnTask(scope, func(*TaskContext) any { panic("worker failed") })
	if failed.Status() != 2 || failed.Fault() != "worker failed" {
		t.Fatal("lost worker failure")
	}
}

func TestTaskCloseCancelsAndJoinsEveryChild(t *testing.T) {
	scope := NewTaskScope()
	started := make(chan struct{})
	release := make(chan struct{})
	task := SpawnTask(scope, func(ctx *TaskContext) any {
		close(started)
		<-ctx.Done()
		<-release
		return 42
	})
	<-started
	var closers sync.WaitGroup
	for range 8 {
		closers.Go(scope.Close)
	}
	close(release)
	closers.Wait()
	if task.Status() != 1 {
		t.Fatal("cancelled child published success")
	}
	late := SpawnTask(scope, func(*TaskContext) any { t.Error("closed scope started child"); return nil })
	if late.Status() != 3 {
		t.Fatal("closed scope accepted child")
	}
}

func TestTaskSpawnCloseRace(t *testing.T) {
	for range 20 {
		scope := NewTaskScope()
		var workers sync.WaitGroup
		for range 32 {
			workers.Go(func() {
				task := SpawnTask(scope, func(ctx *TaskContext) any { <-ctx.Done(); return nil })
				task.Wait()
				if status := task.Status(); status != 1 && status != 3 {
					t.Errorf("status %d", status)
				}
			})
		}
		scope.Close()
		workers.Wait()
	}
}

func TestTaskCancellationPoints(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tc := &TaskContext{ctx}
	if TaskSleep(tc, 0) || TaskSleep(tc, 1<<62) {
		t.Fatal("sleep ignored cancellation")
	}
	scope := NewTaskScope()
	defer scope.Close()
	task := SpawnTask(scope, func(ctx *TaskContext) any { <-ctx.Done(); return nil })
	if task.WaitIn(tc) {
		t.Fatal("await ignored cancellation")
	}
	task.Cancel()
	task.Wait()
	if !task.WaitIn(tc) {
		t.Fatal("completed result should remain observable")
	}
}
