package backend

import (
	"github.com/waj/fango/internal/compileevent"
	"os"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/codegen"
)

type objectCache struct {
	candidates map[string][][]byte
	objects    map[string][]byte
}

func newObjectCache() *objectCache {
	return &objectCache{candidates: map[string][][]byte{}, objects: map[string][]byte{}}
}

func (c *objectCache) LoadCandidates(key string) [][]byte { return c.candidates[key] }
func (c *objectCache) StoreCandidate(key string, data []byte) {
	for _, old := range c.candidates[key] {
		if string(old) == string(data) {
			return
		}
	}
	c.candidates[key] = append(c.candidates[key], append([]byte(nil), data...))
}
func (c *objectCache) LoadObject(key string) ([]byte, bool) {
	data, ok := c.objects[key]
	return data, ok
}
func (c *objectCache) StoreObject(key string, data []byte) {
	c.objects[key] = append([]byte(nil), data...)
}

// project drives the module pipeline directly: no whole-project shortcut can
// stand in for a module-level result.
type project struct {
	dir      string
	entry    string
	objects  *objectCache
	emitted  *memoryCache
	events   map[string]map[string]int
	printOut bool
}

func newProject(t *testing.T) *project {
	t.Helper()
	return &project{dir: t.TempDir(), objects: newObjectCache(), emitted: newMemoryCache()}
}

