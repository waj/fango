package fangort

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"
)

// Completion stores data without applying a scope-wide error policy. Finish
// must still drain every ignored child before the parent publishes its value.
func TestAsyncValuesAndDrain(t *testing.T) {
	root := NewAsyncScope(nil)
	release := make(chan struct{})
	cancelled := make(chan struct{})
	child := SpawnAsync(root, func(scope *AsyncScope) AsyncCompletion {
		<-scope.Context().Done()
		close(cancelled)
		<-release
		return AsyncCompletion{Value: "Err missing"}
	})
	joined := make(chan AsyncCompletion, 1)
	go func() { joined <- root.Finish(AsyncCompletion{Value: "body"}) }()
	<-cancelled
	select {
	case <-joined:
		t.Fatal("returned before child cleanup")
	default:
	}
	close(release)
	if got := <-joined; got.Cancelled || got.Value != "body" {
		t.Fatalf("child value changed parent result: %+v", got)
	}
	if got := child.Wait(); !got.Cancelled {
		t.Fatalf("unfinished child was not cancelled: %+v", got)
	}
}

func TestAsyncObservationAndStableResults(t *testing.T) {
	root := NewAsyncScope(nil)
	task := SpawnAsync(root, func(*AsyncScope) AsyncCompletion { return AsyncCompletion{Value: "Err handled"} })
	var observers sync.WaitGroup
	for range 16 {
		observers.Go(func() {
			for range 3 {
				got := task.Wait()
				if got.Cancelled || got.Value != "Err handled" {
					t.Errorf("unstable result: %+v", got)
				}
				task.Cancel()
			}
		})
	}
	observers.Wait()
	if got := root.Finish(AsyncCompletion{Value: 7}); got.Cancelled || got.Value != 7 {
		t.Fatalf("observation changed parent: %+v", got)
	}
	if got := task.Wait(); got.Cancelled || got.Value != "Err handled" {
		t.Fatalf("handle lost result after runner: %+v", got)
	}
}

func TestAsyncIndividualCancellationAndWaitCancellation(t *testing.T) {
	root := NewAsyncScope(nil)
	started := make(chan struct{})
	child := SpawnAsync(root, func(s *AsyncScope) AsyncCompletion {
		close(started)
		<-s.Context().Done()
		return AsyncCompletion{Cancelled: true}
	})
	<-started
	observer := NewAsyncScope(nil)
	observer.Cancel()
	if got := child.Await(observer); !got.Cancelled {
		t.Fatalf("wait did not cancel: %+v", got)
	}
	if child.scope.Cancelled() {
		t.Fatal("cancelling the observer cancelled the target")
	}
	child.Cancel()
	if got := child.Wait(); !got.Cancelled {
		t.Fatalf("child not cancelled: %+v", got)
	}
	if got := root.Finish(AsyncCompletion{Value: 1}); got.Cancelled || got.Value != 1 {
		t.Fatalf("child cancellation propagated to parent: %+v", got)
	}
}

func TestAsyncReturnedErrorsAndCompletedValuesLeaveSiblingsRunning(t *testing.T) {
	root := NewAsyncScope(nil)
	ready, release := make(chan struct{}), make(chan struct{})
	sibling := SpawnAsync(root, func(s *AsyncScope) AsyncCompletion {
		close(ready)
		<-release
		return AsyncCompletion{Value: !s.Cancelled()}
	})
	<-ready
	task := SpawnAsync(root, func(*AsyncScope) AsyncCompletion { return AsyncCompletion{Value: "Err boom"} })
	task.Wait()
	completed := NewAsyncValue("Err completed")
	PublishAsyncValue(root, completed)
	completed.Wait()
	if sibling.scope.Cancelled() {
		t.Fatal("returned data cancelled a sibling")
	}
	close(release)
	if sibling.Wait().Value != true {
		t.Fatal("sibling was cancelled")
	}
	if got := root.Finish(AsyncCompletion{Value: 0}); got.Cancelled || got.Value != 0 {
		t.Fatalf("ignored error changed parent: %+v", got)
	}
}

func waitChannelQueue[T any](t *testing.T, c *AsyncChannel[T], senders, receivers int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.mu.Lock()
		ready := len(c.senders) == senders && len(c.receivers) == receivers
		c.mu.Unlock()
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("channel waiter did not register")
		}
		runtime.Gosched()
	}
}

func TestAsyncChannelCloseBlockedSendAndDrain(t *testing.T) {
	for _, capacity := range []int{0, 1, 8} {
		c := NewAsyncChannel[int](capacity)
		for i := range capacity {
			if c.Send(context.Background(), i) != AsyncChannelReady {
				t.Fatal("buffer send failed")
			}
		}
		result := make(chan AsyncChannelStatus, 1)
		go func() { result <- c.Send(context.Background(), 99) }()
		waitChannelQueue(t, c, 1, 0)
		c.Close()
		c.Close()
		if got := <-result; got != AsyncChannelClosed {
			t.Fatalf("blocked send: %v", got)
		}
		for i := range capacity {
			value, status := c.Receive(context.Background())
			if status != AsyncChannelReady || value != i {
				t.Fatalf("drain: %v %v, want %d", value, status, i)
			}
		}
		if _, got := c.Receive(context.Background()); got != AsyncChannelClosed {
			t.Fatalf("closed receive: %v", got)
		}
	}
}

