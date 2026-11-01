package check

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
)

// Lib declares an unparameterized record, whose constructor has no
// instantiation to copy its fields into, and Bump updates it from another
// module.
var recordUpdateModules = map[string]string{
	"Lib.fango": `module Lib exposing (Note(..), Source(..), Box(..), box)

effect Note
    note : () -> Int

type Source e = { pull : () ->{e} Int }

type Box = { source : Source {Note}, count : Int, run : () ->{Note} Int }

box : Box
box = { source = { pull = { _ -> note() } }, count = 0, run = { _ -> note() } }
`,
	"Bump.fango": `module Bump exposing (bump)

import Lib exposing (Box(..))

bump : Box -> Box
bump value = { value | count = 1 }
`,
	"Main.fango": `module Main exposing (main)

import Bump exposing (bump)
import Lib exposing (Box(..), Note(..), Source(..), box)

pull : Source {Note} ->{Note} Int
pull source = source.pull()

main() =
    updated = bump box
    print (handle pull updated.source on
        note () -> resume 1)
`,
}

func writeRecordUpdateModules(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	for name, body := range recordUpdateModules {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(d, "Main.fango")
}

// Elaborating an update of an imported record must leave the field types that
// later modules check against as they were declared.
func TestRecordUpdateKeepsImportedFieldTypes(t *testing.T) {
	compileEvents(t, writeRecordUpdateModules(t), newMemoryObjectCache())
}

// Installing one module at a time joins each summary job before the next
// module is checked, so anything a later module changes in a finished object
// shows up as a fingerprint that no longer matches, whatever the timing.
func TestLaterModulesLeaveFinishedObjectsUnchanged(t *testing.T) {
	loaded, errs := modules.LoadWithOptions(writeRecordUpdateModules(t), modules.LoadOptions{})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	sup := &types.Supply{}
	ck := infer.NewChecker(sup, types.NewBuiltins(sup), infer.NewEnv())
	ck.Fixity, ck.EntryName = loaded.Fixity, loaded.Entry
	installer := NewInstaller(ck, staging.Install(ck), nil, nil)
	for _, module := range loaded.Modules {
		if module.Name == "Main" {
			break
		}
		if _, diagnostics, err := installer.Install([]modules.ResolvedModule{module}, loaded.FixityHash); len(diagnostics) != 0 || err != nil {
			t.Fatal(module.Name, diagnostics, err)
		}
	}
	for _, object := range installer.Objects() {
		if semantic, abi, _ := ownFingerprints(object, ""); semantic != object.OwnSemantic || abi != object.OwnABI {
			t.Errorf("%s changed after its fingerprints were computed", object.State.Name)
		}
		if stage := ownStageFingerprint(object, object.Stage); stage != object.StageImplementation {
			t.Errorf("%s stage Core changed after its fingerprint was computed", object.State.Name)
		}
	}
}
