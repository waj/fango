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
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/waj/fango/internal/build"
	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/libroot"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/nativehost"
	"github.com/waj/fango/internal/natives"
	"github.com/waj/fango/internal/testutil"
	"github.com/waj/fango/internal/types"
)

// The differential end-to-end suite (doc/design.md, "Testing and performance"): every
// testdata/run/*.fango runs through BOTH backends — the Core interpreter
// in-process and generated Go compiled and executed — and stdout is diffed
// byte-exact against the .expected file AND between the backends. Programs
// with a .error file instead assert a compile-error substring.
//
// Pure value programs produce no output, so the compiled leg is emitted with
// printMain and the eval leg shows main's value through the same shared
// fangort formatter.
//
// The run fixtures' compiled legs share one Go project (fixtureBatch): each
// entry module becomes its own package under progs/, the bundled packages
// they all emit are written once, and a single `go build` produces every
// binary. The examples and the multi-module fixtures instead go through the
// real CLI once per differential runner; dedicated command cases cover
// `fango run` itself end to end.

// The CLI is built once, lazily, so short mode never pays for it. The Once
// records the failure rather than calling t.Fatal, because the goroutine that
// wins the race belongs to an arbitrary parallel case.
var (
	buildOnce sync.Once
	cliDir    string
	fangoBin  string
	buildErr  error

	cliBuildsMu sync.Mutex
	cliBuilds   = make(map[string]*cliBuild)
)

// cliBuild is the reusable output of compiling one source through the public
// CLI. The source can be exercised repeatedly with different argv, stdin, and
// working directories without re-running the compiler and Go toolchain.
type cliBuild struct {
	once   sync.Once
	binary string
	err    error
}

func cliBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fango-e2e")
		if err != nil {
			buildErr = err
			return
		}
		cliDir = dir
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

func cliCompiledBinary(t *testing.T, source string) string {
	t.Helper()
	absSource, err := filepath.Abs(source)
	if err != nil {
		t.Fatal(err)
	}

	cliBuildsMu.Lock()
	b := cliBuilds[absSource]
	if b == nil {
		b = new(cliBuild)
		cliBuilds[absSource] = b
	}
	cliBuildsMu.Unlock()
	return buildCLIBinary(t, source, absSource, b)
}

func buildCLIBinary(t *testing.T, source, absSource string, b *cliBuild) string {
	t.Helper()
	b.once.Do(func() {
		dir, err := os.MkdirTemp(filepath.Dir(cliBinary(t)), "program-")
		if err != nil {
			b.err = err
			return
		}
		b.binary = filepath.Join(dir, "program")
		cmd := exec.Command(cliBinary(t), "build", "-o", b.binary, absSource)
		cmd.Env = append(os.Environ(),
			"FANGO_INTERNAL_PRINT_MAIN=1",
			"FANGO_BUILD_DIR="+filepath.Join(dir, "build"))
		if out, err := cmd.CombinedOutput(); err != nil {
			b.err = fmt.Errorf("building %s through fango: %w\n%s", source, err, out)
		}
	})
	if b.err != nil {
		t.Fatal(b.err)
	}
	return b.binary
}

func TestMain(m *testing.M) {
	// The suite builds the CLI into a temporary directory and runs it with
	// working directories of its own, so neither the executable-relative
	// install layout nor the checkout walk reaches the library this
	// repository owns. Naming it explicitly is what every spawned command
	// inherits, compiled fixtures and their native workers included. It is
	// process-wide rather than per-test because this suite runs in parallel.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		fmt.Fprintf(os.Stderr, "locating the Fango library: %v\n", err)
		os.Exit(1)
	}
	os.Setenv(libroot.EnvRoot, root)

	code := m.Run()
	for _, dir := range []string{cliDir, batch.dir} {
		if dir != "" {
			os.RemoveAll(dir)
		}
	}
	os.Exit(code)
}

// fixtureEmissions holds every runnable testdata/run fixture's emitted Go
// project (with printMain), keyed by fixture path. Emission is pure and
// CPU-bound, so it runs across a worker pool; the structural tests inspect
// these files and fixtureBatch compiles them.
type fixtureEmissions struct {
	paths []string
	files map[string][]codegen.File
	err   error
}

var (
	emissionsOnce sync.Once
	emissions     fixtureEmissions
)

func runFixtureEmissions(t *testing.T) *fixtureEmissions {
	t.Helper()
	emissionsOnce.Do(func() { emissions = emitRunFixtures() })
	if emissions.err != nil {
		t.Fatal(emissions.err)
	}
	return &emissions
}

