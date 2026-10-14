package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/natives"
)

// A compiled program that panics reports the Fango stack on stderr and exits
// with Go's panic status. These are compiled-only checks: the differential
// harness runs the interpreter in-process, where a panic would end the test
// binary, and compares standard output, which a crash report never touches.
func TestCompiledCrashReportsFangoTrace(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled backend")
	}
	t.Parallel()
	cases := []struct {
		name, source string
		trace        []string
	}{
		{
			name: "worker",
			source: `module Main exposing (main)
import Basics
import Console
import IO exposing (IO)

divide : Int -> Int -> Int
divide a b =
    Basics.quotientBy b a

report : Int ->{IO} ()
report n =
    Console.write (show (divide n 0))
    Console.write (show (divide n 1))

main() =
    report 7
`,
			trace: []string{"Main.report (Main.fango:12)", "Main.main (Main.fango:16)"},
		},
		{
			name: "lambda and local function",
			source: `module Main exposing (main)
import Basics
import Console
import IO exposing (IO)
import List

main() =
    scale : Int ->{IO} Int
    scale n =
        Console.write ""
        Basics.quotientBy (n - 2) 100
    List.each { n -> Console.write (show (scale n)) } [3, 2]
`,
			trace: []string{"Main.main.scale (Main.fango:11)", "Main.main (lambda) (Main.fango:12)", "List.each (<stdlib>/List.fango:", "Main.main (Main.fango:12)"},
		},
		{
			name: "instance method",
			source: `module Main exposing (main)
import Basics
import Console
import IO exposing (IO)

type Ratio = Ratio Int Int

class Describe a
    describe : a ->{IO} String

instance Describe Ratio
    describe (Ratio a b) =
        Console.write ""
        show (Basics.quotientBy b a)

main() =
    Console.write (describe (Ratio 1 0))
`,
			trace: []string{"Main.Describe (Ratio).describe (Main.fango:14)", "Main.main (Main.fango:17)"},
		},
		{
			name: "spawned task",
			source: `module Main exposing (main)
import Async
import Basics
import Console
import IO exposing (IO)

crash : Int ->{IO} Int
crash n =
    Console.write ""
    Basics.quotientBy n 1

main() =
    Async.run {
        task = Async.spawn { crash 0 }
        Console.write (show (Async.await task))
    }
`,
			trace: []string{"Main.crash (Main.fango:10)", "spawned from:", "Main.main (lambda) (Main.fango:14)"},
		},
	}
	panicLine := regexp.MustCompile(`^panic: runtime error: integer divide by zero\n\nFango trace:\n`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "Main.fango")
			if err := os.WriteFile(path, []byte(tc.source), 0o644); err != nil {
				t.Fatal(err)
			}
			binary := cliCompiledBinary(t, path)
			stdout, stderr, status := runCrashing(t, binary, nil)
			if status != 2 {
				t.Fatalf("exit %d, stderr:\n%s", status, stderr)
			}
			if !panicLine.MatchString(stderr) {
				t.Fatalf("stderr does not start with the panic report:\n%s", stderr)
			}
			rest := stderr
			for _, frame := range tc.trace {
				i := strings.Index(rest, frame)
				if i < 0 {
					t.Fatalf("frame %q missing or out of order in:\n%s\nstdout:\n%s", frame, stderr, stdout)
				}
				rest = rest[i+len(frame):]
			}
			if strings.Contains(stderr, "goroutine ") || strings.Contains(stderr, "/.fango/") {
				t.Fatalf("Go traceback details leaked into the report:\n%s", stderr)
			}
			_, goTrace, status := runCrashing(t, binary, []string{"FANGO_TRACEBACK=go"})
			if status != 2 || !strings.Contains(goTrace, "[running]:") || strings.Contains(goTrace, "Fango trace:") {
				t.Fatalf("FANGO_TRACEBACK=go did not keep Go's traceback (exit %d):\n%s", status, goTrace)
			}
		})
	}
}

func runCrashing(t *testing.T, binary string, env []string) (stdout, stderr string, status int) {
	t.Helper()
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		status = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String(), status
}

func TestAdvancingClockRangeFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled backend")
	}
	t.Parallel()
	for _, tc := range []struct {
		name, start, step string
	}{
		{"upper", "9999-12-30T23:59:59.999999999Z", "Duration.nanoseconds 1"},
		{"lower", "0001-01-02T00:00:00Z", "Duration.nanoseconds (-1)"},
		{"huge positive", "2026-01-01T00:00:00Z", "Duration.seconds 9223372036854775807"},
		{"huge negative", "2026-01-01T00:00:00Z", "Duration.seconds (-9223372036854775807 - 1)"},
		{"offset", "9999-12-30T23:59:59.999999999Z", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := fmt.Sprintf(`Clock.runStepped start (%s) {
            Console.write (DT.toIso Clock.now())
            DT.toIso Clock.now()
        }`, tc.step)
			wantOut, runner := tc.start, "Clock.runStepped"
			if tc.step == "" {
				action = `Clock.runOffset start {
            Async.runOutcome { Async.sleep 1; DT.toIso Clock.now() }
        }`
				wantOut, runner = "", "Clock.runOffset"
			}
			source := fmt.Sprintf(`module Main exposing (main)
import Async
import Console
import Fail
import Time.Clock as Clock
import Time.DateTime as DT
import Time.Duration as Duration

main() =
    result = Fail.attempt {
        start = DT.parse %q
        %s
    }
    Console.write (show result)
`, tc.start, action)
			path := filepath.Join(t.TempDir(), "Main.fango")
			if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, status := runCrashing(t, cliCompiledBinary(t, path), nil)
			if status != 2 || stdout != wantOut || !strings.Contains(stderr, runner+" reading outside supported instant range") {
				t.Fatalf("exit %d, stdout %q, stderr:\n%s", status, stdout, stderr)
			}
		})
	}
}

// Trace.capture sees the same frames a crash report prints, starting at its
// caller.
func TestCompiledTraceCapture(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled backend")
	}
	t.Parallel()
	source := `module Main exposing (main)
import Console
import IO exposing (IO)
import List
import Trace

log : String ->{IO} ()
log message =
    Console.write (message ++ "\n" ++ Trace.render (Trace.capture ()))

step : Int ->{IO} ()
step n =
    if n == 0 then log "bottom" else step (n - 1)

main() =
    List.each { n -> step n } [2]
    log "top"
`
	path := filepath.Join(t.TempDir(), "Main.fango")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status := runCrashing(t, cliCompiledBinary(t, path), nil)
	if status != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr:\n%s", status, stderr)
	}
	want := "bottom\n" +
		"  at Main.log (Main.fango:9)\n" +
		"  at Main.step (Main.fango:13)\n" +
		"  at Main.main (lambda) (Main.fango:16)\n" +
		"  at List.each (<stdlib>/List.fango:"
	if !strings.HasPrefix(stdout, want) || !strings.Contains(stdout, "\ntop\n  at Main.log (Main.fango:9)\n  at Main.main (Main.fango:17)\n") {
		t.Fatalf("unexpected trace output:\n%s", stdout)
	}
}

// A status outside 0 through 255 faults where it is built, like a division by
// zero, rather than wrapping around to success.
func TestOutOfRangeStatusFaults(t *testing.T) {
	t.Parallel()
	source := `module Main exposing (main)
import Process

main() =
    print "before"
    Process.status 256
`
	path := filepath.Join(t.TempDir(), "Main.fango")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	const message = "exit status 256 is out of range 0 through 255"
	if interpreterTestsEnabled() {
		var diagnostics bytes.Buffer
		prog, _, ok := compileFile(path, &diagnostics)
		if !ok {
			t.Fatal(diagnostics.String())
		}
		var out bytes.Buffer
		err := withInterpreter(func() error {
			env := eval.NewEnv()
			env.DefineProg(prog)
			_, err := eval.ForceIO(context.Background(), prog.Entry, env, eval.NewIOContext(strings.NewReader(""), &out))
			return err
		})
		var exit *natives.ExitError
		if err == nil || errors.As(err, &exit) || !strings.Contains(err.Error(), message) || out.String() != "before\n" {
			t.Fatalf("interpreter: err %v, output %q", err, out.String())
		}
	}
	if testing.Short() {
		return
	}
	stdout, stderr, status := runCrashing(t, cliCompiledBinary(t, path), nil)
	if status != 2 || stdout != "before\n" || !strings.Contains(stderr, message) || !strings.Contains(stderr, "Main.main (Main.fango:6)") {
		t.Fatalf("exit %d, stdout %q, stderr:\n%s", status, stdout, stderr)
	}
}
