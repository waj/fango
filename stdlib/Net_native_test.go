package native

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"github.com/waj/fango/runtime/fangort"
)

func TestConnectionReadUsesAvailableBytes(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	connection := &connection{value: local, reader: bufio.NewReader(local)}
	go func() {
		_, _ = peer.Write([]byte("hello"))
		_ = peer.Close()
	}()
	for _, check := range []struct {
		max  int64
		want string
	}{{2, "he"}, {8, "llo"}, {8, ""}} {
		got, err := readConnectionBytes(connection, check.max)
		if err != nil || string(got) != check.want || cap(got) != len(got) {
			t.Fatalf("read(max=%d) = %q len=%d cap=%d, err=%v; want %q", check.max, got, len(got), cap(got), err, check.want)
		}
	}
}

func TestSystemSocketsBelongToTheirProvider(t *testing.T) {
	provider, other := NetSystemProvider(), NetSystemProvider()
	t.Cleanup(func() { NetRetireSystem(other) })
	listenerValue, err := SystemListen(provider, 0)
	if err != nil {
		t.Fatal(err)
	}
	l := listenerValue.(*fangort.OwnedResource).Value.(*listener)
	port := int64(l.value.Addr().(*net.TCPAddr).Port)

	accepted := make(chan any, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := SystemAccept(provider, listenerValue)
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()

	client, err := SystemConnect(provider, "127.0.0.1", port, 0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	var server any
	select {
	case server = <-accepted:
	case err := <-acceptErr:
		t.Fatal(err)
	}

	if err := SystemWrite(provider, client, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if got, err := SystemRead(provider, server, 4); err != nil || string(got) != "ping" {
		t.Fatalf("server read = %q, %v", got, err)
	}
	if _, err := SystemRead(other, server, 4); err == nil || !strings.Contains(err.Error(), "foreign connection") {
		t.Fatalf("foreign read = %v", err)
	}
	if err := SystemCloseConnection(provider, server); err != nil {
		t.Fatal(err)
	}
	if err := SystemCloseConnection(provider, server); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	if _, err := SystemRead(provider, server, 4); err == nil || !strings.Contains(err.Error(), "closed connection") {
		t.Fatalf("read after close = %v", err)
	}

	pool := SystemNewPool(provider, 1, 60000)
	if _, err := SystemPoolConnect(other, pool, "127.0.0.1", port, 0, false, "", false); err == nil || !strings.Contains(err.Error(), "foreign pool") {
		t.Fatalf("foreign pool = %v", err)
	}

	NetRetireSystem(provider)
	if !l.closed.Load() {
		t.Fatal("retirement left the listener open")
	}
	if _, err := SystemRead(provider, client, 1); err == nil || !strings.Contains(err.Error(), "closed provider") {
		t.Fatalf("read after retirement = %v", err)
	}
	if !SystemListenerStopped(provider, listenerValue) {
		t.Fatal("retired listener reported running")
	}
	late := SystemNewPool(provider, 1, 60000)
	if _, err := SystemPoolConnect(provider, late, "127.0.0.1", port, 0, false, "", false); err == nil || !strings.Contains(err.Error(), "closed provider") {
		t.Fatalf("pool after retirement = %v", err)
	}
}
