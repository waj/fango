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

// DumpErrors renders diagnostics for inclusion in golden files.
func DumpErrors(errs []diag.Error) string {
	var b strings.Builder
	diag.Render(&b, errs)
	return b.String()
}
