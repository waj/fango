package main

import (
	"bytes"
	"context"
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/testutil"
	"github.com/waj/fango/internal/types"
)

// The differential end-to-end suite (doc/design.md, "Testing and performance"): every
// testdata/run/*.fango runs through BOTH backends — the Core interpreter
// in-process and the compiled binary via the real CLI — and stdout is
// diffed byte-exact against the .expected file AND between the backends.
// Programs with a .error file instead assert a compile-error substring.
//
// Pure value programs produce no output, so the compiled leg runs under
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
	files := testutil.GlobFango(t, filepath.Join("..", "..", "testdata", "run"))
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

			// Backend 1: the Core interpreter. A Unit-typed main is
			// observed through its print output; any other main through
			// its value, shown via the shared fangort formatter.
			var stderr bytes.Buffer
			prog, ck, ok := compileFile(path, &stderr)
			if !ok {
				t.Fatalf("compile failed:\n%s", stderr.String())
			}
			env := eval.NewEnv()
			env.DefineProg(prog)
			var printed bytes.Buffer
			v, err := eval.ForceIO(context.Background(), "main", env, eval.NewIOContext(strings.NewReader(""), &printed))
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			var mainTy = prog.Defs[len(prog.Defs)-1].Type
			for _, d := range prog.Defs {
				if d.Name == "main" {
					mainTy = d.Type
				}
			}
			var evalOut string
			_, functionMain := mainTy.(*types.TFun)
			if con, isCon := mainTy.(*types.TCon); (isCon && con.Unique == ck.B.Unit.Unique) || functionMain {
				evalOut = printed.String()
			} else {
				evalOut = eval.ShowForPrint(v, mainTy, ck.B) + "\n"
			}
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
// the output is gofmt-idempotent (emitted via go/format.Node). Covers a
// value program, a printing (Unit main) program, and an IIFE-if program.
func TestEmitDeterministicAndFormatted(t *testing.T) {
	// poly_map_filter_foldr covers generic emission — instantiation
	// plumbing is where nondeterminism would first appear (risk #1).
	for _, name := range []string{"arith0.fango", "print_float.fango", "if_expr.fango", "block_area.fango", "block_print_order.fango", "fib.fango", "partial.fango", "poly_map_filter_foldr.fango", "poly_eq_nested.fango", "effect_translate_return.fango", "effect_nested_restore.fango", "effect_partial_capture.fango", "effect_row_union.fango"} {
		path := filepath.Join("..", "..", "testdata", "run", name)
		var stderr bytes.Buffer
		a, ok := emitGo(path, &stderr)
		if !ok {
			t.Fatalf("%s: emit failed:\n%s", name, stderr.String())
		}
		b, _ := emitGo(path, &stderr)
		if !bytes.Equal(a, b) {
			t.Errorf("%s: two compilations of the same file differ", name)
		}
		formatted, err := format.Source(a)
		if err != nil {
			t.Fatalf("%s: generated Go does not parse: %v", name, err)
		}
		if !bytes.Equal(formatted, a) {
			t.Errorf("%s: generated Go is not gofmt-idempotent:\n%s", name, a)
		}
	}
}

func TestCheckpoint2GeneratedGoHasNoContinuationRuntime(t *testing.T) {
	files := testutil.GlobFango(t, filepath.Join("..", "..", "testdata", "run"))
	for _, path := range files {
		if _, err := os.Stat(strings.TrimSuffix(path, ".fango") + ".error"); err == nil {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			var stderr bytes.Buffer
			src, ok := emitGo(path, &stderr)
			if !ok {
				t.Fatalf("emit failed:\n%s", stderr.String())
			}
			file, err := goparser.ParseFile(gotoken.NewFileSet(), path+".go", src, 0)
			if err != nil {
				t.Fatal(err)
			}
			goast.Inspect(file, func(n goast.Node) bool {
				switch n := n.(type) {
				case *goast.GoStmt:
					t.Error("generated a goroutine")
				case *goast.ChanType, *goast.SendStmt:
					t.Errorf("generated channel syntax %T", n)
				case *goast.CallExpr:
					if id, ok := n.Fun.(*goast.Ident); ok && id.Name == "panic" {
						t.Error("generated a panic sentinel")
					}
				case *goast.TypeSpec:
					if strings.Contains(strings.ToLower(n.Name.Name), "continuation") {
						t.Errorf("generated continuation type %q", n.Name.Name)
					}
				}
				return true
			})
		})
	}
}
