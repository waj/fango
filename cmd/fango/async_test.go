package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/nativehost"
)

const asyncAbortSource = `import Async
import Fail exposing (Fail)
import Result exposing (Result(..))
effect Stop
    abort stop : () -> value
effect Database
    lookup : () -> Int
runStrings : (() ->{Async.Async, IO | e} a) ->{IO | e} Async.Outcome a
runStrings body = Async.runOutcome body
withDatabase body =
    handle body() on
        lookup () -> stop()
withStop body =
    handle body() on
        stop () -> ()
job : () ->{IO, Database} Int
job() =
    print "CHILD_STARTED"
    lookup()
main() = withStop ({ _ -> withDatabase ({ _ ->
    ignore (runStrings ({ _ ->
        task = Async.spawn job
        Async.await task })) }) })
`

func TestAsyncUnsupportedInheritedAbort(t *testing.T) {
	testAsyncRejectedBeforeUserCode(t, asyncAbortSource)
}

func testAsyncRejectedBeforeUserCode(t *testing.T, source string) {
	t.Helper()
	path := writeModuleFile(t, t.TempDir(), "Main.fango", source)
	for _, backend := range []string{"interpreter", "compiled"} {
		t.Run(backend, func(t *testing.T) {
			var cmd *exec.Cmd
			if backend == "compiled" {
				if testing.Short() {
					t.Skip("compiled backend")
				}
				cmd = exec.Command(cliCompiledBinary(t, path))
			} else {
				cmd = exec.Command(os.Args[0], "-test.run=^TestAsyncAbortInterpreterProcess$")
				cmd.Env = append(os.Environ(), "FANGO_ASYNC_ABORT_SOURCE="+path)
			}
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "cannot inherit abort handler") || strings.Contains(string(output), "CHILD_STARTED\n") {
				t.Fatalf("exit %v, output:\n%s", err, output)
			}
		})
	}
}

func TestAsyncAbortInterpreterProcess(t *testing.T) {
	path := os.Getenv("FANGO_ASYNC_ABORT_SOURCE")
	if path == "" {
		t.Skip("subprocess helper")
	}
	var diagnostics bytes.Buffer
	program, _, _, _, sources, ok := compileFileGraph(path, &diagnostics)
	if !ok {
		t.Fatal(diagnostics.String())
	}
	env := eval.NewEnv()
	env.DefineProg(program)
	workerSources := make([]nativehost.Source, len(sources))
	for i, source := range sources {
		workerSources[i] = nativehost.Source{Module: source.Module, Content: source.Content}
	}
	executor, err := nativehost.New(workerSources)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	ioctx := eval.NewIOContext(strings.NewReader(""), os.Stdout)
	ioctx.Natives = executor
	_, err = eval.ForceIO(context.Background(), program.Entry, env, ioctx)
	fmt.Fprintln(os.Stderr, err)
	if err != nil {
		os.Exit(1)
	}
}

// A polymorphic launch cannot prove its row contains only resumptive effects.
// Prepared evidence must reject the abort before even the first user statement.
func TestAsyncGenericAbortBoundary(t *testing.T) {
	source := `import Async
import Fail exposing (Fail)
effect Stop
    abort stop : () -> value
submit : (() ->{e} a) ->{Async.Async | e} Async.Task a
submit body = Async.spawn body
job : () ->{IO, Stop} Int
job() =
    print "CHILD_STARTED"
    stop()
main() =
    handle ignore (Async.runOutcome { submit job }) on
        stop () -> ()
`
	testAsyncRejectedBeforeUserCode(t, source)
}

func TestAsyncKnownAbortBoundary(t *testing.T) {
	for _, callback := range []string{"{ stop() }", "job"} {
		source := `import Async
effect Stop
    abort stop : () -> value
job : () ->{Stop} Int
job() = stop()
main() =
    handle ignore (Async.runOutcome { Async.spawn ` + callback + ` }) on
        stop () -> ()
`
		path := writeModuleFile(t, t.TempDir(), "Main.fango", source)
		var diagnostics bytes.Buffer
		if _, _, ok := compileFile(path, &diagnostics); ok || !strings.Contains(diagnostics.String(), "ASYNC BOUNDARY") {
			t.Fatalf("callback %s: %s", callback, diagnostics.String())
		}
	}
}

func TestAsyncGenericFailBoundary(t *testing.T) {
	testAsyncRejectedBeforeUserCode(t, `import Async
import Fail exposing (Fail)
submit : (() ->{e} a) ->{Async.Async | e} Async.Task a
submit body = Async.spawn body
job : () ->{IO, Fail String} Int
job() =
    print "CHILD_STARTED"
    Fail.fail "unsupported"
main() = ignore (Fail.attempt { Async.runOutcome { submit job } })
`)
}

// A same-typed local Fail does not retarget an inherited handler's lexical
// abort dependency, including dependencies reached through another handler.
func TestAsyncLocalCaptureDoesNotRetargetInheritedAbort(t *testing.T) {
	testAsyncRejectedBeforeUserCode(t, `import Async
import Fail exposing (Fail)
import Result exposing (Result(..))
effect Database
    lookup : () -> Int
effect Audit
    check : () -> Int
job : () ->{IO, Database} Result String Int
job() =
    print "CHILD_STARTED"
    Fail.attempt { if lookup() > 0 then Fail.fail "local" else 42 }
main() = ignore (Fail.attempt {
        handle
            handle Async.runOutcome { Async.spawn job } on
                lookup () ->
                    value = check()
                    resume value
        on
            check () -> Fail.fail "outer"
})
`)
}
