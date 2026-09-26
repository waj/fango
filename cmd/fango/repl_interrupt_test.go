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

func TestREPLTaskInterruptAndInputRecovery(t *testing.T) {
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
	write("import Task\n")
	waitFor("loaded Task")
	write("worker : Task.Context -> Int ->{IO} Int\nworker context value =\n    print \"started\"\n    ignore (Task.sleep context 60000)\n    value\n\nwork() = Task.scope (\\scope -> Task.await (Task.spawn scope worker 1))\nwork()\n")
	waitFor("started")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitFor("Err Cancelled")
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
