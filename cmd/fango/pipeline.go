package main

import (
	"flag"
	"fmt"
	"go/format"
	"io"
	"path/filepath"

	"github.com/waj/fango/internal/backend"
	"github.com/waj/fango/internal/build"
	compilecheck "github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/compileevent"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/runtimefiles"
)

// stageEvent is the pipeline's own event type; the CLI adds no fields of its
// own to it.
type stageEvent = compileevent.Event

// compilationSession carries what a command installs into the pipeline: an
// observer, and whether to bypass the persistent cache. A session with no
// observer costs the pipeline nothing.
type compilationSession struct {
	observe compileevent.Observer
	noCache bool
}

func (s *compilationSession) disableCache() bool { return s != nil && s.noCache }

func (s *compilationSession) observer() compileevent.Observer {
	if s == nil {
		return nil
	}
	return s.observe
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
	result, checkErrs, internalErr := (&compilecheck.Session{Observe: session.observer(), DisableObjectCache: session.disableCache()}).Compile(entry)
	if report(stderr, checkErrs) {
		return nil, false
	}
	if internalErr != nil {
		reportInternal(stderr, internalErr)
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
// value-typed entry print its value through the shared formatter. The program
// it returns is the entry's stem, which names both its generated package and
// its cached artifacts. The tree is where the generated Go a cached emission
// describes already lives; emitting without one, as the structural tests do,
// generates every unit.
func emitProjectManifest(entry string, printMain bool, stderr io.Writer) ([]codegen.File, []modules.ManifestEntry, string, bool) {
	return emitProjectManifestSession(entry, "", printMain, stderr, nil)
}

func emitProjectManifestSession(entry, tree string, printMain bool, stderr io.Writer, session *compilationSession) ([]codegen.File, []modules.ManifestEntry, string, bool) {
	result, ok := checkGraph(entry, stderr, session)
	if !ok {
		return nil, nil, "", false
	}
	manifest, loadedUnits, nativeSources := result.Graph.Manifest, result.Graph.Units, result.Graph.Natives
	if !hasMain(result.Program) {
		fmt.Fprintf(stderr, "fango: %s has no `main` — a program needs `main = ...`\n", entry)
		return nil, nil, "", false
	}
	units := make([]codegen.Unit, len(loadedUnits))
	program := ""
	for i, unit := range loadedUnits {
		units[i] = codegen.Unit{Name: unit.Name, Program: unit.Program, Imports: unit.Imports, Entry: unit.Entry}
		if unit.Entry {
			program = unit.Program
		}
	}
	emitting := &backend.Session{Observe: session.observer(), DisableCache: session.disableCache(), Emitted: build.Generated(tree)}
	files, err := emitting.EmitProject(entry, result, units, printMain)
	if err != nil {
		reportInternal(stderr, err)
		return nil, nil, "", false
	}
	hostSource, err := runtimefiles.NativeHost()
	if err != nil {
		reportInternal(stderr, err)
		return nil, nil, "", false
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
	return files, manifest, program, true
}

// cmdCheck parses and typechecks only: quiet on success (exit 0),
// diagnostics on stderr (exit 1). The test harness's workhorse.
func cmdCheck(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	observed := reporting(fs, stderr)
	if fs.Parse(args) != nil || fs.NArg() != 1 {
		usage(stderr)
		return 2
	}
	session, report := observed()
	code := cmdCheckSession(fs.Args(), stderr, session)
	if code == 0 {
		// check stops before emission, so it reports discovery and the
		// semantic phase and nothing else.
		report.finish("")
	}
	return code
}

func cmdCheckSession(args []string, stderr io.Writer, session *compilationSession) int {
	if len(args) != 1 {
		usage(stderr)
		return 2
	}
	if _, _, _, _, _, ok := compileFileGraphSession(args[0], stderr, session); !ok {
		return 1
	}
	return 0
}

// reportInternal prints a compiler-side failure. A library that cannot be
// found is the one such failure a user can fix, and telling them to report a
// compiler bug instead would be actively misleading, so it reports as itself.
func reportInternal(stderr io.Writer, err error) {
	if libroot.Missing(err) {
		diag.Render(stderr, []diag.Error{{Title: "MISSING LIBRARY", Body: err.Error() + "."}})
		return
	}
	fmt.Fprintf(stderr, "fango: internal compiler error: %v\n", err)
}
