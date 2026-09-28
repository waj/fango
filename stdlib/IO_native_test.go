package native

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type testHost struct {
	in     *bufio.Reader
	out    strings.Builder
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
func (h *testHost) WriteOutput(b []byte) error     { _, err := h.out.Write(b); return err }
func (h *testHost) Arguments() []string            { return h.args }
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

	if !HasInput() {
		t.Fatal("expected input")
	}
	if raw := ReadRawLine(); raw != "a�b\r\n" || LineText(raw) != "a�b" || LineEnding(raw) != "\r\n" {
		t.Fatalf("unexpected first line %q", raw)
	}
	if raw := ReadRawLine(); raw != "last" {
		t.Fatalf("unexpected final line %q", raw)
	}
	if HasInput() {
		t.Fatal("expected EOF")
	}
	Write("one 二")
	if h.out.String() != "one 二" {
		t.Fatalf("output = %q", h.out.String())
	}
}

type failingHost struct{ testHost }

func (*failingHost) HasInput() (bool, error) { return false, errors.New("broken input") }

func TestIOHostErrorPanics(t *testing.T) {
	old := FangoHost
	FangoHost = &failingHost{}
	t.Cleanup(func() { FangoHost = old })
	defer func() {
		if recover() == nil {
			t.Fatal("host error did not panic")
		}
	}()
	HasInput()
}
