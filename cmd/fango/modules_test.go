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
	"github.com/waj/fango/internal/eval"
)

func TestMultiModuleDifferential(t *testing.T) {
	for _, fixture := range []string{"basic", "effects"} {
		t.Run(fixture, func(t *testing.T) { testMultiModule(t, fixture) })
	}
}

func testMultiModule(t *testing.T, fixture string) {
	entry := filepath.Join("..", "..", "testdata", "modules", fixture, "Main.fango")
	want, err := os.ReadFile(strings.TrimSuffix(entry, ".fango") + ".expected")
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	prog, ck, ok := compileFile(entry, &stderr)
	if !ok {
		t.Fatalf("compile: %s", stderr.String())
	}
	env := eval.NewEnv()
	env.DefineProg(prog)
	v, err := eval.ForceIO(context.Background(), prog.Entry, env, eval.NewIOContext(strings.NewReader(""), &bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	var entryType = prog.Defs[len(prog.Defs)-1].Type
	for i := range prog.Defs {
		if prog.Defs[i].Name == prog.Entry {
			entryType = prog.Defs[i].Type
		}
	}
	if got := strings.TrimSpace(eval.Show(v, entryType, ck.B)); got != strings.TrimSpace(string(want)) {
		t.Fatalf("interpreter got %q, want %q", got, want)
	}

	if testing.Short() {
		return
	}
	stderr.Reset()
	t.Setenv("FANGO_BUILD_DIR", t.TempDir())
	t.Setenv("FANGO_INTERNAL_PRINT_MAIN", "1")
	dir, ok := ensureBuilt(entry, &stderr)
	if !ok {
		t.Fatalf("build: %s", stderr.String())
	}
	stdout, err := exec.Command(build.BinaryPath(dir)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(stdout)) != strings.TrimSpace(string(want)) {
		t.Fatalf("compiled got %q, want %q", stdout, want)
	}
}

func TestDependencyManifestInvalidatesBuild(t *testing.T) {
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
	cmd := exec.Command("go", "build", ".")
	cmd.Dir = out
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "go-cache"), "GOPROXY=off", "GOTOOLCHAIN=local")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build emitted project: %v\n%s", err, data)
	}
	cmd = exec.Command("go", "list", "./...")
	cmd.Dir = out
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "go-cache"), "GOPROXY=off", "GOTOOLCHAIN=local")
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

type Token = Token Int

token = Token 42
`)
	writeModuleFile(t, root, "Bridge.fango", `module Bridge exposing (Box(..), boxed, unitBox, apply)

import Base

type Box a = Box a

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
	want := "Token 42\nTrue\nBox (Token 42)\n"
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

func TestUserNativeCompiledAndInterpreterRefuses(t *testing.T) {
	root := t.TempDir()
	writeModuleFile(t, root, "Hash.fango", `module Hash exposing (twice, tick)

twice : Int -> Int
twice = native

tick : () -> Int
tick = native
`)
	writeModuleFile(t, root, "Hash.native.go", `package native

func Twice(x int64) int64 { return x * 2 }
func Tick() int64 { return 42 }
`)
	entry := writeModuleFile(t, root, "Main.fango", `module Main exposing (main)
import Hash
main() = print (Hash.twice (Hash.tick (print "before")))
`)
	var stderr bytes.Buffer
	prog, _, ok := compileFile(entry, &stderr)
	if !ok {
		t.Fatalf("compile: %s", stderr.String())
	}
	env := eval.NewEnv()
	env.DefineProg(prog)
	if _, err := eval.ForceIO(context.Background(), prog.Entry, env, eval.NewIOContext(strings.NewReader(""), &bytes.Buffer{})); err == nil || !strings.Contains(err.Error(), "native modules run only in compiled mode") {
		t.Fatalf("interpreter error = %v", err)
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

	writeModuleFile(t, root, "Hash.fango", `module Hash exposing (twice, tick)

twice x = x * 2
tick _ = 42
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
