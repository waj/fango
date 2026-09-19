package check

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
)

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
		r, diagnostics, internalErr := (&Session{}).Compile(entry)
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
	baseContent := []byte(`{-# no-prelude #-}
module Base exposing (make)
import Basics exposing (Show(..))
type Seed = Seed deriving (Show)
identity code = quote $(code)
make = identity (quote ())
`)
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
		data, err := EncodeObject(object)
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
	data, err := EncodeObject(compiled.Objects[0])
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
	data, err := EncodeObject(result.Objects[0])
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
	if len(decoded.Runtime) == 0 || len(decoded.Stage) == 0 {
		t.Fatalf("runtime=%d stage=%d", len(decoded.Runtime), len(decoded.Stage))
	}
	if _, visible := decoded.Resolver.Values["hidden"]; visible {
		t.Fatal("private value leaked into resolver interface")
	}
}

func TestModuleObjectRejectsCorruptEnvelope(t *testing.T) {
	data, err := EncodeObject(&ModuleObject{State: &infer.ModuleState{Name: "M"}})
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-2] ^= 1
	if _, err := DecodeObject(data, nil); err == nil {
		t.Fatal("corrupt module object was accepted")
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
		data, err := EncodeObject(object)
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
