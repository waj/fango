package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
)

func TestModuleBoundaryMatchesMergedEmission(t *testing.T) {
	d := t.TempDir()
	entry := filepath.Join(d, "Main.fango")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(d, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Lib.fango", "{-# no-prelude #-}\nmodule Lib exposing (main, twice)\nmain x y = x\ntwice f x = f (f x)\n")
	write("Main.fango", "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Lib\nmain = Lib.main (Lib.twice (\\x -> x) ()) ()\n")

	var stderr bytes.Buffer
	modular, modularChecker, _, units, _, ok := compileFileGraphSession(entry, &stderr, nil)
	if !ok {
		t.Fatalf("module compile: %s", stderr.String())
	}

	loaded, loadErrs := modules.Load(entry)
	if len(loadErrs) != 0 {
		t.Fatal(loadErrs)
	}
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	mergedChecker := infer.NewChecker(sup, b, infer.NewEnv())
	mergedChecker.Fixity, mergedChecker.EntryName = loaded.Fixity, loaded.Entry
	staging.Install(mergedChecker)
	infos, inferErrs := mergedChecker.Module(loaded.Module)
	if len(inferErrs) != 0 {
		t.Fatal(inferErrs)
	}
	merged, elabErrs := elaborate.Module(infos, mergedChecker)
	if len(elabErrs) != 0 {
		t.Fatal(elabErrs)
	}
	if lintErrs := core.Lint(merged, mergedChecker.B); len(lintErrs) != 0 {
		t.Fatal(lintErrs)
	}

	codeUnits := make([]codegen.Unit, len(units))
	for i, unit := range units {
		codeUnits[i] = codegen.Unit{Name: unit.Name, Imports: unit.Imports, Entry: unit.Entry}
	}
	got, err := codegen.EmitProject(modular, modularChecker.B, codeUnits, true)
	if err != nil {
		t.Fatal(err)
	}
	want, err := codegen.EmitProject(merged, mergedChecker.B, codeUnits, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("file counts %d != %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Path != want[i].Path || !bytes.Equal(got[i].Data, want[i].Data) {
			t.Fatalf("emission differs for %s", got[i].Path)
		}
	}
}
