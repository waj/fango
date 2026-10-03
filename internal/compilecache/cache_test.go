package compilecache

import (
	"errors"
	"os"
	"path/filepath"
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
	slot := Slot(false, "String")
	NewModuleStore(entry).StoreObject(slot, []byte("object"))
	NewEmissionStore(entry).Store(slot, []byte("emitted"))
	if got, ok := NewModuleStore(entry).LoadObject(slot); !ok || string(got) != "object" {
		t.Fatalf("object = %q, %v", got, ok)
	}
	if got, ok := NewEmissionStore(entry).Load(slot); !ok || string(got) != "emitted" {
		t.Fatalf("emitted = %q, %v", got, ok)
	}
	// Every artifact kind lives beneath the running compiler's namespace, so
	// a different compiler starts cold rather than reading these.
	namespace := filepath.Join(local, "v1", mustFingerprint(t))
	for _, kind := range []string{"checked", "emitted"} {
		if _, err := os.Stat(filepath.Join(namespace, kind)); err != nil {
			t.Fatalf("%s artifacts are not under the compiler namespace: %v", kind, err)
		}
	}
}

// A slot is built from a module name, so the store is what stands between a
// name it cannot spell and the filesystem.
func TestMalformedSlotsAreRejected(t *testing.T) {
	entry := fixture(t)
	local := filepath.Join(t.TempDir(), "local")
	rootedAt(t, local, "")
	for _, slot := range []string{"", "String", "module", "module/", "module/a/b", "other/String",
		"module/../escape", "module/.", "module/..", "module/with space", "module/with/slash", "module/2Digits"} {
		NewModuleStore(entry).StoreObject(slot, []byte("object"))
		NewEmissionStore(entry).Store(slot, []byte("emitted"))
		if _, ok := NewModuleStore(entry).LoadObject(slot); ok {
			t.Fatalf("slot %q was accepted", slot)
		}
		if _, ok := NewEmissionStore(entry).Load(slot); ok {
			t.Fatalf("slot %q was accepted", slot)
		}
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("a rejected slot created storage: %v", err)
	}
}

// An entry and a dependency can answer to the same name — a headerless
// String.fango importing the stdlib String — and must not share a slot.
func TestEntryAndModuleSlotsAreDistinct(t *testing.T) {
	entry := fixture(t)
	rootedAt(t, filepath.Join(t.TempDir(), "local"), "")
	NewModuleStore(entry).StoreObject(Slot(true, "String"), []byte("the entry"))
	NewModuleStore(entry).StoreObject(Slot(false, "String"), []byte("the module"))
	if got, ok := NewModuleStore(entry).LoadObject(Slot(true, "String")); !ok || string(got) != "the entry" {
		t.Fatalf("entry slot = %q, %v", got, ok)
	}
}

// Storing a module twice replaces its artifact: the cache keeps what a module
// is now, not what it has been.
func TestStoringASlotTwiceLeavesOneArtifact(t *testing.T) {
	entry := fixture(t)
	local := filepath.Join(t.TempDir(), "local")
	rootedAt(t, local, "")
	slot := Slot(false, "List")
	NewModuleStore(entry).StoreObject(slot, []byte("first"))
	NewModuleStore(entry).StoreObject(slot, []byte("second"))
	if got, ok := NewModuleStore(entry).LoadObject(slot); !ok || string(got) != "second" {
		t.Fatalf("object = %q, %v", got, ok)
	}
	entries, err := os.ReadDir(filepath.Join(local, "v1", mustFingerprint(t), "checked", "module"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("a rewritten slot left %d artifacts", len(entries))
	}
}

func TestFingerprintFailureIsSticky(t *testing.T) {
	oldFingerprint, oldExecutable, oldFiles := fingerprint, executablePath, cacheFiles
	t.Cleanup(func() { fingerprint, executablePath, cacheFiles = oldFingerprint, oldExecutable, oldFiles })
	fingerprint = &fingerprintResult{}
	executablePath = func() (string, error) { return "/not/an/executable", nil }
	calls := 0
	cacheFiles.readHead = func(string) ([]byte, error) { return []byte("no build ID here"), nil }
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

// The fingerprint reads the toolchain's build ID when the executable has one
// and hashes the whole file only when it does not.
func TestFingerprintPrefersTheGoBuildID(t *testing.T) {
	head := []byte("\x00\x01\xff Go build ID: \"aaa/bbb/ccc/ddd\"\n \xff\x00")
	if id, ok := goBuildID(head); !ok || id != "aaa/bbb/ccc/ddd" {
		t.Fatalf("goBuildID = %q, %v", id, ok)
	}
	for _, missing := range []string{"", "\xff Go build ID: \"\"\n \xff", "\xff Go build ID: \"a//b\"\n \xff", "\xff Go build ID: \"unterminated"} {
		if id, ok := goBuildID([]byte(missing)); ok {
			t.Fatalf("goBuildID(%q) = %q, want none", missing, id)
		}
	}
	oldFingerprint, oldExecutable, oldFiles := fingerprint, executablePath, cacheFiles
	t.Cleanup(func() { fingerprint, executablePath, cacheFiles = oldFingerprint, oldExecutable, oldFiles })
	executablePath = func() (string, error) { return "/not/an/executable", nil }
	cacheFiles.readHead = func(string) ([]byte, error) { return head, nil }
	cacheFiles.readFile = func(string) ([]byte, error) { return nil, errors.New("read the whole executable") }
	fingerprint = &fingerprintResult{}
	byID, err := compilerFingerprint()
	if err != nil || byID == "" {
		t.Fatalf("fingerprint from build ID = %q, %v", byID, err)
	}
	cacheFiles.readHead = func(string) ([]byte, error) { return []byte("\xff Go build ID: \"aaa/bbb/ccc/eee\"\n \xff"), nil }
	fingerprint = &fingerprintResult{}
	if other, err := compilerFingerprint(); err != nil || other == byID {
		t.Fatalf("a different build ID kept the fingerprint %q (%v)", other, err)
	}
}

func TestReadDoesNotCreateCacheDirectories(t *testing.T) {
	entry := fixture(t)
	if _, ok := NewModuleStore(entry).LoadObject(Slot(false, "Maybe")); ok {
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
	slot := Slot(false, "Result")
	NewModuleStore(entry).StoreObject(slot, []byte("object"))
	if got, ok := NewModuleStore(entry).LoadObject(slot); !ok || string(got) != "object" {
		t.Fatalf("fallback artifact was not readable: %q, %v", got, ok)
	}
	if _, err := os.Stat(filepath.Join(fallback, "v1", mustFingerprint(t), "checked", "module", "Result.json")); err != nil {
		t.Fatalf("artifact did not land in the fallback namespace: %v", err)
	}
}
