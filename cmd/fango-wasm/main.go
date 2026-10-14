// Command fango-wasm runs one Fango program through the Core interpreter,
// without generating Go or invoking the Go toolchain. It is the entry point
// for running Fango in a browser:
//
//	GOOS=wasip1 GOARCH=wasm go build -o fango.wasm ./cmd/fango-wasm
//
// The host provides a WASI filesystem holding the library root (stdlib/,
// found through FANGO_ROOT) and the program, passed as the only argument
// (default /src/main.fango). The program's output goes to stdout;
// diagnostics go to stderr. The exit status is the program's own, 1 for a
// compile error, or 2 for an internal error.
//
// Natives that need the sidecar worker (File, Net, Async, user sidecars) are
// unavailable: the interpreter runs with no worker, so they fail with an
// error instead of trying to build one.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/natives"
)

func main() {
	entry := "/src/main.fango"
	if len(os.Args) > 1 {
		entry = os.Args[1]
	}
	os.Exit(run(entry, os.Stdin, os.Stdout, os.Stderr))
}

func run(entry string, stdin io.Reader, stdout, stderr io.Writer) int {
	result, errs, internalErr := (&check.Session{DisableObjectCache: true}).Compile(entry)
	if len(errs) > 0 {
		diag.Render(stderr, errs)
		return 1
	}
	if internalErr != nil {
		if libroot.Missing(internalErr) {
			diag.Render(stderr, []diag.Error{{Title: "MISSING LIBRARY", Body: internalErr.Error() + "."}})
		} else {
			fmt.Fprintf(stderr, "fango: internal compiler error: %v\n", internalErr)
		}
		return 2
	}
	prog := result.Program

	env := eval.NewEnv()
	env.DefineProg(prog)
	ioctx := eval.NewIOContext(stdin, stdout)
	ioctx.ErrorWriter = stderr
	ctx := context.Background()
	_, err := eval.ForceIO(ctx, prog.Entry, env, ioctx)
	var exitErr *natives.ExitError
	switch {
	case errors.As(err, &exitErr):
		// Process.exit, or a non-zero status main returned.
		return exitErr.Code
	case err != nil:
		// A runtime fault ends the program as a compiled one's panic does.
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	return 0
}
