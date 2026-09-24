package native

import "sync"

type device struct {
	mu     sync.Mutex
	finish chan struct{}
	done   chan struct{}
	live   int
}

func OpenDevice() any { return &device{} }
func CloseDevice(value any) {
	d := value.(*device)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.live != 0 {
		panic("device released before native quiescence")
	}
	FangoHost.WriteOutput([]byte("device released\n"))
}
func Submit(token, value any, mode int64) {
	r, d := token.(*FangoRequest), value.(*device)
	stop, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	if !r.Begin(func() { close(stop) }) {
		return
	}
	if mode == 0 {
		if !r.Complete() || r.Complete() {
			panic("duplicate publication")
		}
		r.Done()
		r.Done()
		return
	}
	d.mu.Lock()
	d.finish, d.done = finish, done
	d.live++
	d.mu.Unlock()
	go func() {
		select {
		case <-finish:
			r.Complete()
		case <-stop:
		}
		d.mu.Lock()
		d.live--
		d.mu.Unlock()
		r.Done()
		close(done)
	}()
}
func Finish(value any) {
	d := value.(*device)
	d.mu.Lock()
	finish, done := d.finish, d.done
	d.mu.Unlock()
	close(finish)
	<-done
}