func TestAsyncChannelCancellationWithdrawsWaiter(t *testing.T) {
	for _, send := range []bool{false, true} {
		c := NewAsyncChannel[int](0)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan AsyncChannelStatus, 1)
		if send {
			go func() { result <- c.Send(ctx, 1) }()
			waitChannelQueue(t, c, 1, 0)
		} else {
			go func() { _, status := c.Receive(ctx); result <- status }()
			waitChannelQueue(t, c, 0, 1)
		}
		cancel()
		if got := <-result; got != AsyncChannelCancelled {
			t.Fatalf("cancelled waiter: %v", got)
		}
		waitChannelQueue(t, c, 0, 0)
		fresh := make(chan AsyncChannelStatus, 1)
		go func() { fresh <- c.Send(context.Background(), 2) }()
		value, status := c.Receive(context.Background())
		if value != 2 || status != AsyncChannelReady || <-fresh != AsyncChannelReady {
			t.Fatal("cancelled waiter consumed a later value")
		}
	}
}

func TestAsyncChannelConcurrentCloseTransferCancel(t *testing.T) {
	for range 100 {
		c := NewAsyncChannel[int](3)
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				for i := range 100 {
					if c.Send(ctx, i) != AsyncChannelReady {
						return
					}
				}
			})
			wg.Go(func() {
				for {
					if _, status := c.Receive(ctx); status != AsyncChannelReady {
						return
					}
				}
			})
		}
		wg.Go(c.Close)
		wg.Go(cancel)
		wg.Wait()
	}
}

func TestAsyncChannelRingPreservesOrder(t *testing.T) {
	c := NewAsyncChannel[int](4)
	ctx := context.Background()
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for i := range 10000 {
			if c.Send(ctx, i) != AsyncChannelReady {
				return
			}
		}
		c.Close()
	}()
	for i := range 10000 {
		value, status := c.Receive(ctx)
		if status != AsyncChannelReady || value != i {
			t.Fatalf("at %d: %d %v", i, value, status)
		}
	}
	<-sent
	if _, status := c.Receive(ctx); status != AsyncChannelClosed {
		t.Fatal("not closed after draining")
	}
}

func TestAsyncSpawnFinishRace(t *testing.T) {
	for range 20 {
		root := NewAsyncScope(nil)
		var workers sync.WaitGroup
		for range 32 {
			workers.Go(func() {
				task := SpawnAsync(root, func(s *AsyncScope) AsyncCompletion {
					<-s.Context().Done()
					return AsyncCompletion{Cancelled: true}
				})
				if !task.Wait().Cancelled {
					t.Error("cancelled scope published success")
				}
			})
		}
		var finishers sync.WaitGroup
		for range 8 {
			finishers.Go(func() { root.Finish(AsyncCompletion{Cancelled: true}) })
		}
		finishers.Wait()
		workers.Wait()
		late := SpawnAsync(root, func(*AsyncScope) AsyncCompletion { t.Error("closed scope started child"); return AsyncCompletion{} })
		if !late.Wait().Cancelled {
			t.Fatal("closed scope accepted child")
		}
	}
}

func TestAsyncHostCancellationDrainsCleanup(t *testing.T) {
	host, cancel := context.WithCancel(context.Background())
	root := NewAsyncRoot(host)
	started, release := make(chan struct{}), make(chan struct{})
	child := SpawnAsync(root, func(s *AsyncScope) AsyncCompletion {
		close(started)
		<-s.Context().Done()
		<-release
		return AsyncCompletion{Cancelled: true}
	})
	<-started
	cancel()
	if !root.Cancelled() {
		t.Fatal("host cancellation did not reach root")
	}
	done := make(chan AsyncCompletion, 1)
	go func() { done <- root.Finish(AsyncCompletion{}) }()
	select {
	case <-done:
		t.Fatal("returned before child cleanup")
	default:
	}
	close(release)
	if !(<-done).Cancelled || !child.Wait().Cancelled {
		t.Fatal("lost cancellation")
	}
}

