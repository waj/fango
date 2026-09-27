package nativehost

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"
)

type testHost struct {
	in  *bufio.Reader
	out strings.Builder
}

func TestBundledSourcesUseCanonicalNestedModuleNames(t *testing.T) {
	sources, err := BundledSources()
	if err != nil {
		t.Fatal(err)
	}
	foundRef, foundAsync := false, false
	for _, source := range sources {
		switch source.Module {
		case "Runtime.Ref":
			foundRef = true
		case "Async":
			foundAsync = true
		}
	}
	if !foundRef || !foundAsync {
		t.Fatalf("nested bundled sidecars missing: Ref=%v Async=%v", foundRef, foundAsync)
	}
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
func Boom() { panic("broken") }
func Emit(s string) { if err := FangoHost.WriteOutput([]byte(s)); err != nil { panic(err) } }
func Environment() string { return FangoHost.Arguments()[1] + FangoHost.WorkingDirectory() }
func Quit(code int64) { FangoHost.Exit(int(code)) }
func Blob(data []byte) []byte { return append(append([]byte{}, data...), 0) }
func Nothing() []byte { return nil }
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
	func() {
		defer func() {
			if got := fmt.Sprint(recover()); got != "native Probe.boom panicked: broken" {
				t.Fatalf("panic = %q", got)
			}
		}()
		_, _ = executor.Call(context.Background(), host, "Probe.boom", nil)
	}()
	if got, err := executor.Call(context.Background(), host, "Probe.echo", []any{"after"}); err != nil || got != "after" {
		t.Fatalf("call after panic = %v, %v", got, err)
	}
	if _, err := executor.Call(context.Background(), host, "Probe.emit", []any{"ok"}); err != nil || host.out.String() != "ok" {
		t.Fatalf("host output = %q, %v", host.out.String(), err)
	}
	if got, err := executor.Call(context.Background(), host, "Probe.environment", nil); err != nil || got != "two/work" {
		t.Fatalf("host environment = %v, %v", got, err)
	}
	// Bytes crosses as a plain []byte, unvalidated: it carries what no String
	// could. gob does not distinguish nil from empty, so an empty answer comes
	// back nil, which is what Bytes.empty already is.
	if got, err := executor.Call(context.Background(), host, "Probe.blob", []any{[]byte{0xff, 0xfe}}); err != nil || string(got.([]byte)) != "\xff\xfe\x00" {
		t.Fatalf("bytes round trip = %q, %v", got, err)
	}
	if got, err := executor.Call(context.Background(), host, "Probe.blob", []any{[]byte{}}); err != nil || len(got.([]byte)) != 1 {
		t.Fatalf("empty bytes argument = %q, %v", got, err)
	}
	if got, err := executor.Call(context.Background(), host, "Probe.nothing", nil); err != nil || len(got.([]byte)) != 0 {
		t.Fatalf("empty bytes result = %q, %v", got, err)
	}

	_, err = executor.Call(context.Background(), host, "Probe.quit", []any{int64(7)})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 7 {
		t.Fatalf("exit = %v", err)
	}
}

func TestWorkerManifestDeterministicAndComplete(t *testing.T) {
	a := Source{Module: "A", Content: []byte("package native\nfunc One() int64 { return 1 }\n")}
	b := Source{Module: "B", Content: []byte("package native\nfunc Two() int64 { return 2 }\n")}
	first, err := New([]Source{b, a})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New([]Source{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if first.digest != second.digest {
		t.Fatalf("reordered sources changed digest: %s != %s", first.digest, second.digest)
	}
	wanted := map[string]bool{
		"go.mod":                         false,
		"main.go":                        false,
		"native/A/host.go":               false,
		"native/A/native.go":             false,
		"native/B/host.go":               false,
		"native/B/native.go":             false,
		"runtime/nativewire/wire.go":     false,
		"runtime/nativeworker/worker.go": false,
	}
	for _, file := range first.files {
		if _, ok := wanted[file.Path]; ok {
			wanted[file.Path] = true
		}
	}
	for path, found := range wanted {
		if !found {
			t.Errorf("worker manifest missing %s", path)
		}
	}
	changed := append([]workerFile(nil), first.files...)
	changed[0].Data = append(append([]byte(nil), changed[0].Data...), '\n')
	if digestWorkerFiles(changed) == first.digest {
		t.Fatal("support source change did not invalidate digest")
	}
	mainSource, err := first.workerSource()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mainSource), "encoding/gob") ||
		!strings.Contains(string(mainSource), "nativeworker.Run") ||
		!strings.Contains(string(mainSource), "native0.FangoHost = host") ||
		!strings.Contains(string(mainSource), "native1.FangoHost = host") {
		t.Fatalf("generated main contains worker implementation:\n%s", mainSource)
	}
}
