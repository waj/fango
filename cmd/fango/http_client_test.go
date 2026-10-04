package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestHTTPClientLoopback runs the client over real sockets against a Go
// server that reports how each request body arrived.
func TestHTTPClientLoopback(t *testing.T) {
	t.Parallel()
	upload := bytes.Repeat([]byte("0123456789abcdef"), 1<<18)
	file := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(file, upload, 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/text", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello")
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			http.Error(w, "missing Accept", http.StatusBadRequest)
			return
		}
		io.WriteString(w, "[1,2,3]")
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})
	mux.HandleFunc("/count", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var numbers []int
		if err := json.Unmarshal(body, &numbers); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, "%s %d numbers, last %d", framing(r), len(numbers), numbers[len(numbers)-1])
	})
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, "%s %d bytes, same %v", framing(r), len(body), bytes.Equal(body, upload))
	})
	mux.HandleFunc("/gzip", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") != "gzip" || r.Header.Get("Accept-Encoding") != "gzip" {
			http.Error(w, "expected gzip both ways", http.StatusBadRequest)
			return
		}
		plain, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(plain)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		out := gzip.NewWriter(w)
		fmt.Fprintf(out, "server read %q", body)
		out.Close()
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
		io.WriteString(w, "late")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	binary := cliCompiledBinary(t, filepath.Join("testdata", "http_client.fango"))
	out, err := exec.Command(binary, server.URL, file).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := strings.Join([]string{
		`Ok "hello"`,
		`Ok [1, 2, 3]`,
		"status 404 gone\n",
		"chunked 200000 numbers, last 199999",
		fmt.Sprintf("length %d bytes, same true", len(upload)),
		`server read "round trip"`,
		"Err Timeout",
		"Ok (Ok ())",
		"",
	}, "\n")
	if string(out) != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
}

func framing(r *http.Request) string {
	if r.ContentLength >= 0 {
		return "length"
	}
	return strings.Join(r.TransferEncoding, ",")
}

// TestHTTPProxyStreamsRequestBody runs an Http.Server handler that forwards
// its request body to an upstream server through the client as it arrives.
func TestHTTPProxyStreamsRequestBody(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, "%s %s %d bytes", r.URL.Path, framing(r), n)
	}))
	defer upstream.Close()
	port := unusedTCPPort(t)
	cmd := exec.Command(cliCompiledBinary(t, filepath.Join("testdata", "http_proxy.fango")), fmt.Sprint(port), upstream.URL)
	startTCPServer(t, cmd, port).Close()

	body := bytes.Repeat([]byte("x"), 8<<20)
	response, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/upload", port), "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("/upload chunked %d bytes", len(body)); response.StatusCode != 200 || string(got) != want {
		t.Fatalf("%s: %q, want %q", response.Status, got, want)
	}
}

// TestHTTPClientTLS checks certificate verification against a loopback TLS
// server whose CA the client trusts only through Config.caFile.
func TestHTTPClientTLS(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "secure hello")
	}))
	defer server.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, certificate, 0o644); err != nil {
		t.Fatal(err)
	}
	// The test certificate names 127.0.0.1 and example.com, not localhost.
	otherHost := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	binary := cliCompiledBinary(t, filepath.Join("testdata", "http_tls.fango"))
	out, err := exec.Command(binary, server.URL, caFile, otherHost).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "secure hello\nrejected\nrejected\n"; string(out) != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
}

// TestHTTPClientKeepAlive counts the connections a sequence of requests uses:
// a connection is reused until the server closes it, says Connection: close,
// or a response body is left with more than the client drains.
func TestHTTPClientKeepAlive(t *testing.T) {
	t.Parallel()
	var connections atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "a") })
	mux.HandleFunc("/kill", func(w http.ResponseWriter, r *http.Request) {
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		buffered.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1\r\n\r\nk")
		buffered.Flush()
		conn.Close()
	})
	mux.HandleFunc("/closing", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		io.WriteString(w, "c")
	})
	mux.HandleFunc("/large", func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("L"), 1<<20))
	})
	mux.HandleFunc("/small", func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("s"), 1000))
	})
	server := httptest.NewUnstartedServer(mux)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	out, err := exec.Command(cliCompiledBinary(t, filepath.Join("testdata", "http_keepalive.fango")), server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := "/a 200 a\n/a 200 a\n/a 200 a\n/kill 200 k\n/a 200 a\n/closing 200 c\n/a 200 a\n/large 200 L\n/a 200 a\n/small 200 s\n/a 200 a\nOk ()\n"
	if string(out) != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
	// One connection until /kill, one until /closing, one until /large, and
	// one for the rest, /small being drained.
	if got := connections.Load(); got != 4 {
		t.Fatalf("%d connections, want 4", got)
	}
}
