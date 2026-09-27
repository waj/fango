package native

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSharedFileReadsConsumeEachLineOnce(t *testing.T) {
	var contents strings.Builder
	for i := range 200 {
		fmt.Fprintf(&contents, "%d\n", i)
	}
	path := filepath.Join(t.TempDir(), "lines")
	if err := os.WriteFile(path, []byte(contents.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := OpenRead(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseHandle(h) })
	lines := make(chan string, 200)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for {
				line, err := ReadHandleLine(h)
				if err != nil {
					t.Error(err)
					return
				}
				if line == "" {
					return
				}
				lines <- line
			}
		})
	}
	workers.Wait()
	close(lines)
	seen := map[string]bool{}
	for line := range lines {
		if seen[line] {
			t.Fatalf("line consumed twice: %q", line)
		}
		seen[line] = true
	}
	if len(seen) != 200 {
		t.Fatalf("read %d distinct lines, want 200", len(seen))
	}
}

type signalledRead struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *signalledRead) Read(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Read(p)
}

func TestSharedConnectionCloseInterruptsRead(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close(); _ = local.Close() })
	observed := &signalledRead{Conn: local, started: make(chan struct{})}
	c := &connection{value: observed, reader: bufio.NewReader(observed)}
	finished := make(chan error, 1)
	go func() {
		_, err := ReadConnectionBytes(c, 1)
		finished <- err
	}()
	<-observed.started
	closed := make(chan error, 1)
	go func() { closed <- CloseConnection(c) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close waited behind the blocked read")
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("blocked read succeeded after close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close did not interrupt read")
	}
	if _, err := ReadConnectionBytes(c, 1); err == nil {
		t.Fatal("read after close succeeded")
	}
}
