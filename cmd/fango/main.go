// Command fango is the compiler CLI: build | run | check | repl | clean.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/waj/fango/internal/build"
	"github.com/waj/fango/internal/modules"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "build":
		return cmdBuild(args[1:], stdout, stderr)
	case "run":
		return cmdRun(args[1:], stderr)
	case "check":
		return cmdCheck(args[1:], stderr)
	case "repl":
		return cmdRepl(args[1:], stdout, stderr)
	case "clean":
		return cmdClean(args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "fango: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage:
  fango build [-o out] [--emit-go] main.fango
  fango run main.fango
  fango check main.fango
  fango repl
  fango clean main.fango
`)
}

// compileToDir runs the pipeline for entry and leaves a ready-to-build main.go
// in the build directory, reporting whether any input changed.
func compileToDir(entry, dir string, stderr io.Writer) (changed bool, ok bool) {
	files, manifest, ok := emitProjectManifest(entry, stderr)
	if !ok {
		return false, false
	}
	materialized, err := build.Materialize(dir)
	if err != nil {
		fmt.Fprintf(stderr, "fango: %v\n", err)
		return false, false
	}
	wrote, err := build.SyncGenerated(dir, files)
	if err != nil {
		fmt.Fprintf(stderr, "fango: %v\n", err)
		return false, false
	}
	manifestWrote, err := build.WriteIfChanged(filepath.Join(dir, "sources.json"), modules.ManifestJSON(manifest))
	if err != nil {
		fmt.Fprintf(stderr, "fango: %v\n", err)
		return false, false
	}
	return materialized || wrote || manifestWrote, true
}

func ensureBuilt(entry string, stderr io.Writer) (dir string, ok bool) {
	dir, err := build.Dir(entry)
	if err != nil {
		fmt.Fprintf(stderr, "fango: %v\n", err)
		return "", false
	}
	changed, ok := compileToDir(entry, dir, stderr)
	if !ok {
		return "", false
	}
	if _, statErr := os.Stat(build.BinaryPath(dir)); changed || statErr != nil {
		if err := build.GoBuild(dir); err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return "", false
		}
	}
	return dir, true
}

func cmdBuild(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "output binary path, or project directory with --emit-go")
	emit := fs.Bool("emit-go", false, "write the generated Go project and exit")
	if fs.Parse(args) != nil || fs.NArg() != 1 {
		usage(stderr)
		return 2
	}
	entry := fs.Arg(0)
	if *emit {
		dest := *out
		if dest == "" {
			dest = strings.TrimSuffix(filepath.Base(entry), ".fango") + ".out"
		}
		if err := build.ValidateExportDir(dest); err != nil {
			fmt.Fprintf(stderr, "fango: %v\n", err)
			return 1
		}
		if _, ok := compileToDir(entry, dest, stderr); !ok {
			return 1
		}
		return 0
	}
	dir, ok := ensureBuilt(entry, stderr)
	if !ok {
		return 1
	}
	dest := *out
	if dest == "" {
		dest = strings.TrimSuffix(filepath.Base(entry), ".fango")
	}
	data, err := os.ReadFile(build.BinaryPath(dir))
	if err != nil {
		fmt.Fprintf(stderr, "fango: %v\n", err)
		return 1
	}
	if err := os.WriteFile(dest, data, 0o755); err != nil {
		fmt.Fprintf(stderr, "fango: %v\n", err)
		return 1
	}
	return 0
}

func cmdRun(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		usage(stderr)
		return 2
	}
	dir, ok := ensureBuilt(args[0], stderr)
	if !ok {
		return 1
	}
	code, err := build.RunBinary(dir)
	if err != nil {
		fmt.Fprintf(stderr, "fango: %v\n", err)
		return 1
	}
	return code
}

func cmdClean(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		usage(stderr)
		return 2
	}
	if err := build.Clean(args[0]); err != nil {
		fmt.Fprintf(stderr, "fango: %v\n", err)
		return 1
	}
	return 0
}
