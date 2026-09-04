package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/codegen"
)

func TestSyncGeneratedPrunesOnlyManagedSources(t *testing.T) {
	dir := t.TempDir()
	userFile := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(userFile, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := []codegen.File{
		{Path: "main.go", Data: []byte("package main\n")},
		{Path: "modules/A/module.go", Data: []byte("package fangomod\n")},
	}
	if changed, err := SyncGenerated(dir, first); err != nil || !changed {
		t.Fatalf("first sync changed=%v err=%v", changed, err)
	}
	if changed, err := SyncGenerated(dir, first); err != nil || changed {
		t.Fatalf("unchanged sync changed=%v err=%v", changed, err)
	}
	if changed, err := SyncGenerated(dir, first[:1]); err != nil || !changed {
		t.Fatalf("pruning sync changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "modules", "A", "module.go")); !os.IsNotExist(err) {
		t.Fatalf("stale module remains: %v", err)
	}
	if data, err := os.ReadFile(userFile); err != nil || string(data) != "keep" {
		t.Fatalf("user file changed: %q, %v", data, err)
	}
}

func TestSyncGeneratedRejectsUnsafePaths(t *testing.T) {
	if _, err := SyncGenerated(t.TempDir(), []codegen.File{{Path: "../outside.go", Data: []byte("x")}}); err == nil {
		t.Fatal("unsafe generated path accepted")
	}
}

func TestValidateExportDirRejectsUnmanagedContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateExportDir(dir); err == nil {
		t.Fatal("non-empty unmanaged directory accepted")
	}
}
