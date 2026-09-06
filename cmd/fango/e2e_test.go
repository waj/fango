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

	"github.com/waj/fango/internal/codegen"
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
			runDifferentialCase(t, path)
		})
	}
}

func TestMandelbrotExample(t *testing.T) {
	runDifferentialCase(t, filepath.Join("..", "..", "examples", "mandelbrot.fango"))
}

func TestGuessingGameExample(t *testing.T) {
	runDifferentialCase(t, filepath.Join("..", "..", "examples", "guess.fango"))
}

func runDifferentialCase(t *testing.T, path string) {
	t.Helper()
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
	stdin := ""
	if stdinData, err := os.ReadFile(base + ".stdin"); err == nil {
		stdin = string(stdinData)
	}

	// Backend 1: the Core interpreter. A Unit-typed main is observed through
	// its print output; any other main through its value and shared formatter.
	var stderr bytes.Buffer
	prog, ck, ok := compileFile(path, &stderr)
	if !ok {
		t.Fatalf("compile failed:\n%s", stderr.String())
	}
	env := eval.NewEnv()
	env.DefineProg(prog)
	var printed bytes.Buffer
	_, err = eval.ForceIO(context.Background(), "main", env, eval.NewIOContext(strings.NewReader(stdin), &printed))
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
		shown, err := eval.EvalIO(context.Background(), prog.EntryDisplay, env, eval.NewIOContext(strings.NewReader(stdin), &printed))
		if err != nil {
			t.Fatal(err)
		}
		evalOut = shown.(string) + "\n"
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
	cmd.Stdin = strings.NewReader(stdin)
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

func emittedProject(t *testing.T, path string) []codegen.File {
	t.Helper()
	var stderr bytes.Buffer
	files, _, ok := emitProjectManifest(path, &stderr)
	if !ok {
		t.Fatalf("emit failed:\n%s", stderr.String())
	}
	return files
}

func generatedFile(t *testing.T, files []codegen.File, path string) []byte {
	t.Helper()
	for _, file := range files {
		if file.Path == path {
			return file.Data
		}
	}
	t.Fatalf("generated project has no %s", path)
	return nil
}

// Determinism: compiling the same file twice yields byte-identical Go, and
// the output is gofmt-idempotent (emitted via go/format.Node). Covers a
// value program, a printing (Unit main) program, and an IIFE-if program.
func TestEmitDeterministicAndFormatted(t *testing.T) {
	// poly_map_filter_foldr covers generic emission — instantiation
	// plumbing is where nondeterminism would first appear (risk #1).
	for _, name := range []string{"arith0.fango", "print_float.fango", "if_expr.fango", "block_area.fango", "block_print_order.fango", "fib.fango", "partial.fango", "poly_map_filter_foldr.fango", "poly_eq_nested.fango", "effect_translate_return.fango", "effect_nested_restore.fango", "effect_partial_capture.fango", "effect_row_union.fango"} {
		path := filepath.Join("..", "..", "testdata", "run", name)
		a := emittedProject(t, path)
		b := emittedProject(t, path)
		if len(a) != len(b) {
			t.Fatalf("%s: two compilations changed file count", name)
		}
		for i := range a {
			if a[i].Path != b[i].Path || !bytes.Equal(a[i].Data, b[i].Data) {
				t.Errorf("%s: generated project differs at file %d", name, i)
			}
			formatted, err := format.Source(a[i].Data)
			if err != nil {
				t.Fatalf("%s: generated %s does not parse: %v", name, a[i].Path, err)
			}
			if !bytes.Equal(formatted, a[i].Data) {
				t.Errorf("%s: generated %s is not gofmt-idempotent:\n%s", name, a[i].Path, a[i].Data)
			}
		}
	}
}

func TestProjectEmitDeterministicAndFormatted(t *testing.T) {
	paths := []string{
		filepath.Join("..", "..", "testdata", "run", "poly_eq_nested.fango"),
		filepath.Join("..", "..", "testdata", "modules", "basic", "Main.fango"),
		filepath.Join("..", "..", "testdata", "modules", "effects", "Main.fango"),
	}
	for _, path := range paths {
		t.Run(filepath.Base(filepath.Dir(path))+"/"+filepath.Base(path), func(t *testing.T) {
			var stderr bytes.Buffer
			a, _, ok := emitProjectManifest(path, &stderr)
			if !ok {
				t.Fatalf("first emit failed:\n%s", stderr.String())
			}
			b, _, ok := emitProjectManifest(path, &stderr)
			if !ok || len(a) != len(b) {
				t.Fatalf("second emit failed or changed file count: %s", stderr.String())
			}
			for i := range a {
				if a[i].Path != b[i].Path || !bytes.Equal(a[i].Data, b[i].Data) {
					t.Errorf("generated project differs at file %d", i)
				}
				formatted, err := format.Source(a[i].Data)
				if err != nil {
					t.Fatalf("%s does not parse: %v", a[i].Path, err)
				}
				if !bytes.Equal(formatted, a[i].Data) {
					t.Errorf("%s is not gofmt-idempotent", a[i].Path)
				}
			}
		})
	}
}

func TestCheckpoint2GeneratedGoHasNoContinuationRuntime(t *testing.T) {
	files := testutil.GlobFango(t, filepath.Join("..", "..", "testdata", "run"))
	for _, path := range files {
		if _, err := os.Stat(strings.TrimSuffix(path, ".fango") + ".error"); err == nil {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			for _, generated := range emittedProject(t, path) {
				file, err := goparser.ParseFile(gotoken.NewFileSet(), generated.Path, generated.Data, 0)
				if err != nil {
					t.Fatal(err)
				}
				goast.Inspect(file, func(n goast.Node) bool {
					switch n := n.(type) {
					case *goast.GoStmt:
						t.Errorf("%s: generated a goroutine", generated.Path)
					case *goast.ChanType, *goast.SendStmt:
						t.Errorf("%s: generated channel syntax %T", generated.Path, n)
					case *goast.CallExpr:
						if id, ok := n.Fun.(*goast.Ident); ok && id.Name == "panic" {
							t.Errorf("%s: generated a panic sentinel", generated.Path)
						}
					case *goast.TypeSpec:
						if strings.Contains(strings.ToLower(n.Name.Name), "continuation") {
							t.Errorf("%s: generated continuation type %q", generated.Path, n.Name.Name)
						}
					case *goast.CompositeLit:
						if st, ok := n.Type.(*goast.StructType); ok && len(st.Fields.List) == 0 {
							t.Errorf("%s: generated anonymous empty-struct Unit literal", generated.Path)
						}
					}
					return true
				})
			}
		})
	}
}

