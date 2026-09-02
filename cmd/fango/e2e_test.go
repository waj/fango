package main

import (
	"bytes"
	"context"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/waj/fango/internal/eval"
)

// The differential end-to-end suite (DESIGN.md §11): every
// testdata/run/*.fango runs through BOTH backends — the Core interpreter
// in-process and the compiled binary via the real CLI — and stdout is
// diffed byte-exact against the .expected file AND between the backends.
// Programs with a .error file instead assert a compile-error substring.
//
// S0 programs produce no output, so the compiled leg runs under
// FANGO_INTERNAL_PRINT_MAIN=1 and the eval leg shows main's value through
// the same shared fangort formatter.

var buildOnce sync.Once
var fangoBin string

func cliBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fango-e2e")
		if err != nil {
			t.Fatal(err)
		}
		fangoBin = filepath.Join(dir, "fango")
		cmd := exec.Command("go", "build", "-o", fangoBin, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building CLI: %v\n%s", err, out)
		}
	})
	return fangoBin
}

func TestDifferential(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "run", "*.fango"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no testdata/run/*.fango files")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			base := strings.TrimSuffix(path, ".fango")
			if errData, err := os.ReadFile(base + ".error"); err == nil {
				runErrorCase(t, path, strings.TrimSpace(string(errData)))
				return
			}
			expData, err := os.ReadFile(base + ".expected")
			if err != nil {
				t.Fatalf("missing %s.expected (or .error): %v", base, err)
			}
			expected := string(expData)

			// Backend 1: the Core interpreter.
			var stderr bytes.Buffer
			prog, ck, ok := compileFile(path, &stderr)
			if !ok {
				t.Fatalf("compile failed:\n%s", stderr.String())
			}
			env := eval.NewEnv()
			env.DefineProg(prog)
			v, err := eval.Force(context.Background(), "main", env)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			var mainTy = prog.Defs[len(prog.Defs)-1].Type
			for _, d := range prog.Defs {
				if d.Name == "main" {
					mainTy = d.Type
				}
			}
			evalOut := eval.Show(v, mainTy, ck.B) + "\n"
			if evalOut != expected {
				t.Errorf("interpreter output:\n%q\nwant:\n%q", evalOut, expected)
			}

			// Backend 2: the compiled binary, via the real CLI.
			if testing.Short() {
				t.Skip("compiled leg skipped in -short mode")
			}
			cmd := exec.Command(cliBinary(t), "run", path)
			cmd.Env = append(os.Environ(),
				"FANGO_INTERNAL_PRINT_MAIN=1",
				"FANGO_BUILD_DIR="+t.TempDir())
			var stdout, runErr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &runErr
			if err := cmd.Run(); err != nil {
				t.Fatalf("fango run: %v\n%s", err, runErr.String())
			}
			if stdout.String() != expected {
				t.Errorf("compiled output:\n%q\nwant:\n%q", stdout.String(), expected)
			}
			if stdout.String() != evalOut {
				t.Errorf("backends disagree: compiled %q vs interpreted %q", stdout.String(), evalOut)
			}
		})
	}
}

func runErrorCase(t *testing.T, path, wantSubstr string) {
	var stderr bytes.Buffer
	if _, _, ok := compileFile(path, &stderr); ok {
		t.Fatalf("expected a compile error containing %q", wantSubstr)
	}
	if !strings.Contains(stderr.String(), wantSubstr) {
		t.Errorf("diagnostics missing %q:\n%s", wantSubstr, stderr.String())
	}
}

// Determinism: compiling the same file twice yields byte-identical Go, and
// the output is gofmt-idempotent (emitted via go/format.Node).
func TestEmitDeterministicAndFormatted(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "run", "arith0.fango")
	var stderr bytes.Buffer
	a, ok := emitGo(path, &stderr)
	if !ok {
		t.Fatalf("emit failed:\n%s", stderr.String())
	}
	b, _ := emitGo(path, &stderr)
	if !bytes.Equal(a, b) {
		t.Error("two compilations of the same file differ")
	}
	formatted, err := format.Source(a)
	if err != nil {
		t.Fatalf("generated Go does not parse: %v", err)
	}
	if !bytes.Equal(formatted, a) {
		t.Errorf("generated Go is not gofmt-idempotent:\n%s", a)
	}
}
