package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/format"
	"github.com/waj/fango/internal/source"
)

// cmdFmt formats Fango source. With no paths it reads standard input and
// writes the result to standard output, so it composes with an editor that
// pipes a buffer through a formatter.
func cmdFmt(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fmt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	write := fs.Bool("w", false, "rewrite each file in place")
	list := fs.Bool("l", false, "list the files that would change, and exit 1 if any would")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	paths := fs.Args()

	if len(paths) == 0 || (len(paths) == 1 && paths[0] == "-") {
		if *write {
			fmt.Fprintln(stderr, "fango fmt: -w needs file paths")
			return 2
		}
		src, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(stderr, "fango fmt: %v\n", err)
			return 1
		}
		out, errs := format.Source(source.NewFile("<stdin>", src))
		if len(errs) > 0 {
			diag.Render(stderr, errs)
			return 1
		}
		stdout.Write(out)
		return 0
	}

	changed, failed := false, false
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "fango fmt: %v\n", err)
			failed = true
			continue
		}
		out, errs := format.Source(source.NewFile(path, src))
		if len(errs) > 0 {
			diag.Render(stderr, errs)
			failed = true
			continue
		}
		differs := !bytes.Equal(out, src)
		changed = changed || differs
		switch {
		case *list:
			if differs {
				fmt.Fprintln(stdout, path)
			}
		case *write:
			if differs {
				if err := os.WriteFile(path, out, 0o644); err != nil {
					fmt.Fprintf(stderr, "fango fmt: %v\n", err)
					failed = true
				}
			}
		default:
			// Like gofmt, printing is unconditional: the formatted source is
			// what was asked for, whether or not it differs.
			stdout.Write(out)
		}
	}
	switch {
	case failed:
		return 1
	case *list && changed:
		return 1
	}
	return 0
}
