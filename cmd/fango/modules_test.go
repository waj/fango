package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/build"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/nativehost"
)

func TestMultiModuleDifferential(t *testing.T) {
	t.Parallel()
	for _, fixture := range []string{"basic", "effects", "classes", "records", "blanket", "reflection", "deriver", "operators", "patterns"} {
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()
			testMultiModule(t, fixture)
		})
	}
}

func TestCrossModuleBlanketCycle(t *testing.T) {
	t.Parallel()
	entry := filepath.Join("..", "..", "testdata", "modules", "blanket_cycle", "Main.fango")
	runErrorCase(t, entry, "Circular blanket instance requirements")
}

func TestPrivateRecordFields(t *testing.T) {
	t.Parallel()
	entry := filepath.Join("..", "..", "testdata", "modules", "record_private", "Main.fango")
	want, err := os.ReadFile(strings.TrimSuffix(entry, ".fango") + ".error")
	if err != nil {
		t.Fatal(err)
	}
	runErrorCase(t, entry, strings.TrimSpace(string(want)))
}

func testMultiModule(t *testing.T, fixture string) {
	entry := filepath.Join("..", "..", "testdata", "modules", fixture, "Main.fango")
	want, err := os.ReadFile(strings.TrimSuffix(entry, ".fango") + ".expected")
	if err != nil {
		t.Fatal(err)
	}
	testMultiModuleEntry(t, entry, string(want))
}

