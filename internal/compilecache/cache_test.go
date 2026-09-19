package compilecache

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	entry := filepath.Join(d, "Main.fango")
	if err := os.WriteFile(entry, []byte("main = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return entry
}

func mustFingerprint(t *testing.T) string {
	t.Helper()
	fp, err := compilerFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func rootedAt(t *testing.T, local, fallback string) {
	t.Helper()
	old := cacheRoots
	t.Cleanup(func() { cacheRoots = old })
	cacheRoots = func(string) (string, string, error) { return local, fallback, nil }
}

func TestStoresRoundTripUnderTheCompilerNamespace(t *testing.T) {
	entry := fixture(t)
	local := filepath.Join(t.TempDir(), "local")
	rootedAt(t, local, "")
	hash := strings.Repeat("ab", 32)
	NewParsedStore(entry).StoreParsed(hash, []byte("unit"))
	NewModuleStore(entry).StoreObject(hash, []byte("object"))
	NewEmissionStore(entry).Store(hash, []byte("emitted"))
	if got, ok := NewParsedStore(entry).LoadParsed(hash); !ok || string(got) != "unit" {
		t.Fatalf("parsed = %q, %v", got, ok)
	}
	if got, ok := NewModuleStore(entry).LoadObject(hash); !ok || string(got) != "object" {
		t.Fatalf("object = %q, %v", got, ok)
	}
	if got, ok := NewEmissionStore(entry).Load(hash); !ok || string(got) != "emitted" {
		t.Fatalf("emitted = %q, %v", got, ok)
	}
	// Every artifact kind lives beneath the running compiler's namespace, so
	// a different compiler starts cold rather than reading these.
	namespace := filepath.Join(local, "v1", mustFingerprint(t))
	for _, kind := range []string{"parsed", "checked", "emitted"} {
		if _, err := os.Stat(filepath.Join(namespace, kind)); err != nil {
			t.Fatalf("%s artifacts are not under the compiler namespace: %v", kind, err)
		}
	}
}

func TestMalformedKeysAreRejected(t *testing.T) {
	entry := fixture(t)
	local := filepath.Join(t.TempDir(), "local")
	rootedAt(t, local, "")
	for _, key := range []string{"", "zz", strings.Repeat("ab", 31), "../escape"} {
		NewParsedStore(entry).StoreParsed(key, []byte("unit"))
		NewModuleStore(entry).StoreObject(key, []byte("object"))
		NewEmissionStore(entry).Store(key, []byte("emitted"))
		if _, ok := NewModuleStore(entry).LoadObject(key); ok {
			t.Fatalf("key %q was accepted", key)
		}
		if _, ok := NewEmissionStore(entry).Load(key); ok {
			t.Fatalf("key %q was accepted", key)
		}
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("a rejected key created storage: %v", err)
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
	entry := fixture(t)
	if _, ok := NewModuleStore(entry).LoadObject(strings.Repeat("cd", 32)); ok {
		t.Fatal("unexpected hit")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(entry), ".fango")); !os.IsNotExist(err) {
		t.Fatalf("cache read created .fango: %v", err)
	}
}

func TestStorageFallsBackWhenLocalIsUnwritable(t *testing.T) {
	entry := fixture(t)
	local := filepath.Join(t.TempDir(), "local")
	fallback := filepath.Join(t.TempDir(), "fallback")
	rootedAt(t, local, fallback)
	if err := os.MkdirAll(local, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(local, 0o755) })
	key := strings.Repeat("ef", 32)
	NewModuleStore(entry).StoreObject(key, []byte("object"))
	if got, ok := NewModuleStore(entry).LoadObject(key); !ok || string(got) != "object" {
		t.Fatalf("fallback artifact was not readable: %q, %v", got, ok)
	}
	if _, err := os.Stat(filepath.Join(fallback, "v1", mustFingerprint(t), "checked", key+".json")); err != nil {
		t.Fatalf("artifact did not land in the fallback namespace: %v", err)
	}
}

func TestModuleStoreKeepsImmutableCandidates(t *testing.T) {
	entry := fixture(t)
	rootedAt(t, filepath.Join(t.TempDir(), "module-cache"), "")
	store := NewModuleStore(entry)
	base := strings.Repeat("ab", 32)
	object := strings.Repeat("cd", 32)
	store.StoreCandidate(base, []byte("first"))
	store.StoreCandidate(base, []byte("second"))
	store.StoreCandidate(base, []byte("first"))
	candidates := store.LoadCandidates(base)
	if len(candidates) != 2 {
		t.Fatalf("candidates = %q", candidates)
	}
	store.StoreObject(object, []byte("payload"))
	if got, ok := store.LoadObject(object); !ok || string(got) != "payload" {
		t.Fatalf("object = %q, %v", got, ok)
	}
}
