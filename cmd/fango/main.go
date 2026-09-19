// Command fango is the compiler CLI: build | run | check | repl | clean.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	case "fmt":
		return cmdFmt(args[1:], stdout, stderr)
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
  fango build [-o out] [--emit-go] [verbosity] main.fango
  fango run [verbosity] main.fango [--] [args...]
  fango check [verbosity] main.fango
  fango fmt [-w] [-l] [file...]
  fango repl [dir]
  fango clean main.fango

verbosity, on build, run, and check:
  -v            report each module as it is compiled or reused
  -vv           also report stage timings and cache totals
  -timings json machine-readable build statistics
  -no-cache     compile every module from source, ignoring cached artifacts

Progress goes to stderr; a compiled program's own output is untouched.
`)
}

// compileToDir runs the pipeline for entry and leaves a ready-to-build main.go
// in the build directory, reporting whether any input changed.
func compileToDir(entry, dir string, stderr io.Writer, session *compilationSession, report *reporter) (changed bool, ok bool) {
	printMain := os.Getenv("FANGO_INTERNAL_PRINT_MAIN") == "1"
	files, manifest, ok := emitProjectManifestSession(entry, printMain, stderr, session)
	if !ok {
		return false, false
	}
	syncStart := time.Now()
	defer func() { report.phase("write + sync", syncStart) }()
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

func ensureBuilt(entry string, stderr io.Writer, session *compilationSession, report *reporter) (dir string, ok bool) {
	dir, err := build.Dir(entry)
	if err != nil {
		fmt.Fprintf(stderr, "fango: %v\n", err)
		return "", false
	}
	changed, ok := compileToDir(entry, dir, stderr, session, report)
	if !ok {
		return "", false
	}
	// A binary already matching unchanged sources needs no toolchain run, so
	// a warm build reports no linking stage at all.
	if _, statErr := os.Stat(build.BinaryPath(dir)); changed || statErr != nil {
		linkStart := time.Now()
		err := build.GoBuild(dir)
		report.phase("go build", linkStart)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return "", false
		}
	}
	return dir, true
}

// reporting registers the verbosity flags shared by the commands that compile,
// and builds the session and reporter they install. Output goes to stderr so
// that a compiled program's own stdout stays exactly what it wrote.
func reporting(fs *flag.FlagSet, stderr io.Writer) func() (*compilationSession, *reporter) {
	verbose := fs.Bool("v", false, "report each module as it is compiled or reused")
	veryVerbose := fs.Bool("vv", false, "also report stage timings and cache totals")
	timings := fs.String("timings", "", "write machine-readable build statistics (`json`)")
	noCache := fs.Bool("no-cache", false, "compile every module from source, ignoring cached artifacts")
	return func() (*compilationSession, *reporter) {
		level := quiet
		switch {
		case *veryVerbose:
			level = stats
		case *verbose:
			level = progress
		}
		report := newReporter(level, *timings == "json", *noCache, stderr)
		return &compilationSession{observe: report.observer(), noCache: *noCache}, report
	}
}

func cmdBuild(args []string, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "output binary path, or project directory with --emit-go")
	emit := fs.Bool("emit-go", false, "write the generated Go project and exit")
	observed := reporting(fs, stderr)
	if fs.Parse(args) != nil || fs.NArg() != 1 {
		usage(stderr)
		return 2
	}
	session, report := observed()
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
		if _, ok := compileToDir(entry, dest, stderr, session, report); !ok {
			return 1
		}
		report.finish(dest)
		return 0
	}
	dir, ok := ensureBuilt(entry, stderr, session, report)
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
	report.finish(dest)
	return 0
}

func cmdRun(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	observed := reporting(fs, stderr)
	// Parsing stops at the entry path, so everything after it — including a
	// `--` separator and any flag the program itself defines — reaches the
	// program untouched.
	if fs.Parse(args) != nil || fs.NArg() < 1 {
		usage(stderr)
		return 2
	}
	session, report := observed()
	args = fs.Args()
	dir, ok := ensureBuilt(args[0], stderr, session, report)
	if !ok {
		return 1
	}
	report.finish("")
	programArgs := args[1:]
	if len(programArgs) > 0 && programArgs[0] == "--" {
		programArgs = programArgs[1:]
	}
	code, err := build.RunBinary(dir, programArgs...)
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
