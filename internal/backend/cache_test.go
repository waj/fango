package backend

import (
	"github.com/waj/fango/internal/compileevent"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/codegen"
)

type objectCache struct {
	objects map[string][]byte
}

func newObjectCache() *objectCache {
	return &objectCache{objects: map[string][]byte{}}
}

func (c *objectCache) LoadObject(slot string) ([]byte, bool) {
	data, ok := c.objects[slot]
	return data, ok
}
func (c *objectCache) StoreObject(slot string, data []byte) {
	c.objects[slot] = append([]byte(nil), data...)
}

// project drives the module pipeline directly: no whole-project shortcut can
// stand in for a module-level result.
type project struct {
	dir      string
	entry    string
	objects  *objectCache
	emitted  *memoryCache
	tree     memoryTree
	events   map[string]map[string]int
	printOut bool
}

// memoryTree stands in for the build directory, which is where the generated
// Go an emission artifact describes actually lives: a build reads what is
// there and the driver writes back what came out.
type memoryTree map[string][]byte

func (t memoryTree) Source(path string) ([]byte, bool) {
	data, ok := t[path]
	return append([]byte(nil), data...), ok
}

func newProject(t *testing.T) *project {
	t.Helper()
	return &project{dir: t.TempDir(), objects: newObjectCache(), emitted: newMemoryCache(), tree: memoryTree{}}
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
	files, err := (&Session{Cache: p.emitted, Emitted: p.tree, Observe: p.record}).EmitProject(p.entry, result, unitsOf(result), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		p.tree[file.Path] = append([]byte(nil), file.Data...)
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
// first run's bytes, is exactly what would hide it. The fixtures are the
// shapes that carry identities an artifact has to restate: effect rows
// deferred into a lambda, and resumes a handler clause names.
func TestEmittingFromAReusedObjectMatchesEmittingFromSource(t *testing.T) {
	for _, fixture := range []string{"stream_file.fango", "abort_effects.fango", "state_handlers.fango"} {
		t.Run(fixture, func(t *testing.T) {
			p := newProject(t)
			p.entry = filepath.Join("..", "..", "testdata", "run", fixture)
			fromSource := p.build(t)
			// The objects stay; only the emitted artifacts go, so the second
			// build installs every module from cache and does the backend work
			// again.
			p.emitted = newMemoryCache()
			fromObjects := p.build(t)
			if p.total("check") != 0 {
				t.Fatalf("the second build checked from source: %#v", p.events)
			}
			if p.events["emission"]["<entry>"] != 1 {
				t.Fatalf("the second build did not re-emit the entry: %#v", p.events)
			}
			assertSameFiles(t, "emission from a reused object", fromObjects, fromSource)
		})
	}
}

// An owner's bytes depend on what it can reach, not on what else happens to be
// in the program around it. Two entry programs in one directory therefore
// share a dependency's emitted unit, and adding an unrelated module to a
// program does not invalidate anything already emitted for it.
func TestUnrelatedModulesDoNotInvalidateEmittedUnits(t *testing.T) {
	p := newProject(t)
	p.write(t, "Shared.fango", "{-# no-prelude #-}\nmodule Shared exposing (value)\nvalue = \"shared\"\n")
	p.write(t, "Aside.fango", "{-# no-prelude #-}\nmodule Aside exposing (pick)\npick x = x\n")
	first := p.write(t, "First.fango", "{-# no-prelude #-}\nmodule First exposing (main)\nimport Shared\nmain = Shared.value\n")
	second := p.write(t, "Second.fango", "{-# no-prelude #-}\nmodule Second exposing (main)\nimport Shared\nimport Aside\nmain = Aside.pick Shared.value\n")

	p.entry = first
	p.build(t)
	p.entry = second
	p.build(t)
	if p.events["emitted-cache-hit"]["Shared"] != 1 || p.events["emission"]["Shared"] != 0 {
		t.Fatalf("a second program re-emitted a dependency it shares: %#v", p.events)
	}

	// Editing the module neither program's Shared can reach leaves both alone.
	p.write(t, "Aside.fango", "{-# no-prelude #-}\nmodule Aside exposing (pick)\npick x = again x\nagain y = y\n")
	p.build(t)
	if p.events["emission"]["Shared"] != 0 || p.events["emitted-cache-hit"]["Shared"] != 1 {
		t.Fatalf("an unreachable module's edit re-emitted Shared: %#v", p.events)
	}
	p.entry = first
	p.build(t)
	if p.total("emission") != 0 {
		t.Fatalf("the first program lost its emitted units to the second: %#v", p.events)
	}
}

// An object must describe the body it ships with whatever identities the
// installation it lands in happens to hand out. Building the second program
// installs Handlers into a graph the first program's Filler is absent from, so
// the identities it is given are not the ones its artifact was written with,
// and lowering is what notices if the two stop agreeing.
func TestReusedObjectsSurviveADifferentIdentityAllocation(t *testing.T) {
	p := newProject(t)
	p.write(t, "Handlers.fango", `module Handlers exposing (run)

effect Ask
    ask : Bool -> Int

run() =
    handle ask True + ask False of
        ask valid -> if valid then resume 5 else resume 0
        return total -> total
`)
	p.write(t, "Filler.fango", `module Filler exposing (filler)

effect Note
    note : Int -> Int

filler() =
    handle note 1 of
        note n -> resume n
        return total -> total
`)
	first := p.write(t, "First.fango", "module First exposing (main)\nimport Filler\nimport Handlers\nmain = Filler.filler() + Handlers.run()\n")
	second := p.write(t, "Second.fango", "module Second exposing (main)\nimport Handlers\nmain = Handlers.run()\n")

	p.entry = first
	p.build(t)
	p.entry = second
	p.emitted = newMemoryCache()
	p.build(t)
	if p.events["checked-cache-hit"]["Handlers"] != 1 {
		t.Fatalf("the second program did not reuse Handlers: %#v", p.events)
	}
	if p.events["emission"]["Handlers"] != 1 {
		t.Fatalf("the second program did not re-emit Handlers: %#v", p.events)
	}
}

// Lowering lints an owner's Core without discharging its obligations again,
// because the installer discharged them — or published them only after doing
// so — and nothing rewrites installed Core in between. That skip is conditional
// on lint agreeing with every contract it reconstructs, so a contract gone
// stale between install and lowering is still reported, from a fresh object
// and from a reused one alike, and the obligations are discharged anyway.
func TestLoweringReportsAStaleContractItWasToldWasProven(t *testing.T) {
	p := libraryProject(t)
	for _, run := range []string{"from source", "from cache"} {
		t.Run(run, func(t *testing.T) {
			result := p.check(t)
			if run == "from cache" && p.total("checked-cache-hit") == 0 {
				t.Fatal("no object was reused; the test proves nothing")
			}
			if !result.FlowsProven["Lib"] {
				t.Fatalf("Lib is not recorded as proven: %v", result.FlowsProven)
			}
			flows := func() (count int, err error) {
				observe := func(event compileevent.Event) {
					if event.Stage == "capture-flow" {
						count++
					}
				}
				_, err = (&Session{DisableCache: true, Observe: observe}).EmitProject(p.entry, result, unitsOf(result), false)
				return count, err
			}
			if count, err := flows(); err != nil || count != 0 {
				t.Fatalf("lowering proven Core: %d flow runs, err %v", count, err)
			}
			damaged := false
			for i := range result.Program.Defs {
				if result.Program.Defs[i].Name == "Lib.twice" {
					result.Program.Defs[i].CaptureContract = nil
					damaged = true
				}
			}
			if !damaged {
				t.Fatal("Lib.twice is not in the program")
			}
			count, err := flows()
			if err == nil || !strings.Contains(err.Error(), "capture contract is stale") {
				t.Fatalf("got %v, want a stale contract", err)
			}
			if count == 0 {
				t.Fatal("a stale contract did not discharge the obligations anyway")
			}
		})
	}
}
