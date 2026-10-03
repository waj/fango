package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	dialEcho(t, port).Close()

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
