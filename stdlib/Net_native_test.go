package native

import (
	"bufio"
	"net"
	"testing"
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
		got, err := ReadConnectionBytes(connection, check.max)
		if err != nil || string(got) != check.want || cap(got) != len(got) {
			t.Fatalf("read(max=%d) = %q len=%d cap=%d, err=%v; want %q", check.max, got, len(got), cap(got), err, check.want)
		}
	}
}

func TestSocketOpaqueValuesRoundTrip(t *testing.T) {
	listenerValue, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	l := listenerValue.(*listener)
	port := int64(l.value.Addr().(*net.TCPAddr).Port)

	accepted := make(chan any, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := AcceptConnection(listenerValue)
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()

	client, err := Dial("127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	var server any
	select {
	case server = <-accepted:
	case err := <-acceptErr:
		t.Fatal(err)
	}

	if err := WriteConnectionBytes(client, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if has, err := ConnectionHasInput(server); err != nil || !has {
		t.Fatalf("server input = %v, %v", has, err)
	}
	if got, err := ReadConnectionBytes(server, 4); err != nil || string(got) != "ping" {
		t.Fatalf("server read = %q, %v", got, err)
	}
	if err := WriteConnectionBytes(server, []byte("pong")); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadConnectionBytes(client, 4); err != nil || string(got) != "pong" {
		t.Fatalf("client read = %q, %v", got, err)
	}

	if err := CloseConnection(server); err != nil {
		t.Fatal(err)
	}
	if err := CloseConnection(client); err != nil {
		t.Fatal(err)
	}
	if err := CloseListener(listenerValue); err != nil {
		t.Fatal(err)
	}
	if _, err := ConnectionHasInput(client); err == nil {
		t.Fatal("closed connection remained usable")
	}
}
