package compilecache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/modules"
)

func TestRoundTripAndInputValidation(t *testing.T) {
	d := t.TempDir()
	entry := filepath.Join(d, "Main.fango")
	source := []byte("main = 1\n")
	if err := os.WriteFile(entry, source, 0o644); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(source)
	inputs := []modules.ManifestEntry{{Module: "<entry>", Path: "Main.fango", SHA256: hex.EncodeToString(h[:])}}
	files := []codegen.File{{Path: "main.go", Data: []byte("package main\n")}}
	Store(entry, "test", inputs, files)
	got, manifest, ok := Load(entry, "test")
	if !ok || string(got[0].Data) != string(files[0].Data) || len(manifest) != 1 {
		t.Fatalf("cache miss or wrong artifact: %#v %#v %v", got, manifest, ok)
	}
	if err := os.WriteFile(entry, []byte("main = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Load(entry, "test"); ok {
		t.Fatal("changed source was a cache hit")
	}
}

func TestNewSidecarInvalidates(t *testing.T) {
	d := t.TempDir()
	entry := filepath.Join(d, "Main.fango")
	source := []byte("main = 1\n")
	if err := os.WriteFile(entry, source, 0o644); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(source)
	Store(entry, "sidecar", []modules.ManifestEntry{{Path: "Main.fango", SHA256: hex.EncodeToString(h[:])}}, nil)
	if err := os.WriteFile(filepath.Join(d, "Main.native.go"), []byte("package native\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Load(entry, "sidecar"); ok {
		t.Fatal("new native sidecar was a cache hit")
	}
}

func TestCorruptArtifactIsMiss(t *testing.T) {
	d := t.TempDir()
	entry := filepath.Join(d, "Main.fango")
	if err := os.WriteFile(entry, []byte("main = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := path(entry, "corrupt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Load(entry, "corrupt"); ok {
		t.Fatal("corrupt artifact was a cache hit")
	}
}
