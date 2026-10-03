package nativeworker

import (
	"encoding/gob"
	"errors"
	"io"
	"io/fs"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/waj/fango/runtime/fangort"
	"github.com/waj/fango/runtime/nativewire"
)

func TestInvoke(t *testing.T) {
	functions := map[string]any{
		"add":  func(a, b int64) int64 { return a + b },
		"unit": func() {},
		"boom": func() { panic("broken") },
		"exit": func() { panic(exitSignal{code: 0}) },
		"open": func(path string) (int64, error) {
			if path == "missing" {
				return 0, &fs.PathError{Op: "open", Path: path, Err: syscall.ENOENT}
			}
			return 7, nil
		},
		"close": func(id int64) error {
			if id == 0 {
				return errors.New("closed handle")
			}
			return nil
		},
	}
	// A fallible native's nil error is dropped and its payload travels as the
	// value; a non-nil error travels as a classified Failure, never as a
	// panic or an infrastructure error.
	if got := invoke(functions, "open", []nativewire.Value{{Kind: "string", S: "ok"}}); got.Failure != nil || got.Value.Kind != "int" || got.Value.I != 7 {
		t.Fatalf("open ok = %#v", got)
	}
	if got := invoke(functions, "open", []nativewire.Value{{Kind: "string", S: "missing"}}); got.Failure == nil || got.Failure.Kind != fangort.IOErrorNotFound || got.Failure.Path != "missing" || got.Panic != "" || got.Error != "" {
		t.Fatalf("open missing = %#v", got)
	}
	if got := invoke(functions, "close", []nativewire.Value{{Kind: "int", I: 1}}); got.Failure != nil || got.Value.Kind != "" {
		t.Fatalf("close ok = %#v", got)
	}
	if got := invoke(functions, "close", []nativewire.Value{{Kind: "int", I: 0}}); got.Failure == nil || got.Failure.Kind != fangort.IOErrorOther || got.Failure.Message != "closed handle" {
		t.Fatalf("close stale = %#v", got)
	}
	got := invoke(functions, "add", []nativewire.Value{{Kind: "int", I: 2}, {Kind: "int", I: 3}})
	if got.Panic != "" || got.Value.Kind != "int" || got.Value.I != 5 {
		t.Fatalf("add = %#v", got)
	}
	if got := invoke(functions, "unit", nil); got.Panic != "" || got.Value.Kind != "" {
		t.Fatalf("unit = %#v", got)
	}
	if got := invoke(functions, "boom", nil); got.Panic != "broken" {
		t.Fatalf("panic = %#v", got)
	}
	if got := invoke(functions, "exit", nil); got.Value.Kind != "exit" || got.Code != 0 {
		t.Fatalf("exit = %#v", got)
	}
}

