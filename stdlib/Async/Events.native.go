package native

import (
	"sync"
	"time"
)

type eventState struct {
	mu       sync.Mutex
	bridge   *FangoEventBridge
	queue    []int64
	capacity int
	policy   int64
	interval int64
	count    int64
	arm      int64
	started  bool
	finished bool
	overflow bool
	closed   bool
	stop     chan struct{}
	done     chan struct{}
}

func EventNew(interval, count, capacity, policy int64) any {
	return &eventState{
		capacity: int(capacity), policy: policy, interval: interval, count: count,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
}

func (s *eventState) notify(ticket int64) {
	if ticket != 0 {
		s.bridge.Notify(ticket)
	}
}

func (s *eventState) push(value int64) bool {
	s.mu.Lock()
	if s.closed || s.overflow {
		s.mu.Unlock()
		return false
	}
	if len(s.queue) == s.capacity {
		switch s.policy {
		case 0:
			s.overflow = true
		case 1:
			copy(s.queue, s.queue[1:])
			s.queue[len(s.queue)-1] = value
		case 2:
			// The new value is dropped.
		}
	} else {
		s.queue = append(s.queue, value)
	}
	ticket := s.arm
	s.mu.Unlock()
	s.notify(ticket)
	return true
}

func (s *eventState) finish() {
	s.mu.Lock()
	s.finished = true
	ticket := s.arm
	s.mu.Unlock()
	s.notify(ticket)
}

func Start(value, bridge any) {
	s := value.(*eventState)
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.bridge = bridge.(*FangoEventBridge)
	s.mu.Unlock()
	go func() {
		defer close(s.done)
		for i := int64(0); i < s.count; i++ {
			if s.interval > 0 {
				timer := time.NewTimer(time.Duration(s.interval) * time.Millisecond)
				select {
				case <-timer.C:
				case <-s.stop:
					timer.Stop()
					return
				}
			}
			select {
			case <-s.stop:
				return
			default:
			}
			if !s.push(i) {
				break
			}
		}
		s.finish()
	}()
}

func TakeEvent(value any) int64 {
	s := value.(*eventState)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.overflow {
		return -3
	}
	if len(s.queue) != 0 {
		item := s.queue[0]
		s.queue[0] = 0
		s.queue = s.queue[1:]
		return item
	}
	if s.finished {
		return -2
	}
	return -1
}

func Arm(value any, ticket int64) {
	s := value.(*eventState)
	s.mu.Lock()
	s.arm = ticket
	ready := s.overflow || s.finished || len(s.queue) != 0
	s.mu.Unlock()
	if ready {
		s.notify(ticket)
	}
}

func Disarm(value any, ticket int64) {
	s := value.(*eventState)
	s.mu.Lock()
	if s.arm == ticket {
		s.arm = 0
	}
	s.mu.Unlock()
}

func EventClose(value any) {
	s := value.(*eventState)
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.stop)
	}
	started := s.started
	s.mu.Unlock()
	if started {
		<-s.done
	}
	s.mu.Lock()
	s.bridge = nil
	s.queue = nil
	s.mu.Unlock()
}
