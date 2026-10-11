package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/nativehost"
	"github.com/waj/fango/internal/natives"
)

func compileWithWorker(t *testing.T, path string) (*core.Prog, *nativehost.Executor) {
	t.Helper()
	var diagnostics bytes.Buffer
	prog, _, _, _, sources, ok := compileFileGraph(path, &diagnostics)
	if !ok {
		t.Fatal(diagnostics.String())
	}
	workerSources := make([]nativehost.Source, len(sources))
	for i, source := range sources {
		workerSources[i] = nativehost.Source{Module: source.Module, Content: source.Content}
	}
	executor, err := nativehost.New(workerSources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { executor.Close() })
	return prog, executor
}

// Avoid graph compilation and worker setup for compiled-only integration runs.
// Interpreter-specific unit tests continue to call compileWithWorker directly.
func compileInterpreterProgram(t *testing.T, path string) (*core.Prog, *nativehost.Executor) {
	t.Helper()
	if !interpreterTestsEnabled() {
		return nil, nil
	}
	return compileWithWorker(t, path)
}

func TestStandardStreams(t *testing.T) {
	path := filepath.Join("testdata", "stdio.fango")
	input := "ab\r\n\xfftail\nlast"
	wantOutput := "[97]\nb:\"\\r\\n\"\n[255]\ntail:\"\\n\"\nTrue\nlast:\"\"\nTrue\nout\nstdout:Other\nstdin:Other\n[\"one\", \"two\"]\n"
	wantError := "err\n\xff\x00A"

	if interpreterTestsEnabled() {
		prog, executor := compileWithWorker(t, path)
		// Retain memoized standard handles in the same environment across sessions.
		env := eval.NewEnv()
		env.DefineProg(prog)
		for range 2 {
			var output, errorOutput bytes.Buffer
			ioctx := eval.NewIOContext(strings.NewReader(input), &output)
			ioctx.ErrorWriter, ioctx.Args, ioctx.Natives = &errorOutput, []string{"one", "two"}, executor
			_, err := eval.ForceIO(context.Background(), prog.Entry, env, ioctx)
			var exit *natives.ExitError
			if !errors.As(err, &exit) || exit.Code != 0 {
				t.Fatalf("interpreter exit = %v", err)
			}
			if output.String() != wantOutput || errorOutput.String() != wantError {
				t.Fatalf("streams = %q / %q", output.String(), errorOutput.String())
			}
		}
	}
	if testing.Short() {
		return
	}
	cmd := exec.Command(cliCompiledBinary(t, path), "one", "two")
	var output, errorOutput bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(input), &output, &errorOutput
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if output.String() != wantOutput || errorOutput.String() != wantError {
		t.Fatalf("compiled streams = %q / %q", output.String(), errorOutput.String())
	}
}

type streamFailureWriter struct{}

func (streamFailureWriter) Write([]byte) (int, error) { return 0, fs.ErrPermission }

type streamFailureReader struct{}

func (streamFailureReader) Read([]byte) (int, error) { return 0, fs.ErrPermission }

func TestStreamFailuresAndConsoleWrappers(t *testing.T) {
	for _, source := range []struct {
		name, body string
		panics     bool
		input      bool
	}{
		{"handle", `case Fail.attempt { IO.write stdout "x" } of
        Ok () -> ()
        Err error -> ignore (Fail.attempt { IO.write stderr (IO.describeError error) })`, false, false},
		{"console", `print "x"`, true, false},
		{"input handle", `case Fail.attempt { IO.readLine stdin } of
        Ok _ -> ()
        Err error -> ignore (Fail.attempt { IO.write stderr (IO.describeError error) })`, false, true},
		{"input console", `ignore (readLine())`, true, true},
	} {
		t.Run(source.name, func(t *testing.T) {
			// Diagnostics use stderr so reporting a failed stdout remains possible.
			path := writeModuleFile(t, t.TempDir(), "Main.fango", "import Fail\nimport Result exposing (Result(..))\nmain() =\n    "+source.body+"\n")
			prog, executor := compileWithWorker(t, path)
			for _, worker := range []bool{false, true} {
				env := eval.NewEnv()
				env.DefineProg(prog)
				var errorOutput bytes.Buffer
				var input io.Reader = strings.NewReader("")
				var output io.Writer = streamFailureWriter{}
				endpoint := "stdout"
				if source.input {
					input, output, endpoint = streamFailureReader{}, io.Discard, "stdin"
				}
				ioctx := eval.NewIOContext(input, output)
				ioctx.ErrorWriter = &errorOutput
				if worker {
					ioctx.Natives = executor
				}
				// A Console failure panics in the program; the interpreter
				// reports that panic as a traced runtime error.
				_, err := eval.ForceIO(context.Background(), prog.Entry, env, ioctx)
				if source.panics {
					if err == nil || !strings.Contains(err.Error(), endpoint+": permission denied") || !strings.Contains(err.Error(), "Fango trace:") {
						t.Fatalf("error = %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if !source.panics && errorOutput.String() != endpoint+": permission denied" {
					t.Fatalf("diagnostic = %q", errorOutput.String())
				}
			}
		})
	}
}
