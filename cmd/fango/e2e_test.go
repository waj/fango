package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/natives"
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

// The CLI is built once, lazily, so short mode never pays for it. The Once
// records the failure rather than calling t.Fatal, because the goroutine that
// wins the race belongs to an arbitrary parallel case.
var (
	buildOnce sync.Once
	fangoBin  string
	buildErr  error
)

func cliBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fango-e2e")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(dir, "fango")
		if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("building CLI: %v\n%s", err, out)
			return
		}
		fangoBin = bin
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return fangoBin
}

// The compiled leg is subprocess work — a `go build` of generated Go and a
// run of the result — and each case compiles into its own FANGO_BUILD_DIR, so
// the suite runs in parallel. The interpreter leg is the exception; see
// interpret below.
func TestDifferential(t *testing.T) {
	files := testutil.GlobFango(t, filepath.Join("..", "..", "testdata", "run"))
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			runDifferentialCase(t, path)
		})
	}
}

func TestMandelbrotExample(t *testing.T) {
	t.Parallel()
	runDifferentialCase(t, filepath.Join("..", "..", "examples", "mandelbrot.fango"))
}

func TestGuessingGameExample(t *testing.T) {
	t.Parallel()
	runDifferentialCase(t, filepath.Join("..", "..", "examples", "guess.fango"))
}

func TestWcExample(t *testing.T) {
	t.Parallel()
	runDifferentialCase(t, filepath.Join("..", "..", "examples", "wc.fango"))
}

func TestMarkdownExample(t *testing.T) {
	t.Parallel()
	runDifferentialCase(t, filepath.Join("..", "..", "examples", "markdown.fango"))
}

func TestTodoExample(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "todo.fango")
	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(strings.TrimSuffix(path, ".fango") + ".expected")
	if err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"list"},
		{"add", "buy milk"},
		{"add", `write "tests"`},
		{"list"},
		{"done", "2"},
		{"list"},
	}

	prog, _, ok := compileFile(path, io.Discard)
	if !ok {
		t.Fatal("compile failed")
	}
	interpDir := t.TempDir()
	var interpreted bytes.Buffer
	for _, args := range commands {
		env := eval.NewEnv()
		env.DefineProg(prog)
		ioctx := eval.NewIOContext(strings.NewReader(""), &interpreted)
		ioctx.Args, ioctx.Dir = args, interpDir
		if _, err := eval.ForceIO(context.Background(), prog.Entry, env, ioctx); err != nil {
			t.Fatalf("interpreter %v: %v", args, err)
		}
	}
	if interpreted.String() != string(want) {
		t.Fatalf("interpreter output:\n%q\nwant:\n%q", interpreted.String(), want)
	}

	if testing.Short() {
		return
	}
	compiledDir := t.TempDir()
	buildDir := t.TempDir()
	var compiled bytes.Buffer
	for _, args := range commands {
		cliArgs := append([]string{"run", absPath, "--"}, args...)
		cmd := exec.Command(cliBinary(t), cliArgs...)
		var stderr bytes.Buffer
		cmd.Env = append(os.Environ(), "FANGO_BUILD_DIR="+buildDir)
		cmd.Dir, cmd.Stdout, cmd.Stderr = compiledDir, &compiled, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("compiled %v: %v\n%s", args, err, stderr.String())
		}
	}
	if compiled.String() != string(want) {
		t.Fatalf("compiled output:\n%q\nwant:\n%q", compiled.String(), want)
	}
	if compiled.String() != interpreted.String() {
		t.Fatalf("backends disagree: compiled %q vs interpreted %q", compiled.String(), interpreted.String())
	}
}

