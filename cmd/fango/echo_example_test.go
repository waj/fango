package main

import (
	"bytes"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestEchoExample(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "examples", "echo.fango")
	port := unusedTCPPort(t)
	var output bytes.Buffer
	cmd := exec.Command(cliCompiledBinary(t, path), strconv.Itoa(port))
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	connection := dialEcho(t, port)
	echo(t, connection, "hello\nsecond line\r\n")
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}

	// Closing one client returns the server to accept rather than ending it.
	connection = dialEcho(t, port)
	defer connection.Close()
	echo(t, connection, "another client\n")
}

func unusedTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func dialEcho(t *testing.T, port int) net.Conn {
	t.Helper()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(5 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			return connection
		}
		if time.Now().After(deadline) {
			t.Fatalf("dialing echo server at %s: %v", address, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func echo(t *testing.T, connection net.Conn, sent string) {
	t.Helper()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(connection, sent); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(sent))
	if _, err := io.ReadFull(connection, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != sent {
		t.Fatalf("echo = %q, want %q", got, sent)
	}
}
