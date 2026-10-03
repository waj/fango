package check

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/waj/fango/internal/compilecache"
	"github.com/waj/fango/internal/compileevent"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
)

type memoryObjectCache struct {
	objects map[string][]byte
}

func newMemoryObjectCache() *memoryObjectCache {
	return &memoryObjectCache{objects: map[string][]byte{}}
}

func (c *memoryObjectCache) LoadObject(slot string) ([]byte, bool) {
	b, ok := c.objects[slot]
	return append([]byte(nil), b...), ok
}
func (c *memoryObjectCache) StoreObject(slot string, data []byte) {
	c.objects[slot] = append([]byte(nil), data...)
}

func compileEvents(t *testing.T, entry string, cache ObjectCache) (*Result, map[string]map[string]int) {
	t.Helper()
	events := map[string]map[string]int{}
	s := &Session{Cache: cache, Observe: func(event compileevent.Event) {
		if event.Begin {
			// A stage announces its start and its completion; counting
			// work means counting completions.
			return
		}
		if events[event.Stage] == nil {
			events[event.Stage] = map[string]int{}
		}
		events[event.Stage][event.Owner]++
	}}
	result, diagnostics, internalErr := s.Compile(entry)
	if internalErr != nil {
		t.Fatal(internalErr)
	}
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	return result, events
}

func intDefinition(t *testing.T, result *Result, name string) int64 {
	t.Helper()
	for _, def := range result.Program.Defs {
		if def.Name == name {
			lit, ok := def.Body.(*core.IntLit)
			if !ok {
				t.Fatalf("%s body = %T", name, def.Body)
			}
			return lit.Val
		}
	}
	t.Fatalf("definition %s missing", name)
	return 0
}

func stringDefinition(t *testing.T, result *Result, name string) string {
	t.Helper()
	for _, def := range result.Program.Defs {
		if def.Name == name {
			lit, ok := def.Body.(*core.StringLit)
			if !ok {
				t.Fatalf("%s body = %T", name, def.Body)
			}
			return lit.Val
		}
	}
	t.Fatalf("definition %s missing", name)
	return ""
}

func TestCheckedCacheKeepsRuntimeOnlyImporterHit(t *testing.T) {
	d := t.TempDir()
	lib := filepath.Join(d, "Lib.fango")
	main := filepath.Join(d, "Main.fango")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(lib, "{-# no-prelude #-}\nmodule Lib exposing (value)\nvalue = \"one\"\n")
	write(main, "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.value\n")
	cache := newMemoryObjectCache()
	compileEvents(t, main, cache)
	write(lib, "{-# no-prelude #-}\nmodule Lib exposing (value)\nvalue = \"two\"\n")
	result, events := compileEvents(t, main, cache)
	if events["check"]["Lib"] != 1 || events["checked-cache-hit"]["Main"] != 1 || events["check"]["Main"] != 0 {
		t.Fatalf("events after runtime edit: %#v", events)
	}
	if got := stringDefinition(t, result, "Lib.value"); got != "two" {
		t.Fatalf("current dependency implementation = %q", got)
	}
}

// Main inlines Lib.bump, so a change to that body alone, with Lib's
// interface unchanged, must recheck Main rather than reuse the stale copy.
func TestCheckedCacheInvalidatesInlinedBodies(t *testing.T) {
	d := t.TempDir()
	lib := filepath.Join(d, "Lib.fango")
	main := filepath.Join(d, "Main.fango")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	library := func(step string) string {
		return "module Lib exposing (bump)\n\nbump : Int -> Int\nbump n = n + " + step + "\n"
	}
	write(lib, library("1"))
	write(main, "module Main exposing (main)\nimport Lib\n\nmain = print (Lib.bump 40)\n")
	cache := newMemoryObjectCache()
	first, _ := compileEvents(t, main, cache)
	mainBody := func(result *Result) string {
		for _, def := range result.Program.Defs {
			if def.Name == "Main.main" {
				return core.Dump(&core.Prog{Defs: []core.Def{def}})
			}
		}
		t.Fatal("Main.main missing")
		return ""
	}
	if body := mainBody(first); !strings.Contains(body, "(int 1 Int)") || strings.Contains(body, "Lib.bump") {
		t.Fatalf("Lib.bump was not inlined into Main:\n%s", body)
	}
	write(lib, library("2"))
	second, events := compileEvents(t, main, cache)
	if events["check"]["Main"] != 1 || events["checked-cache-hit"]["Main"] != 0 {
		t.Fatalf("an inlined body change reused Main's artifact: %#v", events)
	}
	if body := mainBody(second); !strings.Contains(body, "(int 2 Int)") {
		t.Fatalf("Main kept the stale inlined body:\n%s", body)
	}
}

