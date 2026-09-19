package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/libroot"
)

// withoutLibrary is os.Environ with FANGO_ROOT removed. TestMain sets it
// process-wide, and the CLI inherits the environment, so a case about a
// missing library has to take it back out rather than add to it.
func withoutLibrary() []string {
	var env []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, libroot.EnvRoot+"=") {
			continue
		}
		env = append(env, entry)
	}
	return env
}

// A compiler that cannot find its library must say so. The failure mode this
// guards against is the search falling through to the source root and
// reporting that Prelude is missing from the user's own directory.
func TestMissingLibraryReportsItself(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "Main.fango")
	if err := os.WriteFile(entry, []byte("main = print 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(cliBinary(t), "check", entry)
	cmd.Dir = dir
	cmd.Env = withoutLibrary()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("check succeeded with no library")
	}
	out := stdout.String() + stderr.String()
	if !strings.Contains(out, "MISSING LIBRARY") {
		t.Fatalf("output does not report a missing library:\n%s", out)
	}
	if !strings.Contains(out, libroot.EnvRoot) {
		t.Fatalf("output does not name %s:\n%s", libroot.EnvRoot, out)
	}
	if strings.Contains(out, "internal compiler error") {
		t.Fatalf("a missing library was reported as a compiler bug:\n%s", out)
	}
	if strings.Contains(out, "MISSING MODULE") {
		t.Fatalf("a missing library was reported as a missing user module:\n%s", out)
	}
}

// A FANGO_ROOT that names no library is a typo to report, not a reason to
// quietly compile against whichever checkout the shell happens to be in.
func TestConfiguredLibraryIsNotSecondGuessed(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "Main.fango")
	if err := os.WriteFile(entry, []byte("main = print 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	cmd := exec.Command(cliBinary(t), "check", entry)
	// The working directory is this repository's checkout, which the search
	// would otherwise accept.
	cmd.Env = append(withoutLibrary(), libroot.EnvRoot+"="+empty)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("check succeeded against an empty library root")
	}
	out := stdout.String() + stderr.String()
	if !strings.Contains(out, empty) {
		t.Fatalf("output does not name the configured root %s:\n%s", empty, out)
	}
}

// copyTree copies a directory of the repository's library into a temporary
// root, so a test can edit the standard library without touching the one this
// checkout owns.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		src, dst := filepath.Join(from, entry.Name()), filepath.Join(to, entry.Name())
		if entry.IsDir() {
			copyTree(t, src, dst)
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runWithLibrary(t *testing.T, root, entry, buildDir string) string {
	t.Helper()
	cmd := exec.Command(cliBinary(t), "run", entry)
	cmd.Env = append(withoutLibrary(), libroot.EnvRoot+"="+root, "FANGO_BUILD_DIR="+buildDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("fango run: %v\n%s", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// The point of unembedding: a standard library edit takes effect on the next
// build, with the same compiler executable. The manifest used to skip bundled
// hashes because the compiler's own fingerprint stood in for them, and this is
// the property that replaced it.
func TestStdlibEditTakesEffectWithoutRebuildingTheCompiler(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles twice through the real CLI")
	}
	root := t.TempDir()
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	copyTree(t, filepath.Join(repo, "stdlib"), filepath.Join(root, "stdlib"))
	copyTree(t, filepath.Join(repo, "runtime"), filepath.Join(root, "runtime"))

	dir := t.TempDir()
	entry := filepath.Join(dir, "Main.fango")
	if err := os.WriteFile(entry, []byte("main = print (List.length [1, 2, 3])\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	build := t.TempDir()
	if got := runWithLibrary(t, root, entry, build); got != "3" {
		t.Fatalf("before the edit: got %q, want %q", got, "3")
	}

	list := filepath.Join(root, "stdlib", "List.fango")
	before, err := os.ReadFile(list)
	if err != nil {
		t.Fatal(err)
	}
	after := bytes.Replace(before, []byte("length values = lengthHelp values 0"), []byte("length values = lengthHelp values 100"), 1)
	if bytes.Equal(before, after) {
		t.Fatal("List.length is no longer written the way this test edits it")
	}
	if err := os.WriteFile(list, after, 0o644); err != nil {
		t.Fatal(err)
	}
	// The same executable, the same build directory: only the library moved.
	if got := runWithLibrary(t, root, entry, build); got != "103" {
		t.Fatalf("after the edit: got %q, want %q", got, "103")
	}
}
