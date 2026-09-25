package native

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"
)

const maxBody = 1 << 20

type requestState struct {
	mu     sync.Mutex
	live   bool
	closed bool
	ok     bool
	body   string
	err    string
}

func NewState() any { return &requestState{} }

func CloseState(value any) {
	s := value.(*requestState)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live {
		panic("native request state released before drain")
	}
	s.closed = true
}

func (s *requestState) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.live {
		panic("invalid native request state")
	}
	s.live = true
}

func (s *requestState) finish(ok bool, body, err string) {
	s.mu.Lock()
	s.ok, s.body, s.err, s.live = ok, body, err, false
	s.mu.Unlock()
}

func Ok(value any) bool {
	s := value.(*requestState)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ok
}
func Body(value any) string {
	s := value.(*requestState)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body
}
func ErrorText(value any) string {
	s := value.(*requestState)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func begin(token, bridge, state any, ticket int64, cancel context.CancelFunc) (*FangoRequest, *requestState, bool) {
	r := token.(*FangoRequest)
	if !r.Begin(cancel) {
		cancel()
		return nil, nil, false
	}
	b := bridge.(*FangoEventBridge)
	if !r.OnDone(func() { b.Notify(ticket) }) {
		panic("native request completion notifier rejected")
	}
	s := state.(*requestState)
	s.start()
	return r, s, true
}

func SubmitSleep(token, bridge, state any, ticket, millis int64) {
	ctx, cancel := context.WithCancel(context.Background())
	r, s, started := begin(token, bridge, state, ticket, cancel)
	if !started {
		return
	}
	go func() {
		if millis < 0 {
			millis = 0
		}
		timer := time.NewTimer(time.Duration(millis) * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
		s.finish(ctx.Err() == nil, "", "")
		r.Complete()
		r.Done()
		cancel()
	}()
}

func SubmitGet(token, bridge, state any, ticket int64, url string) {
	ctx, cancel := context.WithCancel(context.Background())
	r, s, started := begin(token, bridge, state, ticket, cancel)
	if !started {
		return
	}
	go func() {
		transport := &http.Transport{DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		var response *http.Response
		if err == nil {
			response, err = client.Do(request)
		}
		var data []byte
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(response.Body, maxBody+1))
			closeErr := response.Body.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil && len(data) > maxBody {
				err = errors.New("response exceeds one MiB")
			}
		}
		if err == nil && response.StatusCode >= 400 {
			err = errors.New(response.Status)
		}
		if err == nil && !utf8.Valid(data) {
			err = errors.New("response is not UTF-8 text")
		}
		if err != nil {
			s.finish(false, "", err.Error())
		} else {
			s.finish(true, string(data), "")
		}
		r.Complete()
		r.Done()
		cancel()
	}()
}
