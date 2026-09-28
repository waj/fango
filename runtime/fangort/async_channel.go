package fangort

import (
	"context"
	"sync"
)

// AsyncChannelStatus is internal transport, not a source-language error type.
type AsyncChannelStatus uint8

const (
	AsyncChannelReady AsyncChannelStatus = iota
	AsyncChannelClosed
	AsyncChannelCancelled
)

type channelWaiter[T any] struct {
	done     chan struct{}
	value    T
	status   AsyncChannelStatus
	finished bool
}

// AsyncChannel linearizes transfer, close, and withdrawal under one lock.
// Wakeup channels carry no values and close exactly once while holding mu.
// The buffer and channel lifetime do not belong to an Async scope.
type AsyncChannel[T any] struct {
	mu        sync.Mutex
	closed    bool
	capacity  int
	buffer    []T
	head      int
	count     int
	senders   []*channelWaiter[T]
	receivers []*channelWaiter[T]
}

func NewAsyncChannel[T any](capacity int) *AsyncChannel[T] {
	if capacity < 0 {
		capacity = 0
	}
	return &AsyncChannel[T]{capacity: capacity}
}

func finishChannelWaiter[T any](w *channelWaiter[T], status AsyncChannelStatus) {
	w.status, w.finished = status, true
	close(w.done)
}

func popChannelWaiter[T any](queue *[]*channelWaiter[T]) *channelWaiter[T] {
	if len(*queue) == 0 {
		return nil
	}
	w := (*queue)[0]
	(*queue)[0] = nil
	*queue = (*queue)[1:]
	return w
}

func removeChannelWaiter[T any](queue *[]*channelWaiter[T], w *channelWaiter[T]) {
	for i, queued := range *queue {
		if queued == w {
			copy((*queue)[i:], (*queue)[i+1:])
			(*queue)[len(*queue)-1] = nil
			*queue = (*queue)[:len(*queue)-1]
			return
		}
	}
}

func (c *AsyncChannel[T]) await(ctx context.Context, w *channelWaiter[T], queue *[]*channelWaiter[T]) AsyncChannelStatus {
	c.mu.Unlock()
	select {
	case <-w.done:
	case <-ctx.Done():
	}
	c.mu.Lock()
	if !w.finished {
		removeChannelWaiter(queue, w)
		finishChannelWaiter(w, AsyncChannelCancelled)
	}
	status := w.status
	c.mu.Unlock()
	return status
}

func (c *AsyncChannel[T]) Send(ctx context.Context, value T) AsyncChannelStatus {
	c.mu.Lock()
	if ctx.Err() != nil {
		c.mu.Unlock()
		return AsyncChannelCancelled
	}
	if c.closed {
		c.mu.Unlock()
		return AsyncChannelClosed
	}
	if receiver := popChannelWaiter(&c.receivers); receiver != nil {
		receiver.value = value
		finishChannelWaiter(receiver, AsyncChannelReady)
		c.mu.Unlock()
		return AsyncChannelReady
	}
	if c.count < c.capacity {
		if c.buffer == nil {
			c.buffer = make([]T, c.capacity)
		}
		c.buffer[(c.head+c.count)%c.capacity] = value
		c.count++
		c.mu.Unlock()
		return AsyncChannelReady
	}
	w := &channelWaiter[T]{done: make(chan struct{}), value: value}
	c.senders = append(c.senders, w)
	return c.await(ctx, w, &c.senders)
}

func (c *AsyncChannel[T]) Receive(ctx context.Context) (T, AsyncChannelStatus) {
	c.mu.Lock()
	var zero T
	if ctx.Err() != nil {
		c.mu.Unlock()
		return zero, AsyncChannelCancelled
	}
	if c.count > 0 {
		value := c.buffer[c.head]
		c.buffer[c.head] = zero
		c.head = (c.head + 1) % c.capacity
		c.count--
		if sender := popChannelWaiter(&c.senders); sender != nil {
			c.buffer[(c.head+c.count)%c.capacity] = sender.value
			c.count++
			finishChannelWaiter(sender, AsyncChannelReady)
		}
		c.mu.Unlock()
		return value, AsyncChannelReady
	}
	if sender := popChannelWaiter(&c.senders); sender != nil {
		value := sender.value
		finishChannelWaiter(sender, AsyncChannelReady)
		c.mu.Unlock()
		return value, AsyncChannelReady
	}
	if c.closed {
		c.mu.Unlock()
		return zero, AsyncChannelClosed
	}
	w := &channelWaiter[T]{done: make(chan struct{})}
	c.receivers = append(c.receivers, w)
	status := c.await(ctx, w, &c.receivers)
	return w.value, status
}

func (c *AsyncChannel[T]) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	for _, sender := range c.senders {
		finishChannelWaiter(sender, AsyncChannelClosed)
	}
	for _, receiver := range c.receivers {
		finishChannelWaiter(receiver, AsyncChannelClosed)
	}
	c.senders, c.receivers = nil, nil
}
