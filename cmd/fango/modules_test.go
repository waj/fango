package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	cmd := []string{"run", entry}
	var stdout bytes.Buffer
	stderr.Reset()
	t.Setenv("FANGO_BUILD_DIR", t.TempDir())
	if code := run(cmd, &stdout, &stderr); code != 0 {
		t.Fatalf("run exit %d: %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("ordinary run unexpectedly printed %q", stdout.String())
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
}