func emitRunFixtures() fixtureEmissions {
	matches, err := filepath.Glob(filepath.Join("..", "..", "testdata", "run", "*.fango"))
	if err != nil {
		return fixtureEmissions{err: err}
	}
	var paths []string
	for _, path := range matches {
		if strings.HasPrefix(filepath.Base(path), ".") {
			continue
		}
		if _, err := os.Stat(strings.TrimSuffix(path, ".fango") + ".error"); err == nil {
			continue
		}
		paths = append(paths, path)
	}

	type result struct {
		path  string
		files []codegen.File
		err   error
	}
	work := make(chan string)
	results := make(chan result)
	var wg sync.WaitGroup
	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
		wg.Go(func() {
			for path := range work {
				var stderr bytes.Buffer
				files, _, ok := emitProjectManifest(path, true, &stderr)
				if !ok {
					results <- result{path: path, err: fmt.Errorf("%s: emit failed:\n%s", path, stderr.String())}
					continue
				}
				results <- result{path: path, files: files}
			}
		})
	}
	go func() {
		for _, path := range paths {
			work <- path
		}
		close(work)
		wg.Wait()
		close(results)
	}()
	out := fixtureEmissions{paths: paths, files: make(map[string][]codegen.File, len(paths))}
	for r := range results {
		if r.err != nil {
			out.err = errors.Join(out.err, r.err)
		}
		out.files[r.path] = r.files
	}
	return out
}

// fixtureBatch is the shared Go project built from fixtureEmissions: one
// `go build` writes bin/<fixture> for every runnable fixture. Shared paths
// emitted by more than one fixture must be byte-identical — bundled and
// dependency packages do not depend on their consumer — and any difference
// fails the batch.
type fixtureBatch struct {
	dir string
	err error
}

var (
	batchOnce sync.Once
	batch     fixtureBatch
)

// prepareFixtureBatch emits and builds the batch once; concurrent callers
// block until the first completes. TestDifferential starts it in the
// background so the Go build overlaps the serialized interpreter legs.
func prepareFixtureBatch() {
	emissionsOnce.Do(func() { emissions = emitRunFixtures() })
	if emissions.err != nil {
		return
	}
	batchOnce.Do(func() { batch = buildFixtureBatch(&emissions) })
}

func runFixtureBatch(t *testing.T) *fixtureBatch {
	t.Helper()
	prepareFixtureBatch()
	if emissions.err != nil {
		t.Fatal(emissions.err)
	}
	if batch.err != nil {
		t.Fatal(batch.err)
	}
	return &batch
}

func (b *fixtureBatch) binary(path string) string {
	return filepath.Join(b.dir, "bin", strings.TrimSuffix(filepath.Base(path), ".fango"))
}

func buildFixtureBatch(emitted *fixtureEmissions) fixtureBatch {
	dir, err := os.MkdirTemp("", "fango-e2e-batch")
	if err != nil {
		return fixtureBatch{err: err}
	}
	b := fixtureBatch{dir: dir}
	owner := make(map[string]string)
	for _, path := range emitted.paths {
		name := strings.TrimSuffix(filepath.Base(path), ".fango")
		for _, file := range emitted.files[path] {
			rel := filepath.FromSlash(file.Path)
			if file.Path == "main.go" {
				rel = filepath.Join("progs", name, "main.go")
			} else if first, seen := owner[file.Path]; seen {
				existing, err := os.ReadFile(filepath.Join(dir, rel))
				if err != nil {
					b.err = err
					return b
				}
				if !bytes.Equal(existing, file.Data) {
					b.err = fmt.Errorf("%s emitted by %s differs from the one emitted by %s: bundled and dependency packages must not depend on their consumer", file.Path, name, first)
					return b
				}
				continue
			} else {
				owner[file.Path] = name
			}
			if _, err := build.WriteIfChanged(filepath.Join(dir, rel), file.Data); err != nil {
				b.err = err
				return b
			}
		}
	}
	if _, err := build.Materialize(dir); err != nil {
		b.err = err
		return b
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		b.err = err
		return b
	}
	if err := build.GoBuildPackages(dir, binDir+string(filepath.Separator), "./progs/..."); err != nil {
		b.err = err
	}
	return b
}

// The compiled legs come from the single batch build, so the cases run in
// parallel with no per-case build work. The interpreter leg is the exception;
// see interpret below.
func TestDifferential(t *testing.T) {
	t.Parallel()
	files := testutil.GlobFango(t, filepath.Join("..", "..", "testdata", "run"))
	if !testing.Short() {
		go prepareFixtureBatch()
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			runDifferentialCase(t, path, batchRunner(path))
		})
	}
}

