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
	provider := SystemProvider()
	t.Cleanup(func() { _ = SystemRetireProvider(provider) })
	h, err := SystemOpenRead(provider, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SystemCloseHandle(provider, h) })
	lines := make(chan string, 200)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for {
				line, err := SystemReadHandleLine(provider, h)
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

func TestVirtualProviderIdentityAndRetirement(t *testing.T) {
	first, second := FreshProvider(), FreshProvider()
	if !SameProvider(first, first) || SameProvider(first, second) {
		t.Fatal("provider identity is not by object")
	}
	RetireProvider(first)
	if err := ProviderStatus(first); err == nil || !strings.Contains(err.Error(), "closed provider") {
		t.Fatalf("retired provider: %v", err)
	}
	if err := ProviderStatus(second); err != nil {
		t.Fatal(err)
	}
	RetireProvider(second)
}

func TestSystemProviderChecksBeforeNativeProjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner, other, fake := SystemProvider(), SystemProvider(), FreshProvider()
	h, err := SystemOpenRead(owner, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SystemReadHandleBytes(other, h, 1); err == nil || !strings.Contains(err.Error(), "foreign handle") {
		t.Fatalf("foreign projection: %v", err)
	}
	if _, err := SystemOpenRead(fake, path); err == nil || !strings.Contains(err.Error(), "system provider expected") {
		t.Fatalf("native file under virtual provider: %v", err)
	}
	if SystemFilePath(h) != path {
		t.Fatalf("path = %q", SystemFilePath(h))
	}
	if err := SystemRetireProvider(owner); err != nil {
		t.Fatal(err)
	}
	if err := SystemRetireProvider(other); err != nil {
		t.Fatal(err)
	}
	RetireProvider(fake)
}

func TestSystemProviderClosesRawHandlesAndForgetsClosedOnes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	provider := SystemProvider()
	h, err := SystemOpenWrite(provider, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SystemCloseHandle(provider, h); err != nil {
		t.Fatal(err)
	}
	if err := SystemCloseHandle(provider, h); err != nil {
		t.Fatal(err)
	}
	if len(provider.(*fileProvider).resources) != 0 {
		t.Fatal("closed native file retained by provider")
	}
	raw, err := SystemOpenWrite(provider, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SystemRetireProvider(provider); err != nil {
		t.Fatal(err)
	}
	if err := SystemWriteHandleBytes(provider, raw, []byte("late")); err == nil {
		t.Fatal("raw handle escaped provider lifetime")
	}
}