func TestCheckedCacheInvalidatesTransitiveStageClosure(t *testing.T) {
	d := t.TempDir()
	lib := filepath.Join(d, "Lib.fango")
	relay := filepath.Join(d, "Relay.fango")
	main := filepath.Join(d, "Main.fango")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(lib, "{-# no-prelude #-}\n"+
		"module Lib exposing (make)\n"+
		"make = `1`\n")
	write(relay, "{-# no-prelude #-}\nmodule Relay exposing (make)\nimport Lib\nmake = Lib.make\n")
	write(main, "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Relay\nmain = $(Relay.make)\n")
	cache := newMemoryObjectCache()
	first, _ := compileEvents(t, main, cache)
	if got := intDefinition(t, first, "Main.main"); got != 1 {
		t.Fatalf("first splice = %d", got)
	}
	write(lib, "{-# no-prelude #-}\n"+
		"module Lib exposing (make)\n"+
		"make = `2`\n")
	second, events := compileEvents(t, main, cache)
	if events["checked-cache-hit"]["Relay"] != 1 || events["check"]["Main"] != 1 {
		t.Fatalf("events after stage edit: %#v", events)
	}
	if got := intDefinition(t, second, "Main.main"); got != 2 {
		t.Fatalf("rebuilt splice = %d", got)
	}
	// A module keeps one artifact, so restoring its earlier source recompiles
	// it rather than finding the artifact that source once had.
	write(lib, "{-# no-prelude #-}\n"+
		"module Lib exposing (make)\n"+
		"make = `1`\n")
	third, events := compileEvents(t, main, cache)
	if events["checked-cache-miss"]["Lib"] != 1 || events["check"]["Lib"] != 1 || events["check"]["Main"] != 1 {
		t.Fatalf("a restored source was served from a superseded artifact: %#v", events)
	}
	if got := intDefinition(t, third, "Main.main"); got != 1 {
		t.Fatalf("recompiled splice = %d", got)
	}
}

func TestCheckedCacheReusesReflectionDictionariesAndImportedDeriver(t *testing.T) {
	for _, fixture := range []string{"reflection", "deriver", "classes"} {
		t.Run(fixture, func(t *testing.T) {
			entry := filepath.Join("..", "..", "testdata", "modules", fixture, "Main.fango")
			cache := newMemoryObjectCache()
			first, _ := compileEvents(t, entry, cache)
			second, events := compileEvents(t, entry, cache)
			if len(first.Program.Defs) != len(second.Program.Defs) || len(events["check"]) != 0 {
				t.Fatalf("cached graph did semantic work: events=%#v defs=%d/%d", events, len(first.Program.Defs), len(second.Program.Defs))
			}
			if errs := core.Lint(second.Program, second.Checker.B); len(errs) != 0 {
				t.Fatalf("cached whole program: %v", errs[0])
			}
			for _, object := range second.Objects {
				owner := object.State.Name
				if owner == "" {
					owner = "<entry>"
				}
				if events["checked-cache-hit"][owner] != 1 {
					t.Fatalf("%s was not a hit: %#v", owner, events)
				}
			}
		})
	}
}

func TestCachedBranchesStillRejectIncompatibleInstances(t *testing.T) {
	d := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(d, name+".fango")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("Capability", "module Capability exposing (Mark(..))\nclass Mark a\n    mark : a -> String\n")
	write("Left", "module Left exposing (left)\nimport Capability\ninstance Capability.Mark Int\n    mark x = \"left\"\nleft = \"left\"\n")
	write("Right", "module Right exposing (right)\nimport Capability\ninstance Capability.Mark Int\n    mark x = \"right\"\nright = \"right\"\n")
	leftEntry := write("UseLeft", "module UseLeft exposing (main)\nimport Left\nmain = Left.left\n")
	rightEntry := write("UseRight", "module UseRight exposing (main)\nimport Right\nmain = Right.right\n")
	combined := write("Combined", "module Combined exposing (main)\nimport Left\nimport Right\nmain = Left.left ++ Right.right\n")
	cache := newMemoryObjectCache()
	compileEvents(t, leftEntry, cache)
	compileEvents(t, rightEntry, cache)
	result, diagnostics, internalErr := (&Session{Cache: cache}).Compile(combined)
	if internalErr != nil {
		t.Fatal(internalErr)
	}
	if result != nil || len(diagnostics) == 0 || diagnostics[0].Title != "OVERLAPPING INSTANCE" {
		t.Fatalf("result=%v diagnostics=%v", result, diagnostics)
	}
}