func TestProxy(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	p := &proxy{enc: gob.NewEncoder(client), dec: gob.NewDecoder(client)}
	done := make(chan error, 1)
	go func() {
		dec, enc := gob.NewDecoder(server), gob.NewEncoder(server)
		want := []string{"host_has_input", "host_read_line", "host_read_bytes", "host_write", "host_write_error", "host_args", "host_dir", "host_exit"}
		for _, kind := range want {
			var request nativewire.Message
			if err := dec.Decode(&request); err != nil {
				done <- err
				return
			}
			if request.Kind != kind {
				done <- errors.New("unexpected request " + request.Kind)
				return
			}
			response := nativewire.Message{Kind: "host_reply"}
			switch kind {
			case "host_has_input":
				response.Bool = true
			case "host_read_line":
				response.Data = []byte("line\n")
			case "host_read_bytes":
				if request.Code != 3 {
					done <- errors.New("unexpected byte count")
					return
				}
				response.Data = []byte{255, 0, 65}
			case "host_write_error":
				if string(request.Data) != "err" {
					done <- errors.New("unexpected stderr")
					return
				}
			case "host_write":
				if string(request.Data) != "out" {
					done <- errors.New("unexpected output")
					return
				}
			case "host_args":
				response.Values = []string{"one", "two"}
			case "host_dir":
				response.Data = []byte("/work")
			}
			if err := enc.Encode(response); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	if ok, err := p.HasInput(); !ok || err != nil {
		t.Fatalf("HasInput = %v, %v", ok, err)
	}
	if line, err := p.ReadInputLine(); string(line) != "line\n" || err != nil {
		t.Fatalf("ReadInputLine = %q, %v", line, err)
	}
	if data, err := p.ReadInputBytes(3); string(data) != "\xff\x00A" || err != nil {
		t.Fatalf("ReadInputBytes = %q, %v", data, err)
	}
	if err := p.WriteOutput([]byte("out")); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteError([]byte("err")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Arguments(), ","); got != "one,two" {
		t.Fatalf("Arguments = %q", got)
	}
	if got := p.WorkingDirectory(); got != "/work" {
		t.Fatalf("WorkingDirectory = %q", got)
	}
	func() {
		defer func() {
			exit, ok := recover().(exitSignal)
			if !ok || exit.code != 0 {
				t.Fatalf("Exit panic = %#v", exit)
			}
		}()
		p.Exit(0)
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := responseError(nativewire.Message{Error: io.EOF.Error()}); err != io.EOF {
		t.Fatalf("EOF response = %v", err)
	}
}

func TestConcurrentProxyRequestsKeepRepliesPaired(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	p := &proxy{enc: gob.NewEncoder(client), dec: gob.NewDecoder(client)}
	const count = 64
	done := make(chan error, 1)
	go func() {
		dec, enc := gob.NewDecoder(server), gob.NewEncoder(server)
		for range count {
			var request nativewire.Message
			if err := dec.Decode(&request); err != nil {
				done <- err
				return
			}
			if err := enc.Encode(nativewire.Message{Kind: "host_reply", Error: string(request.Data)}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := strconv.Itoa(i)
			if got := p.request(nativewire.Message{Kind: "host_write", Data: []byte(id)}); got.Error != id {
				t.Errorf("request %s received reply %q", id, got.Error)
			}
		}(i)
	}
	wg.Wait()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Bytes is the one non-scalar the boundary admits. It crosses verbatim and
// unvalidated, empty and nil are the same value on the way back, and a slice
// that is not []byte is still refused rather than reinterpreted.
func TestInvokeBytes(t *testing.T) {
	functions := map[string]any{
		"echo":    func(data []byte) []byte { return append(append([]byte{}, data...), '!') },
		"size":    func(data []byte) int64 { return int64(len(data)) },
		"nothing": func() []byte { return nil },
		"read": func(fail bool) ([]byte, error) {
			if fail {
				return nil, errors.New("closed handle")
			}
			return []byte{0xff, 0x00}, nil
		},
		"codes": func() []int64 { return []int64{1} },
	}
	got := invoke(functions, "echo", []nativewire.Value{{Kind: "bytes", Bytes: []byte{0xff, 0x00}}})
	if got.Panic != "" || got.Value.Kind != "bytes" || string(got.Value.Bytes) != "\xff\x00!" {
		t.Fatalf("echo = %#v", got)
	}
	if got := invoke(functions, "size", []nativewire.Value{{Kind: "bytes"}}); got.Value.Kind != "int" || got.Value.I != 0 {
		t.Fatalf("size of nil = %#v", got)
	}
	if got := invoke(functions, "nothing", nil); got.Value.Kind != "bytes" || len(got.Value.Bytes) != 0 {
		t.Fatalf("nothing = %#v", got)
	}
	if got := invoke(functions, "read", []nativewire.Value{{Kind: "bool", B: false}}); got.Failure != nil || string(got.Value.Bytes) != "\xff\x00" {
		t.Fatalf("read ok = %#v", got)
	}
	if got := invoke(functions, "read", []nativewire.Value{{Kind: "bool", B: true}}); got.Failure == nil || got.Failure.Message != "closed handle" {
		t.Fatalf("read failing = %#v", got)
	}
	if got := invoke(functions, "codes", nil); !strings.Contains(got.Panic, "unsupported native result type") {
		t.Fatalf("a non-byte slice result must be refused, got %#v", got)
	}
}
