package main

import (
	"fmt"
	"go/format"
	"io"
	"path/filepath"

	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/compilecache"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/runtimefiles"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
)

type stageEvent struct {
	Stage string
	Owner string
}

type compilationSession struct{ observe func(stageEvent) }

func (s *compilationSession) event(stage, owner string) {
	if s != nil && s.observe != nil {
		s.observe(stageEvent{Stage: stage, Owner: owner})
	}
}

// compileFile runs source → tokens → AST → typed AST → Core. Diagnostics go
// to stderr; ok is false if any stage failed.
func compileFile(entry string, stderr io.Writer) (*core.Prog, *infer.Checker, bool) {
	prog, ck, _, _, _, ok := compileFileGraph(entry, stderr)
	return prog, ck, ok
}

func compileFileGraph(entry string, stderr io.Writer) (*core.Prog, *infer.Checker, []modules.ManifestEntry, []modules.Unit, []modules.NativeSource, bool) {
	return compileFileGraphSession(entry, stderr, nil)
}

func compileFileGraphSession(entry string, stderr io.Writer, session *compilationSession) (*core.Prog, *infer.Checker, []modules.ManifestEntry, []modules.Unit, []modules.NativeSource, bool) {
	var observe modules.StageObserver
	if session != nil && session.observe != nil {
		observe = func(stage, owner string) { session.event(stage, owner) }
	}
	loaded, loadErrs := modules.LoadWithOptions(entry, modules.LoadOptions{Observe: observe, Parsed: compilecache.NewParsedStore(entry)})
	if report(stderr, loadErrs) {
		return nil, nil, nil, nil, nil, false
	}

	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	ck.Fixity = loaded.Fixity
	ck.EntryName = loaded.Entry
	staging.Install(ck)
	owner := entryOwner(loaded.Units, entry)
	session.event("check", owner)
	infos, inferErrs := ck.Module(loaded.Module)
	if report(stderr, inferErrs) {
		return nil, nil, nil, nil, nil, false
	}

	session.event("elaborate", owner)
	prog, elabErrs := elaborate.Module(infos, ck)
	if report(stderr, elabErrs) {
		return nil, nil, nil, nil, nil, false
	}
	session.event("semantic-lint", owner)
	if lintErrs := core.Lint(prog, ck.B); len(lintErrs) > 0 {
		fmt.Fprintf(stderr, "fango: internal compiler error: Core invariants violated:\n")
		for _, e := range lintErrs {
			fmt.Fprintf(stderr, "  %v\n", e)
		}
		return nil, nil, nil, nil, nil, false
	}
	return prog, ck, loaded.Manifest, loaded.Units, loaded.Natives, true
}

func entryOwner(units []modules.Unit, entry string) string {
	for _, unit := range units {
		if unit.Entry {
			if unit.Name != "" {
				return unit.Name
			}
			return "<entry>"
		}
	}
	return filepath.Base(entry)
}

func report(stderr io.Writer, errs []diag.Error) bool {
	if len(errs) == 0 {
		return false
	}
	diag.Render(stderr, errs)
	return true
}

func hasMain(p *core.Prog) bool {
	entry := p.Entry
	if entry == "" {
		entry = "main"
	}
	for _, d := range p.Defs {
		if d.Name == entry {
			return true
		}
	}
	return false
}

// emitProjectManifest emits the complete multi-package Go project used by
// build, run, --emit-go, and backend structural tests. printMain makes a
// value-typed entry print its value through the shared formatter.
func emitProjectManifest(entry string, printMain bool, stderr io.Writer) ([]codegen.File, []modules.ManifestEntry, bool) {
	return emitProjectManifestSession(entry, printMain, stderr, nil)
}

func emitProjectManifestSession(entry string, printMain bool, stderr io.Writer, session *compilationSession) ([]codegen.File, []modules.ManifestEntry, bool) {
	mode := fmt.Sprintf("emit:print-main=%t", printMain)
	if files, manifest, ok := compilecache.Load(entry, mode); ok {
		session.event("cache-hit", filepath.Base(entry))
		return files, manifest, true
	}
	session.event("cache-miss", filepath.Base(entry))
	prog, ck, manifest, loadedUnits, nativeSources, ok := compileFileGraphSession(entry, stderr, session)
	if !ok {
		return nil, nil, false
	}
	if !hasMain(prog) {
		fmt.Fprintf(stderr, "fango: %s has no `main` — a program needs `main = ...`\n", entry)
		return nil, nil, false
	}
	units := make([]codegen.Unit, len(loadedUnits))
	for i, unit := range loadedUnits {
		units[i] = codegen.Unit{Name: unit.Name, Imports: unit.Imports, Entry: unit.Entry}
	}
	var files []codegen.File
	var err error
	session.event("lowering", entryOwner(loadedUnits, entry))
	if prog.Intrinsics[types.StreamWithProducerName] || prog.Intrinsics[types.IteratorNextName] {
		machineProg, lowerErrs := machineir.Lower(prog, ck.B)
		if len(lowerErrs) > 0 {
			fmt.Fprintf(stderr, "fango: internal compiler error: machine lowering failed: %v\n", lowerErrs[0])
			return nil, nil, false
		}
		session.event("emission", entryOwner(loadedUnits, entry))
		files, err = codegen.EmitMachineProject(prog, machineProg, ck.B, units, printMain)
	} else {
		session.event("emission", entryOwner(loadedUnits, entry))
		files, err = codegen.EmitProject(prog, ck.B, units, printMain)
	}
	if err != nil {
		fmt.Fprintf(stderr, "fango: internal compiler error: %v\n", err)
		return nil, nil, false
	}
	hostSource, err := runtimefiles.NativeHost()
	if err != nil {
		fmt.Fprintf(stderr, "fango: internal compiler error: %v\n", err)
		return nil, nil, false
	}
	for _, native := range nativeSources {
		data := native.Content
		if formatted, err := format.Source(data); err == nil {
			data = formatted
		}
		dir := "native/" + codegen.NativeLinkName(native.Module) + "/"
		files = append(files,
			codegen.File{Path: dir + "native.go", Data: data},
			codegen.File{Path: dir + "host.go", Data: hostSource})
	}
	compilecache.Store(entry, mode, manifest, files)
	return files, manifest, true
}

// cmdCheck parses and typechecks only: quiet on success (exit 0),
// diagnostics on stderr (exit 1). The test harness's workhorse.
func cmdCheck(args []string, stderr io.Writer) int {
	return cmdCheckSession(args, stderr, nil)
}

func cmdCheckSession(args []string, stderr io.Writer, session *compilationSession) int {
	if len(args) != 1 {
		usage(stderr)
		return 2
	}
	if _, _, ok := compilecache.Load(args[0], "check"); ok {
		session.event("cache-hit", filepath.Base(args[0]))
		return 0
	}
	session.event("cache-miss", filepath.Base(args[0]))
	if _, _, manifest, _, _, ok := compileFileGraphSession(args[0], stderr, session); !ok {
		return 1
	} else {
		compilecache.Store(args[0], "check", manifest, nil)
	}
	return 0
}
