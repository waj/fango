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
runStrings : (() ->{Async.Async String, IO, Fail String | e} a) ->{IO | e} Async.Outcome (Result String a)
runStrings body = Async.run body
withDatabase body =
    handle body() of
        lookup () -> stop()
withStop body =
    handle body() of
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
	path := writeModuleFile(t, t.TempDir(), "Main.fango", asyncAbortSource)
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