func TestMandelbrotExample(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "mandelbrot.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestGuessingGameExample(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "guess.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestCalculatorExample(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "calculator.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestWcExample(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "wc.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestMarkdownExample(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "markdown.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestTodoExample(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "todo.fango")
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
	func() {
		interpret.Lock()
		defer interpret.Unlock()
		for _, args := range commands {
			env := eval.NewEnv()
			env.DefineProg(prog)
			ioctx := eval.NewIOContext(strings.NewReader(""), &interpreted)
			ioctx.Args, ioctx.Dir = args, interpDir
			if _, err := eval.ForceIO(context.Background(), prog.Entry, env, ioctx); err != nil {
				t.Fatalf("interpreter %v: %v", args, err)
			}
		}
	}()
	if interpreted.String() != string(want) {
		t.Fatalf("interpreter output:\n%q\nwant:\n%q", interpreted.String(), want)
	}

	if testing.Short() {
		return
	}
	compiledDir := t.TempDir()
	var compiled bytes.Buffer
	for _, args := range commands {
		cmd := exec.Command(cliCompiledBinary(t, path), args...)
		var stderr bytes.Buffer
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
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "todo.fango")
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
			err := withInterpreter(func() error {
				_, err := eval.ForceIO(context.Background(), prog.Entry, env, ioctx)
				return err
			})
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
			cmd := exec.Command(cliCompiledBinary(t, path), tc.args...)
			var stdout, stderr bytes.Buffer
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

// The Core interpreter runs Fango programs inside this process and shares its
// persistent native-worker host infrastructure across cases. Every in-process
// evaluation in this package's parallel tests therefore holds interpret; the
// compiled legs stay parallel. Handler-local Random state itself needs no
// serialization.
var interpret sync.Mutex

func withInterpreter(run func() error) error {
	interpret.Lock()
	defer interpret.Unlock()
	return run()
}

func runDifferentialCase(t *testing.T, path string, compiled compiledRunner) {
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
	runDifferentialCaseWith(t, path, compiled, readFixtureInputs(t, base), string(expData))
}

// runDifferentialCaseWith runs one program under both backends with explicit
// inputs and expected output, so a test can drive the same source through
// several argument sets.
func runDifferentialCaseWith(t *testing.T, path string, compiled compiledRunner, in fixtureInputs, expected string) {
	t.Helper()

	// Backend 1: the Core interpreter. A Unit-typed main is observed through
	// its print output; any other main through its value and shared formatter.
	// A fixture with a sibling sidecar gets its own worker, exactly as a user
	// module would in an interpreter session.
	var stderr bytes.Buffer
	prog, ck, _, _, sources, ok := compileFileGraph(path, &stderr)
	if !ok {
		t.Fatalf("compile failed:\n%s", stderr.String())
	}
	evalOut := func() string {
		interpret.Lock()
		defer interpret.Unlock()

		env := eval.NewEnv()
		env.DefineProg(prog)
		if prog.Intrinsics[types.StreamWithProducerName] || prog.Intrinsics[types.IteratorNextName] {
			machineProg, errs := machineir.Lower(prog, ck.B)
			if len(errs) > 0 {
				t.Fatalf("machine lowering: %v", errs)
			}
			if err := env.DefineMachineProg(machineProg); err != nil {
				t.Fatal(err)
			}
		}
		var printed bytes.Buffer
		ioctx := eval.NewIOContext(strings.NewReader(in.stdin), &printed)
		ioctx.Args = in.args
		if dir := seedDir(t, in); dir != "" {
			ioctx.Dir = dir
		}
		if len(sources) > 0 {
			workerSources := make([]nativehost.Source, len(sources))
			for i, source := range sources {
				workerSources[i] = nativehost.Source{Module: source.Module, Content: source.Content}
			}
			executor, err := nativehost.New(workerSources)
			if err != nil {
				t.Fatal(err)
			}
			defer executor.Close()
			ioctx.Natives = executor
		}
		_, err := eval.ForceIO(context.Background(), "main", env, ioctx)
		var exitErr *natives.ExitError
		switch {
		case errors.As(err, &exitErr):
			if exitErr.Code != in.status {
				t.Fatalf("eval: exited with status %d, want %d", exitErr.Code, in.status)
			}
			return printed.String()
		case err != nil:
			t.Fatalf("eval: %v", err)
		case in.status != 0:
			t.Fatalf("eval: completed normally, want exit status %d", in.status)
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
		shown, err := eval.EvalIO(context.Background(), prog.EntryDisplay, env, ioctx)
		if err != nil {
			t.Fatal(err)
		}
		return shown.(string) + "\n"
	}()
	if evalOut != expected {
		t.Errorf("interpreter output:\n%q\nwant:\n%q", evalOut, expected)
	}

	// Backend 2: generated Go, compiled and executed.
	if testing.Short() {
		t.Skip("compiled leg skipped in -short mode")
	}
	compiledOut, status := compiled(t, in, seedDir(t, in))
	if status != in.status {
		t.Errorf("compiled exit status %d, want %d", status, in.status)
	}
	if compiledOut != expected {
		t.Errorf("compiled output:\n%q\nwant:\n%q", compiledOut, expected)
	}
	if compiledOut != evalOut {
		t.Errorf("backends disagree: compiled %q vs interpreted %q", compiledOut, evalOut)
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

// emittedProject compiles from source. Artifacts are bypassed on purpose: a
// test that asks what the compiler generates must not be answered with bytes
// an earlier run stored, which would make a determinism check compare a result
// with a copy of itself.
func emittedProject(t *testing.T, path string) []codegen.File {
	t.Helper()
	var stderr bytes.Buffer
	files, _, ok := emitProjectManifestSession(path, false, &stderr, &compilationSession{noCache: true})
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
	t.Parallel()
	// poly_map_filter_foldr covers generic emission — instantiation
	// plumbing is where nondeterminism would first appear (risk #1).
	// list_literals and stdlib_list cover the bundled List's runtime
	// representation, whose emission is driven by a nominal identity rather
	// than by a name (doc/roadmap-list.md).
	for _, name := range []string{"arith0.fango", "print_float.fango", "if_expr.fango", "block_area.fango", "block_print_order.fango", "fib.fango", "partial.fango", "poly_map_filter_foldr.fango", "poly_eq_nested.fango", "list_literals.fango", "stdlib_list.fango", "effect_translate_return.fango", "effect_nested_restore.fango", "effect_partial_capture.fango", "effect_row_union.fango", "scope_cleanup_failure.fango", "file_copy.fango", "native_wrapper.fango"} {
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
	t.Parallel()
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
			a, _, ok := emitProjectManifest(path, false, &stderr)
			if !ok {
				t.Fatalf("first emit failed:\n%s", stderr.String())
			}
			b, _, ok := emitProjectManifest(path, false, &stderr)
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
	t.Parallel()
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
	if src := generatedFile(t, files, "native/Random/native.go"); !bytes.Contains(src, []byte("func EntropySeed")) {
		t.Errorf("Random sidecar did not contain the entropy implementation:\n%s", src)
	}
}

func TestGeneratedGoHasNoContinuationRuntime(t *testing.T) {
	t.Parallel()
	emitted := runFixtureEmissions(t)
	for _, path := range emitted.paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			for _, generated := range emitted.files[path] {
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
						if sel, ok := n.Fun.(*goast.SelectorExpr); ok {
							pkg, isIdent := sel.X.(*goast.Ident)
							forbidden := map[string]bool{"RunGeneral": true, "Perform": true, "Resume": true, "Discard": true}
							if isIdent && pkg.Name == "fangort" && forbidden[sel.Sel.Name] {
								t.Errorf("%s: generated forbidden continuation runtime call fangort.%s", generated.Path, sel.Sel.Name)
							}
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "mandelbrot.fango")
	src := string(generatedFile(t, emittedProject(t, path), "main.go"))
	if strings.Contains(src, "n_IO.Write(\" \")\n\t\t\treturn fangort.UnitValue") {
		t.Fatalf("statement-position IO.write unnecessarily materialized Unit:\n%s", src)
	}
	if !strings.Contains(src, "n_IO.Write(\" \")") {
		t.Fatalf("statement-position IO.write was not emitted directly:\n%s", src)
	}
}

// The bundled List has a runtime representation: its module declares no marker
// interface and no constructor structs, and its consumers name fangort.List.
// A locally declared cons type is a different nominal type and is unaffected —
// recognition is by identity, so the negative case is the one that proves the
// mechanism is not matching on a spelling (doc/roadmap-list.md).
func TestBundledListUsesRuntimeRepresentationAndLocalListDoesNot(t *testing.T) {
	t.Parallel()
	listModule := filepath.Join("modules", "List", "module.go")

	bundled := emittedProject(t, filepath.Join("..", "..", "testdata", "run", "list_literals.fango"))
	list := string(generatedFile(t, bundled, listModule))
	for _, unwanted := range []string{"type T_List_dot_List", "isT_List_dot_List", "C_List_dot_Cons", "C_List_dot_Nil"} {
		if strings.Contains(list, unwanted) {
			t.Errorf("bundled List still emits %q:\n%s", unwanted, list)
		}
	}
	if !strings.Contains(list, "fangort.List[") {
		t.Errorf("bundled List module never names fangort.List:\n%s", list)
	}
	// The same fixture declares its own `LocalList`, and its bracket syntax
	// still means the bundled constructors.
	main := string(generatedFile(t, bundled, "main.go"))
	for _, want := range []string{"type T_LocalList interface", "C_Cons", "fangort.ListCons[int64]"} {
		if !strings.Contains(main, want) {
			t.Errorf("entry module is missing %q:\n%s", want, main)
		}
	}

	// A user type of List's exact shape, in a program that never uses the
	// bundled one, keeps the ordinary cons lowering.
	local := emittedProject(t, filepath.Join("..", "..", "testdata", "run", "poly_eq_nested.fango"))
	entry := string(generatedFile(t, local, "main.go"))
	if !strings.Contains(entry, "interface") {
		t.Errorf("user ADTs stopped emitting marker interfaces:\n%s", entry)
	}
}

func TestCsvExample(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "csv.fango")
	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "examples", "csv.expected"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile(filepath.Join("..", "..", "examples", "expenses.csv"))
	if err != nil {
		t.Fatal(err)
	}
	// The report is read from the working directory, so each leg gets its own
	// copy of the committed fixture.
	seed := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "expenses.csv"), input, 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	prog, _, ok := compileFile(path, io.Discard)
	if !ok {
		t.Fatal("compile failed")
	}
	env := eval.NewEnv()
	env.DefineProg(prog)
	var interpreted bytes.Buffer
	ioctx := eval.NewIOContext(strings.NewReader(""), &interpreted)
	ioctx.Args, ioctx.Dir = []string{"expenses.csv"}, seed(t)
	err = withInterpreter(func() error {
		_, err := eval.ForceIO(context.Background(), prog.Entry, env, ioctx)
		return err
	})
	if err != nil {
		t.Fatalf("interpreter: %v", err)
	}
	if interpreted.String() != string(want) {
		t.Fatalf("interpreter output:\n%q\nwant:\n%q", interpreted.String(), want)
	}

	if testing.Short() {
		return
	}
	cmd := exec.Command(cliBinary(t), "run", absPath, "--", "expenses.csv")
	var compiled, stderr bytes.Buffer
	cmd.Env = append(os.Environ(), "FANGO_BUILD_DIR="+t.TempDir())
	cmd.Dir, cmd.Stdout, cmd.Stderr = seed(t), &compiled, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("compiled: %v\n%s", err, stderr.String())
	}
	if compiled.String() != string(want) {
		t.Fatalf("compiled output:\n%q\nwant:\n%q", compiled.String(), want)
	}
	if compiled.String() != interpreted.String() {
		t.Fatalf("backends disagree: compiled %q vs interpreted %q", compiled.String(), interpreted.String())
	}
}

func TestCsvExampleFailures(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "csv.fango")
	prog, _, ok := compileFile(path, io.Discard)
	if !ok {
		t.Fatal("compile failed")
	}

	cases := []struct {
		name       string
		args       []string
		wantStatus int
		wantOutput string
	}{
		{"usage", nil, 1, "usage: csv FILE\n"},
		{"too many arguments", []string{"a", "b"}, 1, "usage: csv FILE\n"},
		{"missing file", []string{"nope.csv"}, 1, "cannot read nope.csv\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/interpreter", func(t *testing.T) {
			env := eval.NewEnv()
			env.DefineProg(prog)
			var output bytes.Buffer
			ioctx := eval.NewIOContext(strings.NewReader(""), &output)
			ioctx.Args, ioctx.Dir = tc.args, t.TempDir()
			err := withInterpreter(func() error {
				_, err := eval.ForceIO(context.Background(), prog.Entry, env, ioctx)
				return err
			})
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
			cmd := exec.Command(cliCompiledBinary(t, path), tc.args...)
			var stdout, stderr bytes.Buffer
			cmd.Dir, cmd.Stdout, cmd.Stderr = t.TempDir(), &stdout, &stderr
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