func TestAsyncTaskReturnDrainsDescendants(t *testing.T) {
	root := NewAsyncRoot(context.Background())
	cleanup, release := make(chan struct{}), make(chan struct{})
	parent := SpawnAsync(root, func(scope *AsyncScope) AsyncCompletion {
		SpawnAsync(scope, func(child *AsyncScope) AsyncCompletion {
			<-child.Context().Done()
			close(cleanup)
			<-release
			return AsyncCompletion{Cancelled: true}
		})
		return AsyncCompletion{Value: 42}
	})
	<-cleanup
	select {
	case <-parent.done:
		t.Fatal("published parent before descendant cleanup")
	default:
	}
	close(release)
	if got := parent.Wait(); got.Cancelled || got.Value != 42 {
		t.Fatalf("normal task exit cancelled its own result: %+v", got)
	}
	root.Finish(AsyncCompletion{})
}

func TestAsyncNestedScopeAndTaskCancellation(t *testing.T) {
	root := NewAsyncRoot(context.Background())
	nested := NewAsyncScope(root)
	nested.Finish(AsyncCompletion{Value: 7})
	if root.Cancelled() {
		t.Fatal("normal nested exit cancelled executing task")
	}
	child := SpawnAsync(root, func(scope *AsyncScope) AsyncCompletion {
		inner := NewAsyncScope(scope)
		inner.CancelTaskOwner()
		if !scope.Cancelled() {
			t.Error("nested cancellation did not reach executing task")
		}
		inner.Finish(AsyncCompletion{Cancelled: true})
		return AsyncCompletion{Cancelled: true}
	})
	if !child.Wait().Cancelled || root.Cancelled() {
		t.Fatal("task cancellation reached its starter")
	}
	root.Finish(AsyncCompletion{})
}

func TestAsyncCompletedTasksLeaveRegistry(t *testing.T) {
	root := NewAsyncRoot(context.Background())
	for range 1000 {
		task := SpawnAsync(root, func(*AsyncScope) AsyncCompletion { return AsyncCompletion{Value: 7} })
		task.Wait()
		root.mu.Lock()
		retained := len(root.children)
		root.mu.Unlock()
		if retained != 0 {
			t.Fatalf("retained %d completed tasks", retained)
		}
		if task.owner != nil {
			t.Fatal("completed handle retained its owner's registry")
		}
	}
	value := NewAsyncValue(42)
	PublishAsyncValue(root, value)
	root.Finish(AsyncCompletion{})
	if value.Wait().Value != 42 {
		t.Fatal("completed handle lost its value")
	}
}

func TestAsyncAwaitAny(t *testing.T) {
	root := NewAsyncRoot(context.Background())
	if got := AwaitAnyAsync(root, nil); got != -1 {
		t.Fatalf("empty selection: %d", got)
	}
	ready := NewAsyncValue(42)
	PublishAsyncValue(root, ready)
	blocked := SpawnAsync(root, func(scope *AsyncScope) AsyncCompletion {
		<-scope.Context().Done()
		return AsyncCompletion{Cancelled: true}
	})
	for range 3 {
		if got := AwaitAnyAsync(root, []*AsyncTask{blocked, ready}); got != 1 {
			t.Fatalf("already-completed selection: %d", got)
		}
	}
	blocked.Cancel()
	blocked.Wait()
	if got := AwaitAnyAsync(root, []*AsyncTask{blocked}); got != 0 {
		t.Fatalf("cancelled target selection: %d", got)
	}
	root.Finish(AsyncCompletion{})
}

func TestAsyncAwaitAnyCancellationWithdrawsSubscriptions(t *testing.T) {
	root := NewAsyncRoot(context.Background())
	target := SpawnAsync(root, func(scope *AsyncScope) AsyncCompletion {
		<-scope.Context().Done()
		return AsyncCompletion{Cancelled: true}
	})
	observer := NewAsyncRoot(context.Background())
	selected := make(chan int, 1)
	go func() { selected <- AwaitAnyAsync(observer, []*AsyncTask{target, target}) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		target.mu.Lock()
		registered := len(target.waiters) != 0
		target.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("selection did not register")
		}
		runtime.Gosched()
	}
	observer.Cancel()
	if got := <-selected; got != -2 {
		t.Fatalf("waiter cancellation: %d", got)
	}
	target.mu.Lock()
	retained := len(target.waiters)
	target.mu.Unlock()
	if retained != 0 || target.scope.Cancelled() {
		t.Fatal("selection retained subscriptions or cancelled unrelated target")
	}
	observer.Finish(AsyncCompletion{Cancelled: true})
	root.Finish(AsyncCompletion{})
}

func TestAsyncAwaitAnyCompletionRegistrationRace(t *testing.T) {
	for range 100 {
		root := NewAsyncRoot(context.Background())
		release := make(chan struct{})
		task := SpawnAsync(root, func(*AsyncScope) AsyncCompletion {
			<-release
			return AsyncCompletion{Value: 7}
		})
		selected := make(chan int, 1)
		go func() { selected <- AwaitAnyAsync(root, []*AsyncTask{task}) }()
		close(release)
		if got := <-selected; got != 0 || task.Wait().Value != 7 {
			t.Fatal("lost completion racing with selection registration")
		}
		root.Finish(AsyncCompletion{})
	}
}
