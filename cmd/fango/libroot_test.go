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
