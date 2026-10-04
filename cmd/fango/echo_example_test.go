package main

import (
	"bytes"
	"context"
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
	cmd := exec.Command(cliCompiledBinary(t, path), strconv.Itoa(port))
	connection := startTCPServer(t, cmd, port)
	echo(t, connection, "hello\nsecond line\r\n")
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}

	// Closing one client returns the server to accept rather than ending it.
	connection = dialEcho(t, port)
	defer connection.Close()
	echo(t, connection, "another client\n")
}

// startTCPServer allows startup to contend with parallel compiler tests while
// still reporting an exited server immediately. Only Wait's completion makes
// it safe to read the output buffer written by os/exec's copying goroutine.
func startTCPServer(t *testing.T, cmd *exec.Cmd, port int) net.Conn {
	t.Helper()
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	dialer := net.Dialer{Timeout: 100 * time.Millisecond}
	for {
		select {
		case <-done:
			t.Fatalf("server at %s exited before accepting connections: %v\n%s", address, waitErr, &output)
		default:
		}
		connection, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			return connection
		}
		select {
		case <-done:
			t.Fatalf("server at %s exited before accepting connections: %v\n%s", address, waitErr, &output)
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-done
			t.Fatalf("waiting for server at %s: %v (last dial: %v)\n%s", address, ctx.Err(), err, &output)
		case <-time.After(10 * time.Millisecond):
		}
	}
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
