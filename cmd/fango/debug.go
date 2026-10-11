package main

import (
	"fmt"
	"io"
	"os"

	"github.com/waj/fango/internal/nativedebug"
)

func cmdDebug(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: fango debug BINARY [--] [args...]")
		return 2
	}
	binary := args[0]
	args = args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if err := nativedebug.Run(binary, args, os.Stdin, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "fango debug: %v\n", err)
		return 1
	}
	return 0
}