func TestGeneratedGoUsesImplicitConcreteUnitABI(t *testing.T) {
	for _, name := range []string{"explicit_unit_calls.fango", "effect_handler.fango"} {
		path := filepath.Join("..", "..", "testdata", "run", name)
		src := generatedFile(t, emittedProject(t, path), "main.go")
		file, err := goparser.ParseFile(gotoken.NewFileSet(), path+".go", src, 0)
		if err != nil {
			t.Fatalf("%s: generated Go does not parse: %v", name, err)
		}
		goast.Inspect(file, func(n goast.Node) bool {
			if lit, ok := n.(*goast.CompositeLit); ok {
				if st, ok := lit.Type.(*goast.StructType); ok && len(st.Fields.List) == 0 {
					t.Errorf("%s: emitted anonymous empty-struct Unit literal", name)
				}
			}
			return true
		})
		if name == "explicit_unit_calls.fango" {
			text := string(src)
			if !strings.Contains(text, "func V_doSomething()") || strings.Contains(text, "V_doSomething(fangort.UnitValue)") {
				t.Errorf("%s: concrete Unit worker ABI was not erased:\n%s", name, text)
			}
		} else if name == "effect_handler.fango" && !strings.Contains(string(src), "Op_choose func() bool") {
			t.Errorf("%s: concrete Unit operation ABI was not erased:\n%s", name, src)
		}
	}
}

func TestGeneratedGoMaterializesNativeUnitOnlyInValueContext(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "mandelbrot.fango")
	src := string(generatedFile(t, emittedProject(t, path), "main.go"))
	if strings.Contains(src, "fangort.WriteString(\" \")\n\t\t\treturn fangort.UnitValue") {
		t.Fatalf("statement-position IO.write unnecessarily materialized Unit:\n%s", src)
	}
	if !strings.Contains(src, "fangort.WriteString(\" \")") {
		t.Fatalf("statement-position IO.write was not emitted directly:\n%s", src)
	}
}
