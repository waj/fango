package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// replSession drives a `fango repl` process through its pipes.
type replSession struct {
	t      *testing.T
	ctx    context.Context
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	bytes  chan byte
	output strings.Builder
}

func startREPL(t *testing.T, ctx context.Context) *replSession {
	t.Helper()
	r := &replSession{t: t, ctx: ctx, cmd: exec.CommandContext(ctx, cliBinary(t), "repl"), bytes: make(chan byte, 4096)}
	stdin, err := r.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	r.stdin = stdin
	stdout, err := r.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	r.cmd.Stderr = os.Stderr
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = r.cmd.Process.Kill()
		_ = r.cmd.Wait()
	})
	go func() {
		var one [1]byte
		for {
			if _, err := stdout.Read(one[:]); err != nil {
				close(r.bytes)
				return
			}
			r.bytes <- one[0]
		}
	}()
	return r
}

func (r *replSession) waitFor(target string) {
	r.t.Helper()
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	start := r.output.Len()
	for {
		if strings.Contains(r.output.String()[start:], target) {
			return
		}
		select {
		case b, ok := <-r.bytes:
			if !ok {
				r.t.Fatalf("REPL exited before %q; output:\n%s", target, r.output.String())
			}
			r.output.WriteByte(b)
		case <-deadline.C:
			r.t.Fatalf("REPL did not emit %q; output:\n%s", target, r.output.String())
		}
	}
}

func (r *replSession) write(source string) {
	r.t.Helper()
	if _, err := io.WriteString(r.stdin, source); err != nil {
		r.t.Fatal(err)
	}
}

func (r *replSession) interrupt() {
	r.t.Helper()
	if err := r.cmd.Process.Signal(os.Interrupt); err != nil {
		r.t.Fatal(err)
	}
}

func (r *replSession) quit() {
	r.t.Helper()
	r.write(":quit\n")
	_ = r.stdin.Close()
	if err := r.cmd.Wait(); err != nil && r.ctx.Err() == nil {
		r.t.Fatal(fmt.Errorf("REPL exited: %w\n%s", err, r.output.String()))
	}
}

func TestREPLAsyncInterruptAndInputRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := startREPL(t, ctx)
	r.waitFor("> ")
	r.write("import Async\n")
	r.waitFor("loaded Async")
	r.waitFor("> ")
	r.write("import Result\n")
	r.waitFor("> ")
	r.write("import Runtime.Scope\n")
	r.waitFor("> ")
	r.write("count n = if n <= 0 then 0 else count (n - 1)\n")
	r.waitFor("> ")
	r.write("worker : Int -> () ->{IO, Async.Async String} Int\nworker value () = Runtime.Scope.bracket { _ -> () } { _ ->\n    ignore (count 10000)\n    print \"child cleaned\"\n} { _ ->\n    print \"started\"\n    Async.sleep 60000\n    value\n}\n\nwork() = Async.run { _ -> Async.await (Async.spawn (worker 1)) }\nwork()\n")
	r.waitFor("started")
	r.interrupt()
	r.waitFor("child cleaned")
	r.waitFor("Cancelled")
	// A root with no spawned task must also own host cancellation and cleanup.
	r.write("root : () ->{IO} Async.Outcome (Result.Result String ())\nroot() = Async.run { _ -> Runtime.Scope.bracket { _ -> () } { _ ->\n    ignore (Async.parMap count [10000, 10000])\n    print \"root cleaned\"\n} { _ ->\n    print \"root sleeping\"\n    Async.sleep 60000\n} }\n\nroot()\n")
	r.waitFor("root sleeping")
	r.interrupt()
	r.waitFor("root cleaned")
	r.waitFor("Cancelled")
	// Pure parallel mapping reports host interruption back to the evaluator.
	r.write("mapped() =\n    print \"mapping\"\n    ignore (Async.parMap { _ -> count 1000000000 } [1, 2, 3, 4])\n\nmapped()\n")
	r.waitFor("mapping")
	r.interrupt()
	r.waitFor("interrupted")
	r.write("line() =\n    print \"reading\"\n    readLine()\n\nline()\n")
	r.waitFor("reading")
	r.interrupt()
	r.waitFor("interrupted")
	r.write("42\n")
	r.waitFor("42 : Num a => a")
	r.quit()
}

// Ctrl-C stops the input running inside a handler level and leaves the level
// and its state in place.
func TestREPLInterruptInsideLevel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := startREPL(t, ctx)
	r.waitFor("> ")
	r.write("import State\n")
	r.waitFor("loaded State")
	r.waitFor("> ")
	r.write("count n = if n <= 0 then 0 else count (n - 1)\n")
	r.waitFor("> ")
	r.write("spin() =\n    print \"counting\"\n    count 1000000000\n\n")
	r.waitFor("> ")
	r.write("with State.run 41\n")
	r.waitFor("1> ")
	r.write("spin()\n")
	r.waitFor("counting")
	r.interrupt()
	r.waitFor("interrupted")
	r.write("State.get() + 1\n")
	r.waitFor("42 : Int")
	r.quit()
}
