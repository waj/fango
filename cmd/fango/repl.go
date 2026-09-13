package main

import (
	"io"
	"os"

	"github.com/waj/fango/internal/repl"
)

// cmdRepl starts a session whose source root is the given directory, or the
// working directory: a prompt `import Foo.Bar` reads `Foo/Bar.fango` there.
func cmdRepl(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		usage(stderr)
		return 2
	}
	var opts repl.Options
	if len(args) == 1 {
		opts.Root = args[0]
	}
	repl.RunWith(os.Stdin, stdout, opts)
	return 0
}
