package main

import (
	"flag"
	"io"
	"os"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/repl"
	"golang.org/x/term"
)

// cmdRepl starts a session whose source root is the given directory, or the
// working directory: a prompt `import Foo.Bar` reads `Foo/Bar.fango` there.
func cmdRepl(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("repl", flag.ContinueOnError)
	flags.SetOutput(stderr)
	debug := flags.Bool("debug", false, "retain source information and enable debugger commands")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	args = flags.Args()
	if len(args) > 1 {
		usage(stderr)
		return 2
	}
	// A session resolves the bundled prelude before it can accept a prompt,
	// and treats a failure there as a compiler bug. Finding the library
	// first keeps an absent or misnamed one an ordinary diagnostic.
	if _, err := libroot.Root(); err != nil {
		diag.Render(stderr, []diag.Error{{Title: "MISSING LIBRARY", Body: err.Error() + "."}})
		return 1
	}
	opts := repl.Options{Debug: *debug}
	opts.ErrorWriter = stderr
	if len(args) == 1 {
		opts.Root = args[0]
	}
	// Line editing needs the process's own terminal on both ends; piped or
	// redirected sessions read plain lines.
	opts.Interactive = stdout == io.Writer(os.Stdout) && isTerminal(os.Stdin) && isTerminal(os.Stdout)
	// https://no-color.org: any non-empty value turns colors off.
	opts.Color = opts.Interactive && os.Getenv("NO_COLOR") == ""
	repl.RunWith(os.Stdin, stdout, opts)
	return 0
}

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }
