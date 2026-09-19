package compilecache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/modules"
)

func fixture(t *testing.T) (string, []modules.ManifestEntry) {
	t.Helper()
	d := t.TempDir()
	entry := filepath.Join(d, "Main.fango")
	source := []byte("main = 1\n")
	if err := os.WriteFile(entry, source, 0o644); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(source)
	return entry, []modules.ManifestEntry{{Module: "<entry>", Path: "Main.fango", SHA256: hex.EncodeToString(h[:])}}
}

func localPath(t *testing.T, entry, mode string) string {
	t.Helper()
	ps, err := paths(entry, mode)
	if err != nil {
		t.Fatal(err)
	}
	return ps[0]
}

func TestRoundTripAndInputValidation(t *testing.T) {
	entry, inputs := fixture(t)
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

func TestSidecarAdditionAndRemovalInvalidate(t *testing.T) {
	entry, inputs := fixture(t)
	Store(entry, "sidecar-add", inputs, nil)
	native := filepath.Join(filepath.Dir(entry), "Main.native.go")
	content := []byte("package native\n")
	if err := os.WriteFile(native, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Load(entry, "sidecar-add"); ok {
		t.Fatal("new native sidecar was a cache hit")
	}
	h := sha256.Sum256(content)
	withNative := append(append([]modules.ManifestEntry(nil), inputs...), modules.ManifestEntry{Module: "<entry>", Path: "Main.native.go", SHA256: hex.EncodeToString(h[:])})
	Store(entry, "sidecar-remove", withNative, nil)
	if err := os.Remove(native); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Load(entry, "sidecar-remove"); ok {
		t.Fatal("removed native sidecar was a cache hit")
	}
}

func TestMalformedAndDamagedArtifactsAreMisses(t *testing.T) {
	entry, inputs := fixture(t)
	for _, tc := range []struct {
		name, data string
	}{
		{"empty object", `{}`},
		{"truncated", `{truncated`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := localPath(t, entry, tc.name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(tc.data), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, ok := Load(entry, tc.name); ok {
				t.Fatal("malformed artifact was a cache hit")
			}
		})
	}

	Store(entry, "damaged", inputs, []codegen.File{{Path: "main.go", Data: []byte("package main\n")}})
	p := localPath(t, entry, "damaged")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var a artifact
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	a.Payload.Files[0].Data[0] ^= 1
	b, _ = json.Marshal(a) // Deliberately retain the old payload digest.
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Load(entry, "damaged"); ok {
		t.Fatal("damaged payload was a cache hit")
	}
}

func TestInvalidEmissionStructureIsMiss(t *testing.T) {
	entry, inputs := fixture(t)
	p := payload{Entry: mustAbs(t, entry), Mode: "emit:print-main=false", Inputs: inputs, Files: []codegen.File{{Path: "../main.go", Data: []byte("x")}}}
	digest, _ := payloadHash(p)
	b, _ := json.Marshal(artifact{Schema: schema, Kind: artifactKind, PayloadSHA256: digest, Payload: p})
	path := localPath(t, entry, p.Mode)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Load(entry, p.Mode); ok {
		t.Fatal("invalid emitted path was a cache hit")
	}
}

func TestMissingEntryInputIsMiss(t *testing.T) {
	entry, _ := fixture(t)
	p := payload{Entry: mustAbs(t, entry), Mode: "check", Inputs: []modules.ManifestEntry{}, Files: []codegen.File{}}
	digest, _ := payloadHash(p)
	b, _ := json.Marshal(artifact{Schema: schema, Kind: artifactKind, PayloadSHA256: digest, Payload: p})
	path := localPath(t, entry, p.Mode)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Load(entry, p.Mode); ok {
		t.Fatal("artifact without its entry input was a cache hit")
	}
}

func TestFingerprintFailureIsSticky(t *testing.T) {
	oldFingerprint, oldExecutable, oldFiles := fingerprint, executablePath, cacheFiles
	t.Cleanup(func() { fingerprint, executablePath, cacheFiles = oldFingerprint, oldExecutable, oldFiles })
	fingerprint = &fingerprintResult{}
	executablePath = func() (string, error) { return "/not/an/executable", nil }
	calls := 0
	cacheFiles.readFile = func(string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("injected read failure")
		}
		return []byte("later success"), nil
	}
	if value, err := compilerFingerprint(); err == nil || value != "" {
		t.Fatalf("first fingerprint = %q, %v", value, err)
	}
	if value, err := compilerFingerprint(); err == nil || value != "" || calls != 1 {
		t.Fatalf("cached fingerprint failure = %q, %v; reads=%d", value, err, calls)
	}
}

func TestReadDoesNotCreateCacheDirectories(t *testing.T) {
	entry, _ := fixture(t)
	if _, _, ok := Load(entry, "absent"); ok {
		t.Fatal("unexpected hit")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(entry), ".fango")); !os.IsNotExist(err) {
		t.Fatalf("cache read created .fango: %v", err)
	}
}

func TestStoreFallsBackWhenLocalStorageFails(t *testing.T) {
	entry, inputs := fixture(t)
	local := filepath.Join(t.TempDir(), "local")
	fallback := filepath.Join(t.TempDir(), "fallback")
	oldRoots, oldFiles := cacheRoots, cacheFiles
	t.Cleanup(func() { cacheRoots, cacheFiles = oldRoots, oldFiles })
	cacheRoots = func(string) (string, string, error) { return local, fallback, nil }
	localNamespace := filepath.Join(local, "v1", mustFingerprint(t))
	if err := os.MkdirAll(localNamespace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(localNamespace, 0o555); err != nil {
		t.Fatal(err)
	}
	cacheFiles.createTemp = func(dir, pattern string) (*os.File, error) {
		if dir == localNamespace {
			return nil, errors.New("injected read-only local cache")
		}
		return os.CreateTemp(dir, pattern)
	}
	Store(entry, "fallback", inputs, nil)
	if _, _, ok := Load(entry, "fallback"); !ok {
		t.Fatal("fallback artifact was not readable")
	}
}

func TestConcurrentAtomicWriters(t *testing.T) {
	entry, inputs := fixture(t)
	files := []codegen.File{{Path: "main.go", Data: []byte("package main\n")}}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); Store(entry, "writers", inputs, files) }()
	}
	wg.Wait()
	if _, _, ok := Load(entry, "writers"); !ok {
		t.Fatal("concurrent writers did not leave a valid artifact")
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func mustFingerprint(t *testing.T) string {
	t.Helper()
	fp, err := compilerFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fp
}
