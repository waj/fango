package libroot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// library writes the smallest tree that satisfies the probe, plus whatever
// extra files a case needs.
func library(t *testing.T, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"stdlib/Prelude.fango": "module Prelude exposing ()\n"}
	for path, body := range extra {
		files[path] = body
	}
	for path, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestEnvRootWins(t *testing.T) {
	dir := library(t, nil)
	t.Setenv(EnvRoot, dir)
	got, err := search()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("root = %s, want %s", got, dir)
	}
}

// An explicit root that does not hold a library is an error rather than a
// reason to keep looking: a configured location silently ignored in favor of
// whatever checkout the shell happens to be in is the worst of both.
func TestEnvRootWithoutLibraryDoesNotFallThrough(t *testing.T) {
	t.Setenv(EnvRoot, t.TempDir())
	if _, err := search(); !Missing(err) {
		t.Fatalf("err = %v, want a not-found error", err)
	}
}

// With nothing configured, resolution finds the checkout the working
// directory is in. This is what makes `go test` and `go run` work untouched.
func TestCheckoutFallback(t *testing.T) {
	t.Setenv(EnvRoot, "")
	got, err := search()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(got, "go.mod")); err != nil {
		t.Fatalf("resolved %s, which is not a checkout: %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(got, filepath.FromSlash(probe))); err != nil {
		t.Fatalf("resolved %s, which has no %s: %v", got, probe, err)
	}
}

// The error names where the compiler looked, since that is what tells a user
// whether their install or their FANGO_ROOT is the problem.
func TestNotFoundNamesSearchedLocations(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvRoot, dir)
	_, err := search()
	if !Missing(err) {
		t.Fatalf("err = %v, want a not-found error", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("error %q does not name the searched %s", err, dir)
	}
	if !strings.Contains(err.Error(), EnvRoot) {
		t.Fatalf("error %q does not mention %s", err, EnvRoot)
	}
}

func TestSetForTestOverridesAndRestores(t *testing.T) {
	dir := library(t, nil)
	restore := SetForTest(dir)
	got, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("root = %s, want %s", got, dir)
	}
	restore()
	if got, err := Root(); err != nil || got == dir {
		t.Fatalf("root = %s (err %v), want the resolved checkout back", got, err)
	}
}

func TestReadsAndListsBothTrees(t *testing.T) {
	dir := library(t, map[string]string{
		"stdlib/List.fango":         "module List exposing ()\n",
		"stdlib/IO.native.go":       "package native\n",
		"stdlib/String.native.go":   "package native\n",
		"runtime/fangort/list.go":   "package fangort\n",
		"runtime/fangort/x_test.go": "package fangort\n",
	})
	defer SetForTest(dir)()

	if b, err := ReadStdlib("List.fango"); err != nil || !strings.Contains(string(b), "module List") {
		t.Fatalf("ReadStdlib = %q, %v", b, err)
	}
	if b, err := ReadRuntime("fangort/list.go"); err != nil || !strings.Contains(string(b), "package fangort") {
		t.Fatalf("ReadRuntime = %q, %v", b, err)
	}
	natives, err := StdlibNatives()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"IO.native.go", "String.native.go"}; !equal(natives, want) {
		t.Fatalf("StdlibNatives = %v, want %v", natives, want)
	}
	// Test files are excluded: a generated module compiles these sources and
	// cannot satisfy test-only dependencies.
	sources, err := RuntimePackage("fangort")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"fangort/list.go"}; !equal(sources, want) {
		t.Fatalf("RuntimePackage = %v, want %v", sources, want)
	}
}

func TestPathsCannotEscapeTheirTree(t *testing.T) {
	defer SetForTest(library(t, nil))()
	if _, err := ReadStdlib("../runtime/fangort/list.go"); err == nil {
		t.Fatal("escaping path accepted")
	}
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The embedded sources were case-exact, and the local provider beside them
// still is. Reading a joined path would regress that on the case-insensitive
// filesystems macOS and Windows default to, letting `import basics` reach the
// bundled Basics.
func TestNamesAreCaseExact(t *testing.T) {
	defer SetForTest(library(t, map[string]string{"stdlib/Basics.fango": "module Basics exposing ()\n"}))()
	if _, err := ReadStdlib("Basics.fango"); err != nil {
		t.Fatalf("exact name: %v", err)
	}
	if _, err := ReadStdlib("basics.fango"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist for a mis-cased name", err)
	}
}

// The embed patterns were flat, so a dotted module never named a nested file.
// Resolving them from disk must not quietly start.
func TestNestedStdlibPathsDoNotResolve(t *testing.T) {
	defer SetForTest(library(t, map[string]string{"stdlib/Foo/Bar.fango": "module Foo.Bar exposing ()\n"}))()
	if _, err := ReadStdlib("Foo/Bar.fango"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist for a nested name", err)
	}
}

// A missing module and a missing library are different situations: the graph
// falls through to a local file for the first and must not for the second.
func TestMissingModuleIsNotMissingLibrary(t *testing.T) {
	defer SetForTest(library(t, nil))()
	_, err := ReadStdlib("Nope.fango")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
	if Missing(err) {
		t.Fatal("a missing module reported itself as a missing library")
	}
}

// Readers within one process see one library, the way the embedded snapshot
// did, so a mid-compile edit cannot tear a build across two versions.
func TestContentsAreStableWithinAProcess(t *testing.T) {
	dir := library(t, map[string]string{"stdlib/List.fango": "first\n"})
	defer SetForTest(dir)()
	before, err := ReadStdlib("List.fango")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stdlib", "List.fango"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := ReadStdlib("List.fango")
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("contents changed mid-process: %q then %q", before, after)
	}
}
