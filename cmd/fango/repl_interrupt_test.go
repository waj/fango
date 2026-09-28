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

func TestREPLAsyncInterruptAndInputRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliBinary(t), "repl")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	bytes := make(chan byte, 4096)
	go func() {
		var one [1]byte
		for {
			if _, err := stdout.Read(one[:]); err != nil {
				close(bytes)
				return
			}
			bytes <- one[0]
		}
	}()
	var output strings.Builder
	waitFor := func(target string) {
		t.Helper()
		deadline := time.NewTimer(90 * time.Second)
		defer deadline.Stop()
		start := output.Len()
		for {
			if strings.Contains(output.String()[start:], target) {
				return
			}
			select {
			case b, ok := <-bytes:
				if !ok {
					t.Fatalf("REPL exited before %q; output:\n%s", target, output.String())
				}
				output.WriteByte(b)
			case <-deadline.C:
				t.Fatalf("REPL did not emit %q; output:\n%s", target, output.String())
			}
		}
	}
	write := func(source string) {
		t.Helper()
		if _, err := io.WriteString(stdin, source); err != nil {
			t.Fatal(err)
		}
	}
	waitFor("> ")
	write("import Async\n")
	waitFor("loaded Async")
	waitFor("> ")
	write("import Result\n")
	waitFor("> ")
	write("import Runtime.Scope\n")
	waitFor("> ")
	write("count n = if n <= 0 then 0 else count (n - 1)\n")
	waitFor("> ")
	write("worker : Int -> () ->{IO, Async.Async String} Int\nworker value () = Runtime.Scope.bracket { _ -> () } { _ ->\n    ignore (count 10000)\n    print \"child cleaned\"\n} { _ ->\n    print \"started\"\n    Async.sleep 60000\n    value\n}\n\nwork() = Async.run { _ -> Async.await (Async.spawn (worker 1)) }\nwork()\n")
	waitFor("started")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitFor("child cleaned")
	waitFor("Cancelled")
	// A root with no spawned task must also own host cancellation and cleanup.
	write("root : () ->{IO} Async.Outcome (Result.Result String ())\nroot() = Async.run { _ -> Runtime.Scope.bracket { _ -> () } { _ ->\n    ignore (Async.parMap count [10000, 10000])\n    print \"root cleaned\"\n} { _ ->\n    print \"root sleeping\"\n    Async.sleep 60000\n} }\n\nroot()\n")
	waitFor("root sleeping")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitFor("root cleaned")
	waitFor("Cancelled")
	// Pure parallel mapping reports host interruption back to the evaluator.
	write("mapped() =\n    print \"mapping\"\n    ignore (Async.parMap { _ -> count 1000000000 } [1, 2, 3, 4])\n\nmapped()\n")
	waitFor("mapping")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitFor("interrupted")
	write("line() =\n    print \"reading\"\n    readLine()\n\nline()\n")
	waitFor("reading")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitFor("interrupted")
	write("42\n")
	waitFor("42 : Num a => a")
	write(":quit\n")
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		t.Fatal(fmt.Errorf("REPL exited: %w\n%s", err, output.String()))
	}
}