func TestTodoExampleFailures(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "todo.fango")
	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	prog, _, ok := compileFile(path, io.Discard)
	if !ok {
		t.Fatal("compile failed")
	}

	type failure struct {
		name       string
		args       []string
		store      string
		wantStatus int
		wantOutput string
	}
	cases := []failure{
		{"malformed store", []string{"list"}, "not json", 1, "Invalid todo.json.\n"},
		{"usage", nil, "", 2, "Usage: todo add TEXT | todo list | todo done NUMBER\n"},
		{"missing index", []string{"done", "1"}, "[]", 2, "No todo at index 1.\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/interpreter", func(t *testing.T) {
			dir := t.TempDir()
			if tc.store != "" {
				if err := os.WriteFile(filepath.Join(dir, "todo.json"), []byte(tc.store), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			env := eval.NewEnv()
			env.DefineProg(prog)
			var output bytes.Buffer
			ioctx := eval.NewIOContext(strings.NewReader(""), &output)
			ioctx.Args, ioctx.Dir = tc.args, dir
			_, err := eval.ForceIO(context.Background(), prog.Entry, env, ioctx)
			var exitErr *natives.ExitError
			if !errors.As(err, &exitErr) || exitErr.Code != tc.wantStatus {
				t.Fatalf("exit = %v, want status %d", err, tc.wantStatus)
			}
			if output.String() != tc.wantOutput {
				t.Fatalf("output = %q, want %q", output.String(), tc.wantOutput)
			}
		})

		if testing.Short() {
			continue
		}
		t.Run(tc.name+"/compiled", func(t *testing.T) {
			dir := t.TempDir()
			if tc.store != "" {
				if err := os.WriteFile(filepath.Join(dir, "todo.json"), []byte(tc.store), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			args := append([]string{"run", absPath, "--"}, tc.args...)
			cmd := exec.Command(cliBinary(t), args...)
			var stdout, stderr bytes.Buffer
			cmd.Env = append(os.Environ(), "FANGO_BUILD_DIR="+t.TempDir())
			cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &stdout, &stderr
			err := cmd.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.wantStatus {
				t.Fatalf("exit = %v, want status %d; stderr: %s", err, tc.wantStatus, stderr.String())
			}
			if stdout.String() != tc.wantOutput {
				t.Fatalf("output = %q, want %q", stdout.String(), tc.wantOutput)
			}
		})
	}
}

// The Core interpreter runs fango programs inside this process, against the
// same bundled-native globals a compiled program owns outright — the PRNG cell
// behind Random above all, which is process-global by design because one
// compiled program owns one process. A test binary hosting many programs
// breaks that assumption, so interpreter legs take turns. The compiled leg,
// where the time actually goes, stays parallel.
var interpret sync.Mutex

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
	evalOut := func() string {
		interpret.Lock()
		defer interpret.Unlock()

		env := eval.NewEnv()
		env.DefineProg(prog)
		var printed bytes.Buffer
		if _, err := eval.ForceIO(context.Background(), "main", env, eval.NewIOContext(strings.NewReader(stdin), &printed)); err != nil {
			t.Fatalf("eval: %v", err)
		}
		mainTy := prog.Defs[len(prog.Defs)-1].Type
		for _, d := range prog.Defs {
			if d.Name == "main" {
				mainTy = d.Type
			}
		}
		_, functionMain := mainTy.(*types.TFun)
		if con, isCon := mainTy.(*types.TCon); (isCon && con.Unique == ck.B.Unit.Unique) || functionMain {
			return printed.String()
		}
		shown, err := eval.EvalIO(context.Background(), prog.EntryDisplay, env, eval.NewIOContext(strings.NewReader(stdin), &printed))
		if err != nil {
			t.Fatal(err)
		}
		return shown.(string) + "\n"
	}()
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
		// Compile-time evaluation is pure, bounded, and native-restricted, so
		// splicing must leave generated Go byte-identical between runs.
		filepath.Join("..", "..", "testdata", "run", "meta_splice.fango"),
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

func TestProjectMaterializesBundledNativeSidecars(t *testing.T) {
	jsonPath := filepath.Join("..", "..", "testdata", "run", "json_encode.fango")
	files := emittedProject(t, jsonPath)
	again := emittedProject(t, jsonPath)
	for _, path := range []string{"native/IO/native.go", "native/Json/native.go", "native/String/native.go"} {
		src := generatedFile(t, files, path)
		if !bytes.HasPrefix(src, []byte("package native\n")) {
			t.Errorf("%s was not materialized as package native:\n%s", path, src)
		}
		if !bytes.Equal(src, generatedFile(t, again, path)) {
			t.Errorf("%s changed between identical emissions", path)
		}
		if formatted, err := format.Source(src); err != nil || !bytes.Equal(formatted, src) {
			t.Errorf("%s is not gofmt-idempotent: %v", path, err)
		}
		host := generatedFile(t, files, strings.TrimSuffix(path, "native.go")+"host.go")
		if !bytes.Contains(host, []byte("var FangoHost")) {
			t.Errorf("%s has no generated host binding:\n%s", path, host)
		}
	}
	if src := generatedFile(t, files, "native/IO/native.go"); !bytes.Contains(src, []byte("func Write")) || bytes.Contains(src, []byte("fangort.")) {
		t.Errorf("IO sidecar does not own its implementation:\n%s", src)
	}

	files = emittedProject(t, filepath.Join("..", "..", "testdata", "run", "stdlib_random.fango"))
	if src := generatedFile(t, files, "native/Random/native.go"); !bytes.Contains(src, []byte("func SwapSeed")) {
		t.Errorf("Random sidecar did not contain its implementation:\n%s", src)
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
				// Native sidecars are library-authored Go, not compiler-emitted
				// control flow; panics are explicitly allowed to cross their ABI.
				if strings.HasPrefix(generated.Path, "native/") {
					continue
				}
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

// Self tail calls compile to loops (doc/design.md, "Go backend and runtime"):
// an eligible worker's FuncDecl contains a ForStmt and no self call, while a
// capture-excluded worker keeps the self call and gains no ForStmt. Codegen
// emits ForStmts nowhere else, so the signal is unambiguous.
func TestTailLoopGeneratedShape(t *testing.T) {
	cases := []struct {
		fixture, fn string
		loop        bool
	}{
		{"tail_loop_deep.fango", "V_loop", true},
		{"tail_loop_unit.fango", "V_countdown", true},
		// Equation dispatch happens inside the worker, so a grouped
		// definition is still eligible for the loop rewrite.
		{"tail_loop_equations.fango", "V_total", true},
		{"tail_loop_equations.fango", "V_build", true},
		{"tail_capture.fango", "V_applyAll", true},
		{"tail_capture.fango", "V_build", false},
	}
	for _, tc := range cases {
		path := filepath.Join("..", "..", "testdata", "run", tc.fixture)
		src := generatedFile(t, emittedProject(t, path), "main.go")
		file, err := goparser.ParseFile(gotoken.NewFileSet(), tc.fixture+".go", src, 0)
		if err != nil {
			t.Fatalf("%s: generated Go does not parse: %v", tc.fixture, err)
		}
		var fn *goast.FuncDecl
		for _, decl := range file.Decls {
			if d, ok := decl.(*goast.FuncDecl); ok && d.Name.Name == tc.fn {
				fn = d
			}
		}
		if fn == nil {
			t.Fatalf("%s: generated Go has no func %s:\n%s", tc.fixture, tc.fn, src)
		}
		hasFor, hasSelfCall := false, false
		goast.Inspect(fn.Body, func(n goast.Node) bool {
			switch n := n.(type) {
			case *goast.ForStmt:
				hasFor = true
			case *goast.CallExpr:
				if id, ok := n.Fun.(*goast.Ident); ok && id.Name == tc.fn {
					hasSelfCall = true
				}
			}
			return true
		})
		if hasFor != tc.loop || hasSelfCall == tc.loop {
			t.Errorf("%s: func %s has ForStmt=%v selfCall=%v, want ForStmt=%v selfCall=%v:\n%s",
				tc.fixture, tc.fn, hasFor, hasSelfCall, tc.loop, !tc.loop, src)
		}
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
	if strings.Contains(src, "n_IO.Write(\" \")\n\t\t\treturn fangort.UnitValue") {
		t.Fatalf("statement-position IO.write unnecessarily materialized Unit:\n%s", src)
	}
	if !strings.Contains(src, "n_IO.Write(\" \")") {
		t.Fatalf("statement-position IO.write was not emitted directly:\n%s", src)
	}
}
