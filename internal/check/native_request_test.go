package check

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
)

const requestFixture = `module Requests exposing (Device, open, close, submit)
import Runtime.NativeRequest
import Runtime.Native
{-# resource #-}
type Device = Device Runtime.Native.Any
open : () ->{IO} Device
open = native
close : Device ->{IO} ()
close = native
submit : Runtime.NativeRequest.Registration -> Device ->{IO} ()
submit = native
`
const requestSidecar = `package native
func Open() any { return nil }
func Close(value any) {}
func Submit(token, device any) { r := token.(*FangoRequest); if r.Begin(nil) { r.Complete(); r.Done() } }
`

func TestNativeRequestOwnershipContracts(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"enclosing resource", `main() = Runtime.Scope.bracket Requests.open Requests.close (\device -> Runtime.NativeRequest.scope 1 (\host ->
    ignored = Runtime.NativeRequest.register host (\token -> Requests.submit token device) (\_ -> ())
    ()))`, ""},
		{"shorter resource", `main() = Runtime.NativeRequest.scope 1 (\host -> Runtime.Scope.bracket Requests.open Requests.close (\device ->
    ignored = Runtime.NativeRequest.register host (\token -> Requests.submit token device) (\_ -> ())
    ()))`, "ESCAPE"},
		{"host escapes", `bad() = Runtime.NativeRequest.scope 1 (\host -> host)
main() = ()`, "ESCAPE"},
		{"callback escapes", `bad() = Runtime.NativeRequest.scope 1 (\host -> Runtime.NativeRequest.register host (\_ -> ()) (\_ -> 42))
main() = ()`, "ESCAPE"},
		{"token escapes through data", `type Saved = Saved Runtime.NativeRequest.Registration
effect Save
    save : Saved -> ()
bad() = Runtime.NativeRequest.scope 1 (\host ->
    ignored = Runtime.NativeRequest.register host (\token -> save (Saved token)) (\_ -> ())
    ())
main() = handle bad() with saved = Nothing of
    save value -> resume () with Just value`, "ESCAPE"},
		{"callback suspends", `producer : (Int ->{Runtime.Coroutine.Suspension} ()) -> () ->{Runtime.Coroutine.Suspension} ()
producer pause () =
    ignored = Runtime.NativeRequest.immediate (\_ -> pause 1)
    ()
main() = Runtime.Coroutine.with producer (\cursor ->
    ignored = Runtime.Coroutine.advance cursor ()
    ())`, "SUSPENDING"},
		{"callback cleanup suspends", `producer : (Int ->{Runtime.Coroutine.Suspension} ()) -> () ->{Runtime.Coroutine.Suspension} ()
producer pause () =
    ignored = Runtime.NativeRequest.immediate (\_ -> Runtime.Scope.finally (\_ -> ()) (\_ -> pause 1))
    ()
main() = Runtime.Coroutine.with producer (\cursor ->
    ignored = Runtime.Coroutine.advance cursor ()
    ())`, "SUSPENDING"},
		{"foreign work driver", `main() = Runtime.NativeRequest.scope 1 (\host -> Runtime.Coroutine.scope (\scope ->
    child = Runtime.Work.register (Runtime.Coroutine.facet scope) (\_ () -> Runtime.NativeRequest.counts host)
    ()))`, "WORK CAPABILITY TRANSFER"},
		{"row mismatch", `bad : Runtime.NativeRequest.Callback Int {IO, Fail.Fail String} -> Runtime.NativeRequest.Callback Int {IO}
bad callback = callback
main() = ()`, "EFFECT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, source := range map[string]string{"Requests.fango": requestFixture, "Requests.native.go": requestSidecar, "Main.fango": "import Requests\nimport Runtime.NativeRequest\nimport Runtime.Scope\nimport Runtime.Coroutine\nimport Runtime.Work\nimport Fail\n" + tc.source + "\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cache := newMemoryObjectCache()
			for range 2 {
				_, ds, err := (&Session{Cache: cache}).Compile(filepath.Join(dir, "Main.fango"))
				if err != nil {
					t.Fatal(err)
				}
				var messages []string
				for _, d := range ds {
					messages = append(messages, d.Title+" "+d.Body)
				}
				got := strings.Join(messages, "\n")
				if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
					t.Fatalf("got %s; want %s", got, tc.want)
				}
			}
		})
	}
}

func TestNativeRequestCoreRetentionProof(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "run", "native_requests.fango")
	result, _ := compileEvents(t, path, newMemoryObjectCache())
	changed := false
	for _, n := range result.Program.Natives {
		if n.RetainsRequest {
			n.RetainsRequest = false
			changed = true
		}
	}
	if !changed {
		t.Fatal("missing native retention contract")
	}
	if got := fmt.Sprint(core.LintMachineInput(result.Program, result.Checker.B)); !strings.Contains(got, "request retention contract") {
		t.Fatal(got)
	}
}

func TestNativeRequestModuleObject(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "native_requests", "Main.fango")
	cache := newMemoryObjectCache()
	compileEvents(t, path, cache)
	_, events := compileEvents(t, path, cache)
	if events["checked-cache-hit"]["Device"] != 1 || events["checked-cache-hit"]["Runtime.NativeRequest"] != 1 {
		t.Fatalf("missing request cache contracts: %v", events)
	}
}

func TestNativeRequestDeclarations(t *testing.T) {
	for _, tc := range []struct{ name, declaration, sidecar string }{
		{"wrong position", "submit : Int -> Runtime.NativeRequest.Registration ->{IO} ()", "func Submit(n int64, token any) {}"},
		{"token allocation", "submit : () ->{IO} Runtime.NativeRequest.Registration", "func Submit() any { return nil }"},
		{"result", "submit : Runtime.NativeRequest.Registration ->{IO} Int", "func Submit(token any) int64 { return 0 }"},
		{"host boundary", "submit : Runtime.NativeRequest.Host ->{IO} ()", "func Submit(host any) {}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "Main.fango")
			if err := os.WriteFile(path, []byte("import Runtime.NativeRequest\n"+tc.declaration+"\nsubmit = native\nmain() = ()\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "Main.native.go"), []byte("package native\n"+tc.sidecar+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, ds, err := (&Session{}).Compile(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(ds) == 0 {
				t.Fatal("invalid request native accepted")
			}
			if !strings.HasPrefix(ds[0].Title, "NATIVE") {
				t.Fatal(ds)
			}
		})
	}
}
