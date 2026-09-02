package main

import (
	"io"
	"os"

	"github.com/waj/fango/internal/repl"
)

func cmdRepl(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		usage(stderr)
		return 2
	}
	repl.Run(os.Stdin, stdout)
	return 0
}
