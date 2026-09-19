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

func TestCacheFallbackIsSourceRootScopedAndCleaned(t *testing.T) {
	old := userCacheDir
	cacheBase := t.TempDir()
	userCacheDir = func() (string, error) { return cacheBase, nil }
	t.Cleanup(func() { userCacheDir = old })

	root := t.TempDir()
	first := filepath.Join(root, "Main.fango")
	second := filepath.Join(root, "Tool.fango")
	local, fallback, err := CacheDirs(first)
	if err != nil {
		t.Fatal(err)
	}
	_, secondFallback, err := CacheDirs(second)
	if err != nil {
		t.Fatal(err)
	}
	if fallback != secondFallback {
		t.Fatalf("same source root got distinct fallbacks: %q and %q", fallback, secondFallback)
	}
	legacy, err := LegacyCacheDir(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Dir(local), fallback, filepath.Dir(legacy)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "artifact"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Clean(first); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Dir(local), fallback, filepath.Dir(legacy)} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("clean left %q: %v", dir, err)
		}
	}
}

func TestBuildDirOverrideDoesNotRedirectCompilationCache(t *testing.T) {
	old := userCacheDir
	userCacheDir = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { userCacheDir = old })
	root := t.TempDir()
	entry := filepath.Join(root, "Main.fango")
	t.Setenv("FANGO_BUILD_DIR", filepath.Join(t.TempDir(), "generated"))
	local, _, err := CacheDirs(entry)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".fango", "cache")
	if local != want {
		t.Fatalf("cache dir = %q, want %q", local, want)
	}
}
