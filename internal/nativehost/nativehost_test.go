package nativehost

import (
	"bufio"
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

type testHost struct {
	in  *bufio.Reader
	out strings.Builder
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
func (*testHost) Arguments() []string              { return []string{"one", "two"} }
func (*testHost) WorkingDirectory() string         { return "/work" }

func TestWorkerStateScalarsAndHost(t *testing.T) {
	source := Source{Module: "Probe", Content: []byte(`package native
import "math"
var total int64
func Add(n int64) int64 { total += n; return total }
func Inf() float64 { return math.Inf(1) }
func Echo(s string) string { return s }
func Emit(s string) { if err := FangoHost.WriteOutput([]byte(s)); err != nil { panic(err) } }
func Environment() string { return FangoHost.Arguments()[1] + FangoHost.WorkingDirectory() }
func Quit(code int64) { FangoHost.Exit(int(code)) }
`)}
	executor, err := New([]Source{source})
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	host := &testHost{in: bufio.NewReader(strings.NewReader("line\n"))}
	for i, want := range []int64{2, 5} {
		delta := []int64{2, 3}[i]
		got, err := executor.Call(context.Background(), host, "Probe.add", []any{delta})
		if err != nil || got != want {
			t.Fatalf("state call = %v, %v; want %d", got, err, want)
		}
	}
	if got, err := executor.Call(context.Background(), host, "Probe.inf", nil); err != nil || !math.IsInf(got.(float64), 1) {
		t.Fatalf("non-finite float = %v, %v", got, err)
	}
	if got, err := executor.Call(context.Background(), host, "Probe.echo", []any{"λ"}); err != nil || got != "λ" {
		t.Fatalf("unicode = %v, %v", got, err)
	}
	if _, err := executor.Call(context.Background(), host, "Probe.emit", []any{"ok"}); err != nil || host.out.String() != "ok" {
		t.Fatalf("host output = %q, %v", host.out.String(), err)
	}
	if got, err := executor.Call(context.Background(), host, "Probe.environment", nil); err != nil || got != "two/work" {
		t.Fatalf("host environment = %v, %v", got, err)
	}
	_, err = executor.Call(context.Background(), host, "Probe.quit", []any{int64(7)})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 7 {
		t.Fatalf("exit = %v", err)
	}
}
