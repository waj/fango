package main

import (
	"fmt"
	"go/format"
	"io"
	"path/filepath"

	"github.com/waj/fango/internal/backend"
	compilecheck "github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/compilecache"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/runtimefiles"
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
	result, ok := checkGraph(entry, stderr, session)
	if !ok {
		return nil, nil, nil, nil, nil, false
	}
	return result.Program, result.Checker, result.Graph.Manifest, result.Graph.Units, result.Graph.Natives, true
}

func checkGraph(entry string, stderr io.Writer, session *compilationSession) (*compilecheck.Result, bool) {
	var observe compilecheck.Observer
	if session != nil && session.observe != nil {
		observe = func(stage, owner string) { session.event(stage, owner) }
	}
	result, checkErrs, internalErr := (&compilecheck.Session{Observe: observe}).Compile(entry)
	if report(stderr, checkErrs) {
		return nil, false
	}
	if internalErr != nil {
		fmt.Fprintf(stderr, "fango: internal compiler error: %v\n", internalErr)
		return nil, false
	}
	return result, true
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
	result, ok := checkGraph(entry, stderr, session)
	if !ok {
		return nil, nil, false
	}
	manifest, loadedUnits, nativeSources := result.Graph.Manifest, result.Graph.Units, result.Graph.Natives
	if !hasMain(result.Program) {
		fmt.Fprintf(stderr, "fango: %s has no `main` — a program needs `main = ...`\n", entry)
		return nil, nil, false
	}
	units := make([]codegen.Unit, len(loadedUnits))
	for i, unit := range loadedUnits {
		units[i] = codegen.Unit{Name: unit.Name, Imports: unit.Imports, Entry: unit.Entry}
	}
	var observe backend.Observer
	if session != nil && session.observe != nil {
		observe = func(stage, owner string) { session.event(stage, owner) }
	}
	files, err := (&backend.Session{Observe: observe}).EmitProject(entry, result, units, printMain)
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
