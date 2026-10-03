package native

import (
	"bufio"
	"context"
	"errors"
	"github.com/waj/fango/runtime/fangort"
	"io"
	"strings"
	"testing"
)

type testHost struct {
	in     *bufio.Reader
	out    strings.Builder
	errout strings.Builder
	args   []string
	dir    string
	exited int
}

func (h *testHost) HasInput() (bool, error) {
	_, err := h.in.Peek(1)
	if err == io.EOF {
		return false, nil
	}
	return err == nil, err
}
func (h *testHost) ReadInputLine() ([]byte, error) { return h.in.ReadBytes('\n') }
func (h *testHost) ReadInputBytes(count int64) ([]byte, error) {
	return fangort.ReadIOBytes(h.in, count)
}
func (h *testHost) WriteError(b []byte) error  { _, err := h.errout.Write(b); return err }
func (h *testHost) WriteOutput(b []byte) error { _, err := h.out.Write(b); return err }
func (h *testHost) Arguments() []string        { return h.args }
func (h *testHost) WorkingDirectory() string {
	if h.dir == "" {
		return "."
	}
	return h.dir
}
func (*testHost) ExecutionContext() context.Context { return context.Background() }
func (h *testHost) Exit(code int)                   { h.exited = code }

func TestIOLineAndOutput(t *testing.T) {
	old := FangoHost
	h := &testHost{in: bufio.NewReader(strings.NewReader("a\xffb\r\nlast"))}
	FangoHost = h
	t.Cleanup(func() { FangoHost = old })

	input := StandardHandle(0)
	output := StandardHandle(1)
	if ok, err := HandleHasInput(input); !ok || err != nil {
		t.Fatal("expected input")
	}
	if raw, err := ReadHandleLine(input); err != nil || raw != "a�b\r\n" || LineText(raw) != "a�b" || LineEnding(raw) != "\r\n" {
		t.Fatalf("unexpected first line %q", raw)
	}
	if raw, err := ReadHandleLine(input); err != nil || raw != "last" {
		t.Fatalf("unexpected final line %q", raw)
	}
	if ok, err := HandleHasInput(input); ok || err != nil {
		t.Fatal("expected EOF")
	}
	if err := WriteHandle(output, "one 二"); err != nil {
		t.Fatal(err)
	}
	if h.out.String() != "one 二" {
		t.Fatalf("output = %q", h.out.String())
	}
}

type failingHost struct{ testHost }

func (*failingHost) HasInput() (bool, error) { return false, errors.New("broken input") }

func TestIOHostErrorIsFallible(t *testing.T) {
	old := FangoHost
	FangoHost = &failingHost{}
	t.Cleanup(func() { FangoHost = old })
	if _, err := HandleHasInput(StandardHandle(0)); err == nil || !strings.Contains(err.Error(), "stdin") || !strings.Contains(err.Error(), "broken input") {
		t.Fatalf("error = %v", err)
	}
}

func TestStandardHandleDirectionsAndLifetime(t *testing.T) {
	old := FangoHost
	h := &testHost{in: bufio.NewReader(strings.NewReader("abcdef\n"))}
	FangoHost = h
	t.Cleanup(func() { FangoHost = old })
	input, output, errorOutput := StandardHandle(0), StandardHandle(1), StandardHandle(2)
	if data, err := ReadHandleBytes(input, 2); err != nil || string(data) != "ab" {
		t.Fatalf("bytes = %q, %v", data, err)
	}
	if line, err := ReadHandleLine(input); err != nil || line != "cdef\n" {
		t.Fatalf("line = %q, %v", line, err)
	}
	if err := WriteHandle(output, "out"); err != nil {
		t.Fatal(err)
	}
	if err := WriteHandleBytes(errorOutput, []byte{255, 0}); err != nil {
		t.Fatal(err)
	}
	if h.out.String() != "out" || h.errout.String() != "\xff\x00" {
		t.Fatal("standard outputs were mixed")
	}
	for _, value := range []any{output, errorOutput} {
		if _, err := ReadHandleLine(value); err == nil || fangort.ClassifyIOError(err).Kind != fangort.IOErrorOther {
			t.Fatalf("read output = %v", err)
		}
	}
	if err := WriteHandle(input, "bad"); err == nil || fangort.ClassifyIOError(err).Path != "stdin" {
		t.Fatalf("write input = %v", err)
	}
	if err := CloseHandle(output); err == nil {
		t.Fatal("closed stdout")
	}
	next := &testHost{in: bufio.NewReader(strings.NewReader("next\n"))}
	FangoHost = next
	if err := WriteHandle(output, "next"); err != nil {
		t.Fatal(err)
	}
	if next.out.String() != "next" || h.out.String() != "out" {
		t.Fatal("standard handle retained its old host")
	}
}
