package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/waj/fango/internal/build"
	"github.com/waj/fango/internal/nativedebug"
)

func cmdDebug(args []string, stdout, stderr io.Writer) int {
	binary, targetArgs, code := prepareDebug(args, stderr)
	if code != 0 {
		return code
	}
	if err := nativedebug.Run(binary, targetArgs, os.Stdin, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "fango debug: %v\n", err)
		return 1
	}
	return 0
}

// Source launches stay inside the isolated debug build tree; they never replace
// an ordinary executable in the user's working directory.
func prepareDebug(args []string, stderr io.Writer) (string, []string, int) {
	fs := flag.NewFlagSet("debug", flag.ContinueOnError)
	fs.SetOutput(stderr)
	observed := reporting(fs, stderr)
	if fs.Parse(args) != nil || fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: fango debug [verbosity] SOURCE.fango|BINARY [--] [args...]")
		return "", nil, 2
	}
	binary, targetArgs := fs.Arg(0), fs.Args()[1:]
	if len(targetArgs) > 0 && targetArgs[0] == "--" {
		targetArgs = targetArgs[1:]
	}
	if filepath.Ext(binary) == ".fango" {
		session, report := observed()
		session.debug = true
		session.goBuildFlags = []string{"-gcflags=all=-N -l"}
		dir, program, ok := ensureBuilt(binary, stderr, session, report)
		if !ok {
			return "", nil, 1
		}
		binary = build.BinaryPath(dir, program)
		data, err := os.ReadFile(binary)
		if err == nil {
			err = nativedebug.WriteManifest(binary, data, session.debugSources, session.debugScopes)
		}
		if err != nil {
			fmt.Fprintf(stderr, "fango debug: %v\n", err)
			return "", nil, 1
		}
		report.finish(binary)
	}
	return binary, targetArgs, 0
}