func testMultiModuleEntry(t *testing.T, entry, want string) {
	t.Helper()
	var stderr bytes.Buffer
	prog, ck, ok := compileFile(entry, &stderr)
	if !ok {
		t.Fatalf("compile: %s", stderr.String())
	}
	env := eval.NewEnv()
	env.DefineProg(prog)
	_ = ck
	var shown any
	err := withInterpreter(func() error {
		if _, err := eval.ForceIO(context.Background(), prog.Entry, env, eval.NewIOContext(strings.NewReader(""), &bytes.Buffer{})); err != nil {
			return err
		}
		var err error
		shown, err = eval.EvalIO(context.Background(), prog.EntryDisplay, env, eval.NewIOContext(strings.NewReader(""), &bytes.Buffer{}))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(shown.(string)); got != strings.TrimSpace(string(want)) {
		t.Fatalf("interpreter got %q, want %q", got, want)
	}

	if testing.Short() {
		return
	}
	// The compiled leg goes through the real CLI, so multi-module fixtures
	// cover `fango run` end to end while each case owns its build directory.
	cmd := exec.Command(cliBinary(t), "run", entry)
	cmd.Env = append(os.Environ(),
		"FANGO_INTERNAL_PRINT_MAIN=1",
		"FANGO_BUILD_DIR="+t.TempDir())
	var stdout, runErr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &runErr
	if err := cmd.Run(); err != nil {
		t.Fatalf("fango run: %v\n%s", err, runErr.String())
	}
	if strings.TrimSpace(stdout.String()) != strings.TrimSpace(string(want)) {
		t.Fatalf("compiled got %q, want %q", stdout.String(), want)
	}
}

func TestCrossModuleContextPrecedence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, extra, value, want, diagnostic string
	}{
		{name: "applicable", value: "True", want: "show/show/show"},
		{name: "fallback", extra: "type Foo = Foo\n", value: "Foo", want: "fallback/fallback/show"},
		{name: "ambiguous", value: "1", diagnostic: "AMBIGUOUS INSTANCE"},
		{name: "stronger", extra: "instance (Show a, Num a) => Base.Inspect a\n    inspect _ = \"both\"\n", value: "1", want: "both/both/show"},
		{name: "concrete", extra: "instance Base.Inspect Int\n    inspect _ = \"int\"\n", value: "1", want: "int/int/show"},
		{name: "duplicate", extra: "instance Show b => Base.Inspect b\n    inspect _ = \"duplicate\"\n", value: "True", diagnostic: "OVERLAPPING INSTANCE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeModuleFile(t, root, "Base.fango", `module Base exposing (Inspect(..), forward)
class Inspect a
    inspect : a -> String
instance Inspect a
    inspect _ = "fallback"
forward : Inspect a => a -> String
forward x = inspect x
`)
			writeModuleFile(t, root, "Display.fango", `module Display exposing (early)
import Base
instance Show a => Base.Inspect a
    inspect _ = "show"
early() = Base.inspect 1
`)
			writeModuleFile(t, root, "Number.fango", `module Number exposing ()
import Base
instance Num a => Base.Inspect a
    inspect _ = "num"
`)
			entry := writeModuleFile(t, root, "Main.fango", "module Main exposing (main)\nimport Base\nimport Display\nimport Number\n"+tc.extra+"main = Base.inspect "+tc.value+" ++ \"/\" ++ Base.forward "+tc.value+" ++ \"/\" ++ Display.early()\n")
			if tc.diagnostic != "" {
				var stderr bytes.Buffer
				if _, _, ok := compileFile(entry, &stderr); ok || !strings.Contains(stderr.String(), tc.diagnostic) {
					t.Fatalf("wanted %s, got: %s", tc.diagnostic, stderr.String())
				}
				if tc.diagnostic == "AMBIGUOUS INSTANCE" {
					for _, location := range []string{"Display.fango:3:", "Number.fango:3:"} {
						if !strings.Contains(stderr.String(), location) {
							t.Errorf("missing candidate %s: %s", location, stderr.String())
						}
					}
				}
				return
			}
			testMultiModuleEntry(t, entry, tc.want)
		})
	}
}

func TestDependencyManifestInvalidatesBuild(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	entry := filepath.Join(src, "Main.fango")
	dep := filepath.Join(src, "Dep.fango")
	if err := os.WriteFile(entry, []byte("module Main exposing (main)\nimport Dep\nmain = Dep.answer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dep, []byte("module Dep exposing (answer)\nanswer = 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buildDir := t.TempDir()
	var stderr bytes.Buffer
	changed, ok := compileToDir(entry, buildDir, &stderr)
	if !ok || !changed {
		t.Fatalf("first compile changed=%v ok=%v: %s", changed, ok, stderr.String())
	}
	changed, ok = compileToDir(entry, buildDir, &stderr)
	if !ok || changed {
		t.Fatalf("unchanged compile changed=%v ok=%v: %s", changed, ok, stderr.String())
	}
	if err := os.WriteFile(dep, []byte("module Dep exposing (answer)\nanswer = 42\n-- comment-only edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, ok = compileToDir(entry, buildDir, &stderr)
	if !ok || !changed {
		t.Fatalf("dependency edit changed=%v ok=%v: %s", changed, ok, stderr.String())
	}
	manifest, err := os.ReadFile(filepath.Join(buildDir, "sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(manifest, []byte(`"module": "Dep"`)) {
		t.Fatalf("manifest: %s", manifest)
	}
	depGo := filepath.Join(buildDir, "modules", "Dep", "module.go")
	mainGo := filepath.Join(buildDir, "main.go")
	depBefore, err := os.ReadFile(depGo)
	if err != nil {
		t.Fatal(err)
	}
	mainBefore, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dep, []byte("module Dep exposing (answer)\nanswer = 43\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, ok = compileToDir(entry, buildDir, &stderr); !ok || !changed {
		t.Fatalf("dependency implementation edit changed=%v ok=%v: %s", changed, ok, stderr.String())
	}
	depAfter, _ := os.ReadFile(depGo)
	mainAfter, _ := os.ReadFile(mainGo)
	if bytes.Equal(depBefore, depAfter) {
		t.Fatal("dependency package did not change")
	}
	if !bytes.Equal(mainBefore, mainAfter) {
		t.Fatal("unchanged entry package was rewritten logically")
	}

	if err := os.WriteFile(entry, []byte("module Main exposing (main)\nmain = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, ok = compileToDir(entry, buildDir, &stderr); !ok || !changed {
		t.Fatalf("dependency removal changed=%v ok=%v: %s", changed, ok, stderr.String())
	}
	if _, err := os.Stat(depGo); !os.IsNotExist(err) {
		t.Fatalf("stale dependency file remains: %v", err)
	}
}

func TestEmitGoProjectDirectory(t *testing.T) {
	t.Parallel()
	entry := filepath.Join("..", "..", "testdata", "modules", "basic", "Main.fango")
	out := filepath.Join(t.TempDir(), "custom.out")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "--emit-go", "-o", out, entry}, &stdout, &stderr); code != 0 {
		t.Fatalf("emit exit %d: %s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("emit stdout = %q", stdout.String())
	}
	for _, rel := range []string{"go.mod", "main.go", "fangort/fangort.go", "modules/Geometry/Point/module.go", "sources.json", ".fango-generated.json"} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	// GOPROXY=off is what proves the emitted project is self-contained; the
	// ambient build cache is shared with every other compiled leg.
	cmd := exec.Command("go", "build", ".")
	cmd.Dir = out
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build emitted project: %v\n%s", err, data)
	}
	cmd = exec.Command("go", "list", "./...")
	cmd.Dir = out
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local")
	listed, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list emitted project: %v\n%s", err, listed)
	}
	for _, pkg := range []string{"fangobuild\n", "fangobuild/fangort\n", "fangobuild/modules/Geometry/Point\n"} {
		if !bytes.Contains(listed, []byte(pkg)) {
			t.Errorf("go list missing %q:\n%s", strings.TrimSpace(pkg), listed)
		}
	}
}

func TestEmitGoDefaultAndSafeDestination(t *testing.T) {
	entry, err := filepath.Abs(filepath.Join("..", "..", "testdata", "modules", "basic", "Main.fango"))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	t.Chdir(work)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "--emit-go", entry}, &stdout, &stderr); code != 0 {
		t.Fatalf("default emit exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(work, "Main.out", "main.go")); err != nil {
		t.Fatal(err)
	}

	unsafe := filepath.Join(work, "occupied")
	if err := os.Mkdir(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unsafe, "keep"), []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{"build", "--emit-go", "-o", unsafe, entry}, &stdout, &stderr); code != 1 {
		t.Fatalf("unsafe emit exit %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not a Fango-generated project") {
		t.Fatalf("unsafe diagnostic: %s", stderr.String())
	}
}

func TestModularCrossPackageABI(t *testing.T) {
	root := t.TempDir()
	writeModuleFile(t, root, "Base.fango", `module Base exposing (Token, token)

type Token = Token Int deriving (Eq, Show)

token = Token 42
`)
	writeModuleFile(t, root, "Bridge.fango", `module Bridge exposing (Box(..), boxed, unitBox, apply)

import Base

type Box a = Box a deriving (Eq, Show)

boxed = Box Base.token
unitBox = Box ()
apply action value = action value
`)
	entry := writeModuleFile(t, root, "Main.fango", `module Main exposing (main)

import Bridge exposing (Box(..), boxed, unitBox, apply)

unbox box =
    case box of
        Box value -> value

consumeUnit box =
    case box of
        Box _ -> ()

same = boxed == Box (unbox boxed)

main() =
    consumeUnit unitBox
    print (apply unbox boxed)
    print same
    print boxed
`)
	t.Setenv("FANGO_BUILD_DIR", filepath.Join(root, "build"))
	var stderr bytes.Buffer
	dir, ok := ensureBuilt(entry, &stderr)
	if !ok {
		t.Fatalf("build: %s", stderr.String())
	}
	stdout, err := exec.Command(build.BinaryPath(dir)).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "Token 42\nTrue\nBox Token 42\n"
	if string(stdout) != want {
		t.Fatalf("output %q, want %q", stdout, want)
	}
	mainGo, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(mainGo, []byte(`"fangobuild/modules/Base"`)) {
		t.Fatalf("entry package lacks transitive type-owner import:\n%s", mainGo)
	}
}

func TestUserNativeCompiledAndInterpreted(t *testing.T) {
	root := t.TempDir()
	writeModuleFile(t, root, "Hash.fango", `module Hash exposing (Clock(..), twice, constant, probe)

twice : Int -> Int
twice = native

constant : () -> Int
constant = native

effect Clock
    tick : () -> Int = native

probe : () ->{Clock} Int
probe() = tick()
`)
	writeModuleFile(t, root, "Hash.native.go", `package native

func Twice(x int64) int64 { return x * 2 }
func Constant() int64 { return 42 }
func Tick() int64 { return 42 }
`)
	entry := writeModuleFile(t, root, "Main.fango", `module Main exposing (main)
import Hash
main() = print (Hash.twice (Hash.constant (print "before")))
`)
	var stderr bytes.Buffer
	prog, ck, _, _, sources, ok := compileFileGraph(entry, &stderr)
	if !ok {
		t.Fatalf("compile: %s", stderr.String())
	}
	env := eval.NewEnv()
	env.DefineProg(prog)
	workerSources := make([]nativehost.Source, len(sources))
	for i, source := range sources {
		workerSources[i] = nativehost.Source{Module: source.Module, Content: source.Content}
	}
	executor, err := nativehost.New(workerSources)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	var interpreted bytes.Buffer
	ioctx := eval.NewIOContext(strings.NewReader(""), &interpreted)
	ioctx.Natives = executor
	if _, err := eval.ForceIO(context.Background(), prog.Entry, env, ioctx); err != nil || interpreted.String() != "before\n84\n" {
		t.Fatalf("interpreter output %q, err %v", interpreted.String(), err)
	}
	probe := &core.App{CalleeKind: core.Worker,
		Callee: &core.VarRef{Name: "Hash.probe", Ty: ck.Natives["Hash.tick"].Scheme.Body},
		Args:   []core.Expr{&core.UnitLit{Ty: ck.B.Unit}}, Ty: ck.B.Int}
	if got, err := eval.EvalIO(context.Background(), probe, env, ioctx); err != nil || got != int64(42) {
		t.Fatalf("native effect = %v, %v", got, err)
	}

	buildDir := filepath.Join(root, "build")
	t.Setenv("FANGO_BUILD_DIR", buildDir)
	t.Setenv("FANGO_INTERNAL_PRINT_MAIN", "1")
	dir, ok := ensureBuilt(entry, &stderr)
	if !ok {
		t.Fatalf("build: %s", stderr.String())
	}
	out, err := exec.Command(build.BinaryPath(dir)).Output()
	if err != nil || string(out) != "before\n84\n" {
		t.Fatalf("compiled output %q, err %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(buildDir, "native", "Hash", "native.go")); err != nil {
		t.Fatalf("sidecar was not materialized: %v", err)
	}
	writeModuleFile(t, root, "Hash.native.go", `package native

func Twice(x int64) int64 { return x * 3 }
func Constant() int64 { return 42 }
func Tick() int64 { return 42 }
`)
	if changed, ok := compileToDir(entry, buildDir, &stderr); !ok || !changed {
		t.Fatalf("sidecar edit changed=%v ok=%v: %s", changed, ok, stderr.String())
	}
	if err := build.GoBuild(buildDir); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	out, err = exec.Command(build.BinaryPath(dir)).Output()
	if err != nil || string(out) != "before\n126\n" {
		t.Fatalf("rebuilt output %q, err %v", out, err)
	}

	writeModuleFile(t, root, "Hash.fango", `module Hash exposing (twice, constant)

twice x = x * 2
constant _ = 42
`)
	if err := os.Remove(filepath.Join(root, "Hash.native.go")); err != nil {
		t.Fatal(err)
	}
	if changed, ok := compileToDir(entry, buildDir, &stderr); !ok || !changed {
		t.Fatalf("sidecar removal changed=%v ok=%v: %s", changed, ok, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(buildDir, "native", "Hash", "native.go")); !os.IsNotExist(err) {
		t.Fatalf("stale sidecar remains: %v", err)
	}
}

func writeModuleFile(t *testing.T, root, rel, contents string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScalarSpecializationModuleStability(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeModuleFile(t, root, "Dep.fango", `module Dep exposing (double)
double value = value + value
`)
	entry := writeModuleFile(t, root, "Main.fango", `module Main exposing (main)
import Dep
main = print (Dep.double 2)
`)
	before := generatedFile(t, emittedProject(t, entry), "modules/Dep/module.go")
	for _, scalar := range []string{"scalar_Int", "scalar_Float"} {
		if !bytes.Contains(before, []byte(scalar)) {
			t.Fatalf("dependency lacks %s variant:\n%s", scalar, before)
		}
	}
	// A new preceding dependency shifts type identities, while the changed
	// use requests the other scalar variant. Neither changes Dep's package.
	writeModuleFile(t, root, "Added.fango", `module Added exposing (Box(..))
type Box a = Box a
`)
	writeModuleFile(t, root, "Main.fango", `module Main exposing (main)
import Added
import Dep
main = print (Dep.double 2.5)
`)
	after := generatedFile(t, emittedProject(t, entry), "modules/Dep/module.go")
	if !bytes.Equal(before, after) {
		t.Fatalf("downstream edit changed dependency package:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
