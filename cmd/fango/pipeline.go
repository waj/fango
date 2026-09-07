package main

import (
	"fmt"
	"go/format"
	"io"
	"os"

	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
)

// compileFile runs source → tokens → AST → typed AST → Core. Diagnostics go
// to stderr; ok is false if any stage failed.
func compileFile(entry string, stderr io.Writer) (*core.Prog, *infer.Checker, bool) {
	prog, ck, _, _, _, ok := compileFileGraph(entry, stderr)
	return prog, ck, ok
}

func compileFileGraph(entry string, stderr io.Writer) (*core.Prog, *infer.Checker, []modules.ManifestEntry, []modules.Unit, []modules.NativeSource, bool) {
	loaded, loadErrs := modules.Load(entry)
	if report(stderr, loadErrs) {
		return nil, nil, nil, nil, nil, false
	}

	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	ck.Operators = loaded.Operators
	ck.EntryName = loaded.Entry
	staging.Install(ck)
	infos, inferErrs := ck.Module(loaded.Module)
	if report(stderr, inferErrs) {
		return nil, nil, nil, nil, nil, false
	}

	prog, elabErrs := elaborate.Module(infos, ck)
	if report(stderr, elabErrs) {
		return nil, nil, nil, nil, nil, false
	}
	if lintErrs := core.Lint(prog, ck.B); len(lintErrs) > 0 {
		fmt.Fprintf(stderr, "fango: internal compiler error: Core invariants violated:\n")
		for _, e := range lintErrs {
			fmt.Fprintf(stderr, "  %v\n", e)
		}
		return nil, nil, nil, nil, nil, false
	}
	return prog, ck, loaded.Manifest, loaded.Units, loaded.Natives, true
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
// build, run, --emit-go, and backend structural tests.
func emitProjectManifest(entry string, stderr io.Writer) ([]codegen.File, []modules.ManifestEntry, bool) {
	prog, ck, manifest, loadedUnits, nativeSources, ok := compileFileGraph(entry, stderr)
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
	printMain := os.Getenv("FANGO_INTERNAL_PRINT_MAIN") == "1"
	files, err := codegen.EmitProject(prog, ck.B, units, printMain)
	if err != nil {
		fmt.Fprintf(stderr, "fango: internal compiler error: %v\n", err)
		return nil, nil, false
	}
	for _, native := range nativeSources {
		if native.Bundled {
			continue // bundled templates inline; their sidecars feed the compiler registry
		}
		data := native.Content
		if formatted, err := format.Source(data); err == nil {
			data = formatted
		}
		files = append(files, codegen.File{Path: "native/" + codegen.NativeLinkName(native.Module) + "/native.go", Data: data})
	}
	return files, manifest, true
}

// cmdCheck parses and typechecks only: quiet on success (exit 0),
// diagnostics on stderr (exit 1). The test harness's workhorse.
func cmdCheck(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		usage(stderr)
		return 2
	}
	if _, _, ok := compileFile(args[0], stderr); !ok {
		return 1
	}
	return 0
}