func (p *project) write(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(p.dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func (p *project) record(event compileevent.Event) {
	if event.Begin {
		return
	}
	if p.events[event.Stage] == nil {
		p.events[event.Stage] = map[string]int{}
	}
	p.events[event.Stage][event.Owner]++
}

func (p *project) check(t *testing.T) *check.Result {
	t.Helper()
	p.events = map[string]map[string]int{}
	result, diagnostics, internalErr := (&check.Session{Cache: p.objects, Observe: p.record}).Compile(p.entry)
	if internalErr != nil {
		t.Fatal(internalErr)
	}
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	return result
}

func (p *project) build(t *testing.T) []codegen.File {
	t.Helper()
	result := p.check(t)
	files, err := (&Session{Cache: p.emitted, Observe: p.record}).EmitProject(p.entry, result, unitsOf(result), false)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func (p *project) total(stages ...string) int {
	n := 0
	for _, stage := range stages {
		for _, count := range p.events[stage] {
			n += count
		}
	}
	return n
}

// compilerWork counts the per-module work artifacts can eliminate. Parsing and
// resolution are not in it: every command rediscovers and revalidates the graph,
// so they run on every build by design.
func (p *project) compilerWork() int {
	return p.total("check", "elaborate", "semantic-lint", "lowering", "emission")
}

func libraryProject(t *testing.T) *project {
	p := newProject(t)
	p.write(t, "Lib.fango", "{-# no-prelude #-}\nmodule Lib exposing (value, twice)\nvalue = \"one\"\ntwice x = x\n")
	p.entry = p.write(t, "Main.fango", "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.twice Lib.value\n")
	return p
}

func TestUnchangedBuildDoesNoModuleWork(t *testing.T) {
	p := libraryProject(t)
	first := p.build(t)
	second := p.build(t)
	assertSameFiles(t, "rebuild", second, first)
	if work := p.compilerWork(); work != 0 {
		t.Fatalf("unchanged build did %d units of compiler work: %#v", work, p.events)
	}
	if p.total("checked-cache-hit") == 0 || p.total("emitted-cache-hit") != len(second) {
		t.Fatalf("unchanged build was not served from artifacts: %#v", p.events)
	}
}

func TestCheckThenBuildRunsOnlyBackendWork(t *testing.T) {
	p := libraryProject(t)
	p.check(t)
	if p.total("check") == 0 {
		t.Fatalf("first check did no checking: %#v", p.events)
	}
	files := p.build(t)
	if p.total("check", "elaborate", "semantic-lint") != 0 {
		t.Fatalf("build repeated semantic work: %#v", p.events)
	}
	if p.total("lowering") != len(files) || p.total("emission") != len(files) {
		t.Fatalf("build did not run exactly the missing backend work: %#v", p.events)
	}
}

func TestMissingEmissionArtifactReusesCheckedObjects(t *testing.T) {
	p := libraryProject(t)
	first := p.build(t)
	p.emitted = newMemoryCache()
	second := p.build(t)
	assertSameFiles(t, "re-emission", second, first)
	if p.total("check", "elaborate", "semantic-lint") != 0 {
		t.Fatalf("a missing emission artifact rechecked modules: %#v", p.events)
	}
	if p.total("emission") != len(second) {
		t.Fatalf("re-emission did not cover every owner: %#v", p.events)
	}
}

func TestDependencyImplementationEditKeepsImporterEmission(t *testing.T) {
	p := libraryProject(t)
	p.build(t)
	p.write(t, "Lib.fango", "{-# no-prelude #-}\nmodule Lib exposing (value, twice)\nvalue = \"two\"\ntwice x = x\n")
	p.build(t)
	if p.events["emission"]["Lib"] != 1 {
		t.Fatalf("edited dependency was not re-emitted: %#v", p.events)
	}
	if p.events["emission"]["Main"] != 0 || p.events["emitted-cache-hit"]["Main"] != 1 {
		t.Fatalf("runtime-only importer was re-emitted: %#v", p.events)
	}
}

func TestExportedContractChangeReEmitsImporter(t *testing.T) {
	p := libraryProject(t)
	p.build(t)
	p.write(t, "Lib.fango", "{-# no-prelude #-}\nmodule Lib exposing (Box(..), value, twice)\ntype Box = Box String\nvalue = Box \"one\"\ntwice x = x\n")
	p.build(t)
	if p.events["emission"]["Lib"] != 1 || p.events["emission"]["Main"] != 1 {
		t.Fatalf("an exported type change did not re-emit its consumer: %#v", p.events)
	}
}

func TestNativeSidecarEditRebuildsOwnerOnly(t *testing.T) {
	p := newProject(t)
	p.write(t, "Lib.fango", "{-# no-prelude #-}\nmodule Lib exposing (tag)\ntag : String -> String\ntag = native\n")
	p.write(t, "Lib.native.go", "package native\n\nfunc Tag(s string) string { return s }\n")
	p.entry = p.write(t, "Main.fango", "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.tag \"x\"\n")
	p.build(t)
	p.write(t, "Lib.native.go", "package native\n\nfunc Tag(s string) string { return s + \"!\" }\n")
	p.build(t)
	if p.events["check"]["Lib"] != 1 || p.events["checked-cache-hit"]["Lib"] != 0 {
		t.Fatalf("sidecar edit did not revalidate its owner: %#v", p.events)
	}
	// The sidecar is materialized as its own package from current bytes; no
	// Fango-facing contract moved, so neither generated module changes.
	if p.events["checked-cache-hit"]["Main"] != 1 || p.total("emission") != 0 {
		t.Fatalf("sidecar edit disturbed unchanged generated modules: %#v", p.events)
	}
}

func TestCorruptEmissionArtifactFallsBackToEmitting(t *testing.T) {
	p := libraryProject(t)
	first := p.build(t)
	for key := range p.emitted.entries {
		p.emitted.entries[key] = []byte("corrupt")
	}
	second := p.build(t)
	assertSameFiles(t, "corrupt artifact", second, first)
	if p.total("emitted-cache-hit") != 0 || p.total("emission") != len(second) {
		t.Fatalf("a corrupt artifact was not a miss: %#v", p.events)
	}
}

// Emission reads a dependency's contract, never its implementation. The unit
// program is the boundary: no imported body reaches lowering or emission.
func TestUnitProgramWithholdsImportedBodies(t *testing.T) {
	p := libraryProject(t)
	result := p.check(t)
	units := unitsOf(result)
	saw := map[string]bool{}
	for _, unit := range units {
		unitProg := codegen.UnitProgram(result.Program, unit)
		owned := 0
		for _, def := range unitProg.Defs {
			if def.Owner == unit.Name {
				owned++
				continue
			}
			if def.Body != nil {
				t.Fatalf("unit %q can read %q's body", unit.Name, def.Name)
			}
			saw[def.Owner] = true
		}
		if owned == 0 && unit.Name == "Lib" {
			t.Fatalf("unit %q has no definitions of its own", unit.Name)
		}
	}
	if !saw["Lib"] {
		t.Fatal("no imported definition was withheld; the test proves nothing")
	}
}

// A module reused from its checked object must lower and emit to the same
// bytes as one checked from source. Nothing downstream distinguishes the two,
// so a difference here is a decoded object that no longer describes what it
// described when it was written — and the emission cache, which serves the
// first run's bytes, is exactly what would hide it.
func TestEmittingFromAReusedObjectMatchesEmittingFromSource(t *testing.T) {
	p := newProject(t)
	// The fixture is the shape that matters: effect rows deferred into a
	// lambda, whose identities the artifact records and installation remaps.
	p.entry = filepath.Join("..", "..", "testdata", "run", "stream_file.fango")
	fromSource := p.build(t)
	// The objects stay; only the emitted artifacts go, so the second build
	// installs every module from cache and does the backend work again.
	p.emitted = newMemoryCache()
	fromObjects := p.build(t)
	if p.total("check") != 0 {
		t.Fatalf("the second build checked from source: %#v", p.events)
	}
	if p.events["emission"]["<entry>"] != 1 {
		t.Fatalf("the second build did not re-emit the entry: %#v", p.events)
	}
	assertSameFiles(t, "emission from a reused object", fromObjects, fromSource)
}
