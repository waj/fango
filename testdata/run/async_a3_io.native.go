package native

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type testServer struct {
	listener net.Listener
	server   *http.Server
	arrived  int
	mu       sync.Mutex
	release  chan struct{}
	done     chan struct{}
}

func OpenServer() any {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s := &testServer{listener: listener, release: make(chan struct{}), done: make(chan struct{})}
	s.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.arrived++
		if s.arrived == 2 {
			close(s.release)
		}
		s.mu.Unlock()
		select {
		case <-s.release:
			_, _ = w.Write([]byte(r.URL.Path[1:]))
		case <-time.After(3 * time.Second):
			http.Error(w, "fetches did not overlap", http.StatusGatewayTimeout)
		case <-r.Context().Done():
		}
	})}
	go func() {
		_ = s.server.Serve(listener)
		close(s.done)
	}()
	return s
}

func CloseServer(value any) {
	s := value.(*testServer)
	_ = s.server.Close()
	<-s.done
}

func ServerURL(value any) string {
	return "http://" + value.(*testServer).listener.Addr().String()
}
