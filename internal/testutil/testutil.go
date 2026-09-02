// Package testutil holds the shared golden-file helper. Run any test
// package with -update to rewrite its golden files from current output.
package testutil

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/diag"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Golden compares got against the file at path, rewriting it under -update.
func Golden(t *testing.T, path string, got string) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (run with -update to create): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

// GlobFango lists the *.fango source files under a testdata directory,
// skipping directories and dot-entries — a stray `.fango/` build directory
// matches the glob otherwise.
func GlobFango(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.fango"))
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, m := range matches {
		base := filepath.Base(m)
		if strings.HasPrefix(base, ".") {
			continue
		}
		if info, err := os.Stat(m); err != nil || info.IsDir() {
			continue
		}
		files = append(files, m)
	}
	if len(files) == 0 {
		t.Fatalf("no *.fango files in %s", dir)
	}
	return files
}

// DumpErrors renders diagnostics for inclusion in golden files.
func DumpErrors(errs []diag.Error) string {
	var b strings.Builder
	diag.Render(&b, errs)
	return b.String()
}
