package main

import (
	"bufio"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHTTPServerExample(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "http_server.fango")
	port := unusedTCPPort(t)
	cmd := exec.Command(cliCompiledBinary(t, path), strconv.Itoa(port))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	connection := dialEcho(t, port)
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(connection, "GET /hello/Ada HTTP/1.1\r\nHost: localhost\r\n\r\nGET /hello/Bob HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	wire, err := io.ReadAll(connection)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Hello, Ada!", "Hello, Bob!"} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("response lacks %q: %q", want, wire)
		}
	}

	chunked := socketHTTP(t, port, "POST /echo HTTP/1.1\r\nHost: localhost\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n2\r\nab\r\n1\r\nc\r\n0\r\n\r\n")
	if !strings.Contains(chunked, "HTTP/1.1 200") || !strings.HasSuffix(chunked, "\r\n\r\nabc") {
		t.Fatalf("chunked request: %q", chunked)
	}
	streamed := socketHTTP(t, port, "POST /stream HTTP/1.1\r\nHost: localhost\r\nContent-Length: 3\r\nConnection: close\r\n\r\nabc")
	if !strings.Contains(streamed, "HTTP/1.1 200") || !strings.HasSuffix(streamed, "\r\n\r\nabc") {
		t.Fatalf("streamed request body: %q", streamed)
	}
	malformed := socketHTTP(t, port, "GET /hello/Ada HTTP/1.1\r\nHost: one\r\nHost: two\r\n\r\n")
	if !strings.Contains(malformed, "HTTP/1.1 400") {
		t.Fatalf("malformed request: %q", malformed)
	}
	oversized := socketHTTP(t, port, "GET /hello/Ada HTTP/1.1\r\nHost: localhost\r\nX-Fill: "+strings.Repeat("a", 70000)+"\r\n\r\n")
	if !strings.Contains(oversized, "HTTP/1.1 431") {
		t.Fatalf("oversized headers: %q", oversized)
	}
	short := socketHTTPWithEOF(t, port, "POST /echo HTTP/1.1\r\nHost: localhost\r\nContent-Length: 5\r\n\r\nabc")
	if !strings.Contains(short, "HTTP/1.1 400") {
		t.Fatalf("short request body: %q", short)
	}
	slow := dialEcho(t, port)
	defer slow.Close()
	if _, err := io.WriteString(slow, "GET /hello/Slow HTTP/1.1\r\nHost: "); err != nil {
		t.Fatal(err)
	}
	fast := socketHTTP(t, port, "GET /hello/Fast HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
	if !strings.Contains(fast, "Hello, Fast!") {
		t.Fatalf("concurrent request: %q", fast)
	}

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/hello/Zip"
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}, Timeout: 5 * time.Second}
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip response: %s %v", response.Status, response.Header)
	}
	compressed, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "Hello, Zip!" {
		t.Fatalf("gzip body = %q", plain)
	}
}

func socketHTTP(t *testing.T, port int, request string) string {
	t.Helper()
	return socketHTTPResponse(t, port, request, false)
}

func socketHTTPWithEOF(t *testing.T, port int, request string) string {
	t.Helper()
	return socketHTTPResponse(t, port, request, true)
}

func socketHTTPResponse(t *testing.T, port int, request string, sendEOF bool) string {
	t.Helper()
	connection := dialEcho(t, port)
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(connection, request); err != nil {
		t.Fatal(err)
	}
	// Only truncated bodies need EOF. A complete or rejected request can
	// already have closed the peer, making CloseWrite race with its shutdown.
	if sendEOF {
		if err := connection.(*net.TCPConn).CloseWrite(); err != nil {
			t.Fatal(err)
		}
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.Proto + " " + response.Status + "\r\n\r\n" + string(body)
}

func TestHTTPServerResponseBodies(t *testing.T) {
	t.Parallel()
	path := filepath.Join("testdata", "http_bodies.fango")
	port := unusedTCPPort(t)
	cmd := exec.Command(cliCompiledBinary(t, path), strconv.Itoa(port))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	dialEcho(t, port).Close()
	invalid := socketHTTP(t, port, "GET /invalid HTTP/1.1\r\nHost: localhost\r\n\r\n")
	if !strings.Contains(invalid, "HTTP/1.1 500") {
		t.Fatalf("invalid response: %q", invalid)
	}
	sized := socketHTTP(t, port, "GET /sized HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
	if !strings.Contains(sized, "HTTP/1.1 200") || !strings.HasSuffix(sized, "\r\n\r\nhello") {
		t.Fatalf("sized response: %q", sized)
	}
}