func TestCheckedCacheIgnoresDependencyCommentPositions(t *testing.T) {
	d := t.TempDir()
	lib := filepath.Join(d, "Lib.fango")
	main := filepath.Join(d, "Main.fango")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(lib, "{-# no-prelude #-}\n"+
		"module Lib exposing (make)\n"+
		"make = `\"same\"`\n")
	write(main, "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = $(Lib.make)\n")
	cache := newMemoryObjectCache()
	compileEvents(t, main, cache)
	write(lib, "{-# no-prelude #-}\n"+
		"module Lib exposing (make)\n"+
		"-- shifted source positions\n"+
		"make = `\"same\"`\n")
	_, events := compileEvents(t, main, cache)
	if events["check"]["Lib"] != 1 || events["checked-cache-hit"]["Main"] != 1 {
		t.Fatalf("comment edit invalidated downstream stage consumer: %#v", events)
	}
}

func TestCheckedCachePublishesDependenciesBeforeEntryFailure(t *testing.T) {
	d := t.TempDir()
	lib := filepath.Join(d, "Lib.fango")
	main := filepath.Join(d, "Main.fango")
	if err := os.WriteFile(lib, []byte("{-# no-prelude #-}\nmodule Lib exposing (value)\nvalue = \"ok\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main, []byte("{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = missing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := newMemoryObjectCache()
	if result, diagnostics, internalErr := (&Session{Cache: cache}).Compile(main); internalErr != nil || result != nil || len(diagnostics) == 0 {
		t.Fatalf("result=%v diagnostics=%v internal=%v", result, diagnostics, internalErr)
	}
	if err := os.WriteFile(main, []byte("{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, events := compileEvents(t, main, cache)
	if events["checked-cache-hit"]["Lib"] != 1 || events["check"]["Lib"] != 0 {
		t.Fatalf("valid dependency was not retained: %#v", events)
	}
}

func TestCorruptCheckedObjectFallsBackToChecking(t *testing.T) {
	d := t.TempDir()
	entry := filepath.Join(d, "Main.fango")
	if err := os.WriteFile(entry, []byte("{-# no-prelude #-}\nmodule Main exposing (main)\nmain = \"ok\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := newMemoryObjectCache()
	compileEvents(t, entry, cache)
	for key := range cache.objects {
		cache.objects[key] = []byte("corrupt")
	}
	_, events := compileEvents(t, entry, cache)
	if events["check"]["Main"] != 1 || events["checked-cache-hit"]["Main"] != 0 {
		t.Fatalf("corrupt object did not become a miss: %#v", events)
	}
}

func TestDependencyStateIsConsumerIndependent(t *testing.T) {
	d := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(d, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("Lib.fango", "{-# no-prelude #-}\nmodule Lib exposing (id, main)\nid x = x\nmain x y = x\n")
	one := write("One.fango", "{-# no-prelude #-}\nmodule One exposing (main)\nimport Lib\nmain = Lib.main (Lib.id ()) ()\n")
	two := write("Two.fango", "{-# no-prelude #-}\nmodule Two exposing (main)\nimport Lib\nmain = Lib.main () (Lib.id ())\n")
	compile := func(entry string) *Result {
		r, diagnostics, internalErr := (&Session{DisableObjectCache: true}).Compile(entry)
		if internalErr != nil {
			t.Fatal(internalErr)
		}
		if len(diagnostics) != 0 {
			t.Fatal(diagnostics)
		}
		return r
	}
	find := func(r *Result) any {
		for _, state := range r.States {
			if state.Name == "Lib" {
				if state.Schemes["Lib.id"].Body == nil || state.Workers["Lib.id"] != 1 {
					t.Fatalf("incomplete Lib state: %#v", state)
				}
				if _, ok := state.Captures["Lib.id"]; !ok {
					t.Fatalf("missing capture summary: %#v", state.Captures)
				}
				return state
			}
		}
		t.Fatal("Lib state missing")
		return nil
	}
	if a, b := find(compile(one)), find(compile(two)); !reflect.DeepEqual(a, b) {
		t.Fatal("dependency state changed between consumers")
	}
}

func TestInstalledStageCoreSupportsSplicesAndDeriving(t *testing.T) {
	d := t.TempDir()
	baseContent := []byte("{-# no-prelude #-}\n" +
		"module Base exposing (make)\n" +
		"import Basics exposing (Show(..))\n" +
		"type Seed = Seed deriving (Show)\n" +
		"identity code = `$(code)`\n" +
		"make = identity (`()`)\n")
	basePath := filepath.Join(d, "Base.fango")
	if err := os.WriteFile(basePath, baseContent, 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics, internalErr := (&Session{}).Compile(basePath)
	if internalErr != nil {
		t.Fatal(internalErr)
	}
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	sources := map[string]*source.File{}
	for _, module := range compiled.Graph.Modules {
		if module.Source != nil {
			sources[module.Source.Name] = source.NewFile(module.Source.Name, append([]byte(nil), module.Source.Content...))
		}
	}

	consumerContent := []byte(`{-# no-prelude #-}
module Consumer exposing (main)
import Basics exposing (Show(..))
import Base
type Next = Next deriving (Show)
main = $(Base.make)
`)
	consumerPath := filepath.Join(d, "Consumer.fango")
	if err := os.WriteFile(consumerPath, consumerContent, 0o644); err != nil {
		t.Fatal(err)
	}
	graph, loadErrs := modules.Load(consumerPath)
	if len(loadErrs) != 0 {
		t.Fatal(loadErrs)
	}
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	ck.Fixity, ck.EntryName = graph.Fixity, graph.Entry
	ck.Templates.Add(&meta.Template{}) // force cached template-index remapping
	stageSession := staging.Install(ck)
	var context []core.Def
	for _, object := range compiled.Objects {
		data, err := EncodeObject(object, nil)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeObject(data, sources)
		if err != nil {
			t.Fatalf("decode %s: %v", object.State.Name, err)
		}
		if err := InstallObject(ck, stageSession, decoded); err != nil {
			t.Fatalf("install %s: %v", object.State.Name, err)
		}
		context = append(context, decoded.Runtime...)
	}
	entry := graph.Modules[len(graph.Modules)-1]
	checked, inferErrs := ck.CheckModule(entry.Module, infer.ModuleOptions{Name: entry.Name, Role: infer.EntryModule, Entry: entry.Entry})
	if len(inferErrs) != 0 {
		t.Fatal(inferErrs)
	}
	if len(checked.State.Instances) == 0 {
		t.Fatal("cached deriver did not produce the consumer instance")
	}
	defs, elabErrs := elaborate.Increment(checked.Infos, checked.State.Instances, nil, context, ck)
	if len(elabErrs) != 0 {
		t.Fatal(elabErrs)
	}
	// false keeps this exercising the full reconstruction, obligations and
	// all, rather than the batch pipeline's shortcut.
	if lintErrs := elaborate.LintProgIn(defs, context, ck); len(lintErrs) != 0 {
		t.Fatal(lintErrs)
	}
}

func TestInstalledObjectSupportsSubsequentInference(t *testing.T) {
	d := t.TempDir()
	libContent := []byte("{-# no-prelude #-}\nmodule Lib exposing (Box(..), id)\ntype Box a = Box a\nid x = x\n")
	libPath := filepath.Join(d, "Lib.fango")
	if err := os.WriteFile(libPath, libContent, 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics, internalErr := (&Session{}).Compile(libPath)
	if internalErr != nil {
		t.Fatal(internalErr)
	}
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	data, err := EncodeObject(compiled.Objects[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeObject(data, map[string]*source.File{"Lib.fango": source.NewFile("Lib.fango", libContent)})
	if err != nil {
		t.Fatal(err)
	}
	originalUnique := decoded.State.ADTs[0].Con.Unique

	mainContent := []byte("{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.id (Lib.Box ())\n")
	mainPath := filepath.Join(d, "Main.fango")
	if err := os.WriteFile(mainPath, mainContent, 0o644); err != nil {
		t.Fatal(err)
	}
	graph, loadErrs := modules.Load(mainPath)
	if len(loadErrs) != 0 {
		t.Fatal(loadErrs)
	}
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	for i := 0; i < 9; i++ {
		sup.NextUnique()
		sup.FreshRigid(types.General)
		sup.FreshCapture()
		sup.FreshScope()
	}
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	ck.Fixity, ck.EntryName = graph.Fixity, graph.Entry
	stageSession := staging.Install(ck)
	if err := InstallObject(ck, stageSession, decoded); err != nil {
		t.Fatal(err)
	}
	installedADT := ck.TypeNames["Lib.Box"].(*types.TCon)
	if installedADT.Unique == originalUnique {
		t.Fatalf("nominal unique %d was not remapped", originalUnique)
	}
	if ck.ADTs[installedADT.Unique] == nil || !ck.Env.Has("Lib.id") {
		t.Fatal("module declaration state was not installed")
	}

	entry := graph.Modules[len(graph.Modules)-1]
	checked, inferErrs := ck.CheckModule(entry.Module, infer.ModuleOptions{Name: entry.Name, Role: infer.EntryModule, Entry: entry.Entry})
	if len(inferErrs) != 0 {
		t.Fatal(inferErrs)
	}
	defs, elabErrs := elaborate.Increment(checked.Infos, checked.State.Instances, nil, decoded.Runtime, ck)
	if len(elabErrs) != 0 {
		t.Fatal(elabErrs)
	}
	if lintErrs := elaborate.LintProgIn(defs, decoded.Runtime, ck); len(lintErrs) != 0 {
		t.Fatal(lintErrs)
	}
}

func TestModuleObjectCodecRoundTrip(t *testing.T) {
	d := t.TempDir()
	entry := filepath.Join(d, "Lib.fango")
	content := []byte("{-# no-prelude #-}\nmodule Lib exposing (Box, box, id)\ntype Box a = Box a\nbox = Box ()\nid x = x\nhidden = ()\n")
	if err := os.WriteFile(entry, content, 0o644); err != nil {
		t.Fatal(err)
	}
	result, diagnostics, internalErr := (&Session{}).Compile(entry)
	if internalErr != nil {
		t.Fatal(internalErr)
	}
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if len(result.Objects) != 1 {
		t.Fatalf("objects = %d", len(result.Objects))
	}
	data, err := EncodeObject(result.Objects[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	f := source.NewFile("Lib.fango", content)
	decoded, err := DecodeObject(data, map[string]*source.File{"Lib.fango": f})
	if err != nil {
		t.Fatal(err)
	}
	adt := decoded.State.ADTs[0]
	if decoded.State.Ctors["Lib.Box"] != adt.Ctors[0] || adt.Ctors[0].Result.Unique != adt.Con.Unique {
		t.Fatal("nominal graph sharing changed during round trip")
	}
	// The stage section is not read until something needs it, so a decoded
	// object carries the rest of itself and a way to reach the stage half.
	if len(decoded.Runtime) == 0 || len(decoded.Stage) != 0 || decoded.Pending == nil {
		t.Fatalf("runtime=%d stage=%d pending=%v", len(decoded.Runtime), len(decoded.Stage), decoded.Pending != nil)
	}
	payload, err := decoded.Pending.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Stage) == 0 {
		t.Fatal("the deferred stage section is empty")
	}
	if decoded.State.ADTs[0] != adt {
		t.Fatal("reading the stage section decoded shared structure a second time")
	}
	if _, visible := decoded.Resolver.Values["hidden"]; visible {
		t.Fatal("private value leaked into resolver interface")
	}
}

func TestModuleObjectRejectsCorruptEnvelope(t *testing.T) {
	data, err := EncodeObject(&ModuleObject{State: &infer.ModuleState{Name: "M"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-2] ^= 1
	if _, err := DecodeObject(data, nil); err == nil {
		t.Fatal("corrupt module object was accepted")
	}
}

func TestCachedObjectRejectsInconsistentOwnSummary(t *testing.T) {
	d := t.TempDir()
	entry := filepath.Join(d, "Main.fango")
	if err := os.WriteFile(entry, []byte("{-# no-prelude #-}\nmodule Main exposing (main)\nmain = ()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := newMemoryObjectCache()
	result, _ := compileEvents(t, entry, cache)
	slot := compilecache.Slot(true, "Main")
	record, _, err := SplitObject(cache.objects[slot])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"semantic", "ABI"} {
		t.Run(field, func(t *testing.T) {
			object := *result.Objects[len(result.Objects)-1]
			if field == "semantic" {
				object.OwnSemantic = "wrong"
			} else {
				object.OwnABI = "wrong"
			}
			data, err := EncodeObject(&object, record)
			if err != nil {
				t.Fatal(err)
			}
			cache.StoreObject(slot, data)
			_, events := compileEvents(t, entry, cache)
			if events["checked-cache-miss"]["Main"] != 1 || events["check"]["Main"] != 1 {
				t.Fatalf("inconsistent %s summary was reused: %#v", field, events)
			}
		})
	}
}

func TestInstallObjectRollsBackAllPublishedState(t *testing.T) {
	d := t.TempDir()
	entry := filepath.Join(d, "Base.fango")
	content := []byte("module Base exposing (main)\nmain = 1\n")
	if err := os.WriteFile(entry, content, 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics, internalErr := (&Session{}).Compile(entry)
	if internalErr != nil {
		t.Fatal(internalErr)
	}
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	var target *ModuleObject
	sources := map[string]*source.File{}
	for _, module := range compiled.Graph.Modules {
		if module.Source != nil {
			sources[module.Source.Name] = source.NewFile(module.Source.Name, module.Source.Content)
		}
	}
	for _, object := range compiled.Objects {
		data, err := EncodeObject(object, nil)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeObject(data, sources)
		if err != nil {
			t.Fatalf("decode %s: %v", object.State.Name, err)
		}
		if target == nil && len(decoded.State.Instances) != 0 {
			target = decoded
		}
	}
	if target == nil {
		t.Fatal("fixture graph has no instance-bearing object")
	}
	target.State.Instances[0].Cutoff = []infer.DeclRef{{Module: "missing", Index: 999}}
	sup := &types.Supply{}
	ck := infer.NewChecker(sup, types.NewBuiltins(sup), infer.NewEnv())
	stageSession := staging.Install(ck)
	beforeADTs, beforeTemplates := len(ck.ADTOrder), ck.Templates.Len()
	if err := InstallObject(ck, stageSession, target); err == nil {
		t.Fatal("corrupt cutoff installed")
	}
	if len(ck.ADTOrder) != beforeADTs || ck.Templates.Len() != beforeTemplates || len(ck.Instances) != 0 || len(ck.Intrinsics) != 0 || ck.IO != nil {
		t.Fatalf("partial install survived: adts=%d templates=%d instances=%d intrinsics=%d io=%v", len(ck.ADTOrder), ck.Templates.Len(), len(ck.Instances), len(ck.Intrinsics), ck.IO)
	}
}

func TestCheckedCacheCoversNativeSidecarEntry(t *testing.T) {
	d := t.TempDir()
	entry := filepath.Join(d, "Sidecar.fango")
	if err := os.WriteFile(entry, []byte("tag : String -> String\ntag = native\nmain() = print (tag \"x\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	native := "package native\n\nfunc Tag(s string) string { return s }\n"
	if err := os.WriteFile(filepath.Join(d, "Sidecar.native.go"), []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := newMemoryObjectCache()
	compileEvents(t, entry, cache)
	_, events := compileEvents(t, entry, cache)
	if events["checked-cache-hit"]["<entry>"] != 1 || events["check"]["<entry>"] != 0 {
		t.Fatalf("native sidecar owner was not cacheable: %#v", events)
	}
}

func TestFixityChangeRechecksEveryOwner(t *testing.T) {
	d := t.TempDir()
	ops := filepath.Join(d, "Ops.fango")
	main := filepath.Join(d, "Main.fango")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(ops, "{-# no-prelude #-}\nmodule Ops exposing ((|+|))\n(|+|) : String -> String -> String\n(|+|) a b = a\ninfixl 6 (|+|)\n")
	write(main, "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Ops exposing ((|+|))\nmain = \"a\" |+| \"b\" |+| \"c\"\n")
	cache := newMemoryObjectCache()
	compileEvents(t, main, cache)
	// Only the operator table moves: the importer's own source is untouched,
	// but the table its declarations are read under is not the same one.
	write(ops, "{-# no-prelude #-}\nmodule Ops exposing ((|+|))\n(|+|) : String -> String -> String\n(|+|) a b = a\ninfixr 7 (|+|)\n")
	_, events := compileEvents(t, main, cache)
	if events["check"]["Ops"] != 1 {
		t.Fatalf("the owner of the changed table was not rechecked: %#v", events)
	}
	if events["check"]["Main"] != 1 || events["checked-cache-hit"]["Main"] != 0 {
		t.Fatalf("a checked artifact survived a graph fixity change: %#v", events)
	}
}

func TestEntriesShareDependencyArtifactsAndKeepRolesApart(t *testing.T) {
	d := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(d, name+".fango")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("Shared", "{-# no-prelude #-}\nmodule Shared exposing (value)\nvalue = \"shared\"\n")
	first := write("First", "{-# no-prelude #-}\nmodule First exposing (main)\nimport Shared\nmain = Shared.value\n")
	second := write("Second", "{-# no-prelude #-}\nmodule Second exposing (main)\nimport Shared\nmain = Shared.value\n")
	cache := newMemoryObjectCache()
	compileEvents(t, first, cache)
	_, events := compileEvents(t, second, cache)
	if events["checked-cache-hit"]["Shared"] != 1 {
		t.Fatalf("a second entry did not share the dependency artifact: %#v", events)
	}
	if events["check"]["Second"] != 1 {
		t.Fatalf("the new entry was not checked: %#v", events)
	}
	// The same module in the other role is a different artifact: an entry
	// carries entry-only obligations a dependency does not.
	_, events = compileEvents(t, first, cache)
	if events["checked-cache-hit"]["First"] != 1 {
		t.Fatalf("the first entry lost its artifact to the second: %#v", events)
	}
	asDependency := write("Uses", "{-# no-prelude #-}\nmodule Uses exposing (main)\nimport First\nmain = First.main\n")
	_, events = compileEvents(t, asDependency, cache)
	if events["check"]["First"] != 1 || events["checked-cache-hit"]["First"] != 0 {
		t.Fatalf("an entry artifact was reused for the same module as a dependency: %#v", events)
	}
}

func TestExportedSchemeChangeInvalidatesConsumers(t *testing.T) {
	d := t.TempDir()
	lib := filepath.Join(d, "Lib.fango")
	main := filepath.Join(d, "Main.fango")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(lib, "{-# no-prelude #-}\nmodule Lib exposing (pick)\npick : String -> String -> String\npick a b = a\n")
	write(main, "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.pick \"a\" \"b\"\n")
	cache := newMemoryObjectCache()
	compileEvents(t, main, cache)
	write(lib, "{-# no-prelude #-}\nmodule Lib exposing (pick)\npick : a -> a -> a\npick a b = a\n")
	_, events := compileEvents(t, main, cache)
	if events["check"]["Main"] != 1 || events["checked-cache-hit"]["Main"] != 0 {
		t.Fatalf("an exported scheme change left its consumer cached: %#v", events)
	}
}

// A dependency's comment edit must not make an importer's diagnostics point at
// the positions its artifact was created under.
func TestReusedArtifactsReportCurrentSourcePositions(t *testing.T) {
	d := t.TempDir()
	lib := filepath.Join(d, "Lib.fango")
	main := filepath.Join(d, "Main.fango")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(lib, "{-# no-prelude #-}\nmodule Lib exposing (value)\nvalue : String\nvalue = \"one\"\n")
	write(main, "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.value\n")
	cache := newMemoryObjectCache()
	compileEvents(t, main, cache)
	// Lib's declaration moves down; its meaning does not.
	write(lib, "{-# no-prelude #-}\nmodule Lib exposing (value)\n-- a comment that shifts every span below it\n-- and another\nvalue : String\nvalue = \"one\"\n")
	write(main, "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.missing\n")
	_, diagnostics, internalErr := (&Session{Cache: cache}).Compile(main)
	if internalErr != nil {
		t.Fatal(internalErr)
	}
	if len(diagnostics) == 0 {
		t.Fatal("expected a diagnostic for the unknown name")
	}
	span := diagnostics[0].Span
	if span.File == nil {
		t.Fatalf("diagnostic has no source file: %#v", diagnostics[0])
	}
	if !strings.Contains(string(span.File.Content), "Lib.missing") {
		t.Fatalf("diagnostic points at a stale source snapshot: %q", span.File.Name)
	}
}

// Stage Core is the largest part of a module object and is needed only to
// check a module from source. A compile whose modules all come from cache
// checks none, so it must read no stage section at all.
func TestAllHitCompileReadsNoStageSection(t *testing.T) {
	d := t.TempDir()
	lib := filepath.Join(d, "Lib.fango")
	main := filepath.Join(d, "Main.fango")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(lib, "{-# no-prelude #-}\nmodule Lib exposing (value)\nvalue = \"one\"\n")
	write(main, "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.value\n")
	cache := newMemoryObjectCache()
	compileEvents(t, main, cache)

	_, events := compileEvents(t, main, cache)
	if events["checked-cache-hit"]["Lib"] != 1 || events["checked-cache-hit"]["Main"] != 1 {
		t.Fatalf("the second compile was not all hits: %#v", events)
	}
	if n := len(events["stage-section"]); n != 0 {
		t.Fatalf("an all-hit compile read %d stage sections: %#v", n, events)
	}

	// One module checked from source elaborates against its dependency's
	// installed stage definitions, so the deferred section is read then.
	write(main, "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.value\n-- edited\n")
	_, events = compileEvents(t, main, cache)
	if events["check"]["Main"] != 1 || events["checked-cache-hit"]["Lib"] != 1 {
		t.Fatalf("expected an edited entry over a cached dependency: %#v", events)
	}
	if events["stage-section"]["Lib"] != 1 {
		t.Fatalf("the dependency's stage section was not read for a source check: %#v", events)
	}
}

// A deferred section is read long after the object it belongs to was
// installed, so it carries its own agreement check with that object.
func TestStageSectionDisagreeingWithItsObjectIsRejected(t *testing.T) {
	d := t.TempDir()
	entry := filepath.Join(d, "Lib.fango")
	content := []byte("{-# no-prelude #-}\nmodule Lib exposing (value)\nvalue = \"one\"\n")
	if err := os.WriteFile(entry, content, 0o644); err != nil {
		t.Fatal(err)
	}
	result, _, internalErr := (&Session{}).Compile(entry)
	if internalErr != nil {
		t.Fatal(internalErr)
	}
	object := result.Objects[0]
	object.StageImplementation = "a fingerprint the section cannot have"
	data, err := EncodeObject(object, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeObject(data, map[string]*source.File{"Lib.fango": source.NewFile("Lib.fango", content)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decoded.Pending.load(); err == nil {
		t.Fatal("a stage section disagreeing with its object was accepted")
	}
}

// A module keeps one slot, so editing it replaces that module's artifact
// instead of adding one beside it and leaving the old one to be found again.
func TestAnEditReplacesTheModuleArtifact(t *testing.T) {
	d := t.TempDir()
	lib := filepath.Join(d, "Lib.fango")
	main := filepath.Join(d, "Main.fango")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(lib, "{-# no-prelude #-}\nmodule Lib exposing (value)\nvalue = \"one\"\n")
	write(main, "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.value\n")
	cache := newMemoryObjectCache()
	compileEvents(t, main, cache)
	before := len(cache.objects)
	write(lib, "{-# no-prelude #-}\nmodule Lib exposing (value)\nvalue = \"two\"\n")
	compileEvents(t, main, cache)
	if len(cache.objects) != before {
		t.Fatalf("an edit left %d artifacts where the graph has %d modules", len(cache.objects), before)
	}
	result, events := compileEvents(t, main, cache)
	if events["check"]["Lib"] != 0 || events["checked-cache-hit"]["Lib"] != 1 {
		t.Fatalf("the replaced artifact was not the one reused: %#v", events)
	}
	if got := stringDefinition(t, result, "Lib.value"); got != "two" {
		t.Fatalf("reused implementation = %q", got)
	}
}

// The same properties through the real store, which is where slots become
// paths: one file per module, and an edit rewrites a file rather than adding
// one.
func TestOnDiskArtifactsAreOnePerModule(t *testing.T) {
	d := t.TempDir()
	lib := filepath.Join(d, "Lib.fango")
	main := filepath.Join(d, "Main.fango")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(lib, "{-# no-prelude #-}\nmodule Lib exposing (value)\nvalue = \"one\"\n")
	write(main, "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.value\n")
	compileEvents(t, main, compilecache.NewModuleStore(main))
	first := artifactPaths(t, d)
	if len(first) != 2 {
		t.Fatalf("a two-module graph stored %d artifacts: %v", len(first), first)
	}
	write(lib, "{-# no-prelude #-}\nmodule Lib exposing (value)\nvalue = \"two\"\n")
	compileEvents(t, main, compilecache.NewModuleStore(main))
	if second := artifactPaths(t, d); !reflect.DeepEqual(first, second) {
		t.Fatalf("an edit changed the stored artifacts from %v to %v", first, second)
	}
	_, events := compileEvents(t, main, compilecache.NewModuleStore(main))
	if events["check"]["Lib"] != 0 || events["check"]["Main"] != 0 {
		t.Fatalf("a warm build off disk did module work: %#v", events)
	}
}

// artifactPaths names every artifact beneath a project's cache, relative to
// it, so a test can compare what a build left behind.
func artifactPaths(t *testing.T, dir string) []string {
	t.Helper()
	root := filepath.Join(dir, ".fango", "cache")
	var out []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// The compiler namespace is a hash of the running binary; the layout
		// beneath it is what this is about.
		parts := strings.Split(filepath.ToSlash(relative), "/")
		out = append(out, strings.Join(parts[2:], "/"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// A compile-time-only declaration exists only in stage Core: the runtime
// elaboration leaves it out, so the stage elaboration, which skips what the
// runtime one discharged, is where its obligations are discharged at all.
func TestStageCoreChecksCompileTimeOnlyDeclarations(t *testing.T) {
	d := t.TempDir()
	main := filepath.Join(d, "main.fango")
	source := `import Meta exposing (Code)

effect Counter
    readCounter : () -> Int

keep : (() ->{e} Int) -> () ->{e} Int
keep action = action

leak : Code -> () ->{Counter} Int
leak code =
    handle keep { _ -> readCounter() } with state = code on
        readCounter () -> resume 1 with state

main() = print "ok"
`
	if err := os.WriteFile(main, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	var snapshots, lints int
	s := &Session{DisableObjectCache: true, Observe: func(event compileevent.Event) {
		if event.Begin || event.Owner != "<entry>" {
			return
		}
		switch event.Stage {
		case "stage-snapshot":
			snapshots++
		case "semantic-lint":
			lints++
		}
	}}
	result, diagnostics, internalErr := s.Compile(main)
	if internalErr != nil || result == nil || len(diagnostics) != 0 {
		t.Fatalf("result=%v diagnostics=%v internal=%v", result, diagnostics, internalErr)
	}
	if snapshots != 1 || lints != 1 {
		t.Fatalf("snapshots=%d lints=%d", snapshots, lints)
	}
	for _, d := range result.Program.Defs {
		if d.Name == "leak" {
			t.Fatal("compile-time-only declaration reached runtime Core")
		}
	}

}
