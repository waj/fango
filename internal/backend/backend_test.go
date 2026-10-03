package backend

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/codegen"
)

type memoryCache struct {
	mu      sync.Mutex
	entries map[string][]byte
	loads   int
}

func newMemoryCache() *memoryCache { return &memoryCache{entries: map[string][]byte{}} }

func (c *memoryCache) Load(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.entries[key]
	if ok {
		c.loads++
	}
	return append([]byte(nil), data...), ok
}

func (c *memoryCache) Store(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = append([]byte(nil), data...)
}

func unitsOf(result *check.Result) []codegen.Unit {
	units := make([]codegen.Unit, len(result.Graph.Units))
	for i, unit := range result.Graph.Units {
		units[i] = codegen.Unit{Name: unit.Name, Program: unit.Program, Imports: unit.Imports, Entry: unit.Entry}
	}
	return units
}

// referenceFiles is the whole-program path: one lowering and one emission over
// every definition, with dependency bodies available throughout.
func referenceFiles(t *testing.T, result *check.Result, units []codegen.Unit, printMain bool) []codegen.File {
	t.Helper()
	prog := result.Program
	files, err := codegen.EmitProject(prog, result.Checker.B, units, printMain)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func fileMap(files []codegen.File) map[string]string {
	out := map[string]string{}
	for _, file := range files {
		out[file.Path] = string(file.Data)
	}
	return out
}

func assertSameFiles(t *testing.T, label string, got, want []codegen.File) {
	t.Helper()
	gotMap, wantMap := fileMap(got), fileMap(want)
	if len(gotMap) != len(wantMap) {
		t.Fatalf("%s: emitted %d files, reference emitted %d", label, len(gotMap), len(wantMap))
	}
	paths := make([]string, 0, len(wantMap))
	for path := range wantMap {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if gotMap[path] != wantMap[path] {
			t.Fatalf("%s: %s differs from the whole-program reference:\n--- module backend ---\n%s\n--- reference ---\n%s",
				label, path, gotMap[path], wantMap[path])
		}
	}
}

func entryPaths(t *testing.T) []string {
	t.Helper()
	var entries []string
	for _, dir := range []string{"modules", "run"} {
		root := filepath.Join("..", "..", "testdata", dir)
		listing, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range listing {
			switch {
			case item.IsDir() && dir == "modules":
				entries = append(entries, filepath.Join(root, item.Name(), "Main.fango"))
			case !item.IsDir() && strings.HasSuffix(item.Name(), ".fango") && !strings.HasPrefix(item.Name(), "err_"):
				entries = append(entries, filepath.Join(root, item.Name()))
			}
		}
	}
	return entries
}

// The module backend never lowers or emits a whole program and never reads a
// dependency body, so this equivalence is the proof that installed headers
// carry every backend-relevant fact.
func TestModuleBackendMatchesWholeProgramEmission(t *testing.T) {
	t.Parallel()
	for _, entry := range entryPaths(t) {
		entry := entry
		t.Run(filepath.Base(filepath.Dir(entry))+"/"+filepath.Base(entry), func(t *testing.T) {
			t.Parallel()
			if _, err := os.Stat(entry); err != nil {
				t.Skip("no entry")
			}
			result, diagnostics, internalErr := (&check.Session{DisableObjectCache: true}).Compile(entry)
			if internalErr != nil || len(diagnostics) != 0 {
				t.Skip("entry does not compile on its own")
			}
			units := unitsOf(result)
			want := referenceFiles(t, result, units, false)
			got, err := (&Session{DisableCache: true}).EmitProject(entry, result, units, false)
			if err != nil {
				t.Fatal(err)
			}
			assertSameFiles(t, entry, got, want)
		})
	}
}
