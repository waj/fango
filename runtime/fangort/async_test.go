package fangort

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestAsyncFailureOrderAndDrain(t *testing.T) {
	for _, bodyFails := range []bool{false, true} {
		root := NewAsyncScope(nil)
		gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
		tasks := make([]*AsyncTask, 2)
		for i := range tasks {
			tasks[i] = SpawnAsync(root, func(*AsyncScope) AsyncCompletion {
				<-gates[i]
				return AsyncCompletion{Failed: true, Failure: i}
			})
		}
		close(gates[1])
		<-tasks[1].done // completion order must not determine the reported failure
		joined := make(chan AsyncCompletion, 1)
		body := AsyncCompletion{Value: "body"}
		if bodyFails {
			body = AsyncCompletion{Failed: true, Failure: 42}
		}
		go func() { joined <- root.Finish(body) }()
		select {
		case <-joined:
			t.Fatal("returned before child cleanup")
		default:
		}
		close(gates[0])
		got := <-joined
		want := 0
		if bodyFails {
			want = 42
		}
		if !got.Failed || got.Failure != want {
			t.Fatalf("got %+v, want failure %d", got, want)
		}
	}
}

func TestAsyncObservationAndStableResults(t *testing.T) {
	root := NewAsyncScope(nil)
	task := SpawnAsync(root, func(*AsyncScope) AsyncCompletion { return AsyncCompletion{Failed: true, Failure: "handled"} })
	for range 3 {
		got := task.Wait()
		if !got.Failed || got.Failure != "handled" {
			t.Fatalf("unstable result: %+v", got)
		}
		task.Cancel()
	}
	if got := root.Finish(AsyncCompletion{Value: 7}); got.Failed || got.Cancelled || got.Value != 7 {
		t.Fatalf("observed failure escaped: %+v", got)
	}
	if got := task.Wait(); !got.Failed {
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
	if got := root.Finish(AsyncCompletion{Value: 1}); got.Cancelled || got.Failed || got.Value != 1 {
		t.Fatalf("child cancellation propagated to parent: %+v", got)
	}
}

func TestAsyncFailureCancelsSiblings(t *testing.T) {
	root := NewAsyncScope(nil)
	ready := make(chan struct{})
	sibling := SpawnAsync(root, func(s *AsyncScope) AsyncCompletion {
		close(ready)
		<-s.Context().Done()
		return AsyncCompletion{Cancelled: true}
	})
	<-ready
	SpawnAsync(root, func(*AsyncScope) AsyncCompletion { return AsyncCompletion{Failed: true, Failure: "boom"} })
	got := root.Finish(AsyncCompletion{Value: 0})
	if !got.Failed || got.Failure != "boom" {
		t.Fatalf("missing unobserved failure: %+v", got)
	}
	if !sibling.Wait().Cancelled {
		t.Fatal("sibling not cancelled")
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

func TestAsyncCompletedFailureParticipatesInScope(t *testing.T) {
	for _, observed := range []bool{false, true} {
		root := NewAsyncScope(nil)
		task := CompletedAsync(root, AsyncCompletion{Failed: true, Failure: "completed"})
		if observed {
			task.Wait()
		}
		got := root.Finish(AsyncCompletion{Value: 5})
		if got.Failed == observed {
			t.Fatalf("observed=%v: %+v", observed, got)
		}
		if got.Cancelled {
			t.Fatal("completed failure became cancellation")
		}
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
