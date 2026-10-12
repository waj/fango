# Native source debugging

`fango debug [verbosity] SOURCE.fango|BINARY [--] [args...]` launches a native
program using Delve on macOS or Linux. A `.fango` path is built automatically
with the [debug profile](commands.md#native-debug-builds), then launched. For
the repository example:

```sh
./fango debug examples/mandelbrot.fango
```

Source launches reuse the isolated debug build cache and leave executables
in the working directory untouched. Build verbosity flags such as `-v` go
before the source path; arguments after the path (and an optional `--`) go
to the program. A failed build does not launch an older executable. Existing
binaries built with `fango build --debug` are also accepted, with their
matching companion metadata: `fango debug ./mandelbrot`.

The debugger stops at the first executable Fango location reached from main
and presents a `debug>` prompt.

The companion source snapshots supply excerpts, so source files do not need
to be present in the current working directory. The displayed text belongs
to the binary's build, even if the files have since changed. Rebuild and start
a new session to debug changed source. Startup does not stop in module
initialization before main.

At a Fango stop, a caret underline marks the recorded expression, using the
same excerpt format as the REPL. Native locations identify lines rather than
expression columns. If several sites on a line share a recorded enclosing
expression, the debugger underlines it and labels it as enclosing; it does
not claim to identify the active subexpression. If the sites have no common
recorded enclosure, or the span is missing, the line is shown with an
explicit span-unavailable notice.

## Stepping and breakpoints

The [shared command reference](debugger-commands.md) defines commands,
aliases, optional colons, repetition, and output formats for both debuggers.
Native inspection additionally provides `args` and the `raw` commands.
Frame selection, expansion, and cancelling a single REPL input are not native
capabilities; unsupported commands report that explicitly.

Use the logical filenames shown at stops, such as `mandelbrot.fango` or
`<stdlib>/Range.fango`. Unambiguous path suffixes are accepted. For Mandelbrot,
`break mandelbrot.fango:35` stops before calculating a point's real coordinate;
`next` proceeds to its imaginary coordinate on line 36 and drawing call on
line 37. `break <stdlib>/Range.fango:29` stops at the range callback.

Breakpoints require a linked statement at the exact line. Comments, type
signatures, and functions removed as unreachable have no breakpoint location.
For example, Mandelbrot uses `Range.each`, so `List.range`'s line 57 has no
executable code in that binary. Several lowered statements or activations on
one Fango line can produce repeated stops. Constant-folded expressions do not
necessarily retain individual locations.

Source stepping skips generated Go instructions and runtime helpers, while
still entering Fango callbacks invoked through them. A native Go sidecar is
an atomic boundary for source stepping. Eligible self tail calls keep one
activation: `next` can stop on another iteration, while `finish` runs until
the loop returns. Explicit breakpoints take precedence over `next` and
`finish`, including a breakpoint in a callee.

`next` executes called functions without stopping at their internal Fango
statements. At `main()` in Mandelbrot, stepping over the call therefore runs
the entire render; use `step` to enter it. Skipped calls can produce output or
wait for IO before the next stop. Ctrl-C cancels the pending step, so a later
`continue` runs with only the user breakpoints.

Stepping follows the goroutine at the current stop; other goroutines may run.
User breakpoints in other goroutines still stop execution. This is not an
all-task stepping model. Ctrl-C interrupts execution; an interruption can
stop inside native or generated code, from which `step` seeks the next Fango
location. EOF at the prompt terminates the launched process. Program stdout
and stderr are forwarded; program stdin is EOF because terminal input belongs
to the debugger.

## Inspection and limits

`args` and `locals` display source-visible bindings with their Fango names and
source types. Compiler temporaries and evidence dictionaries are hidden.
`print NAME` looks up one Fango binding in the current frame; it accepts a
name, not a Fango expression. For example, at Mandelbrot line 36, `print real`
shows `real : Float = -2.3` on the first iteration.

The compiler records these mappings, including pattern bindings and closure
captures. Names are not reconstructed from Go prefixes. The argument/local
split follows the native frame, so captured values passed as generated
parameters can appear in `args`. Generic source types retain their type
variables, such as `a`, rather than displaying a Go instantiation.

A source binding absent from the native frame is explicitly unavailable. If
several lexical scopes share a source line and disagree about a binding, it
is also unavailable: the Go line table cannot identify the active expression
on that line. Inspection does not guess between scopes. Move to a distinct
source line or use the raw view to investigate.

`raw args`, `raw locals`, and `raw print EXPR` retain Go names, types, and
read-only Go expressions. Inspection does not inject target function calls.
`stack` also continues to list native frames, including generated frames.

Source inspection renders values structurally in Fango notation: Lists as
`[1, 2, 3]`, records as `{ name = "Ada", age = 42 }`, ADTs as `Just 42`
or `Branch (Leaf 1) (Leaf 2)`, and tuples as `(7, "seven")`. It selects only
the active constructor's fields. Booleans, characters, strings, and unit use
their Fango forms. This also works when the source type is generic. Rendering
reads memory without invoking `Show`, List accessors, or other target code.
Functions display as `<function>`; native resources and unknown
representations remain opaque. The raw view is available for investigation.

Each source value preview is bounded to four structural levels, 50 items per
collection or record, 512 visited values, and 256 debugger reads. Strings load
at most 256 bytes. Truncation uses `…`; cycles and unreadable fields have
explicit markers. These are previews, so truncated or opaque output is not a
complete Fango expression. Raw inspection retains its limits of two nested
levels, 50 children, and 256 string bytes.

Source stepping supports stacks up to
1024 native frames; deeper or unreadable stacks report an error. Temporary
stepping breakpoints are removed after each command, including failure and
interruption. A cleanup failure prevents further execution through the
frontend; quit and restart the session.

The frontend owns its launched process; attaching to an existing process,
restarting, editor integration, and redirecting target input are not yet
supported. Delve must have host permission to debug the child. Its launch
errors are reported and startup waits at most 30 seconds for its socket.
Plain Delve remains useful for Go-level investigation; it does not apply the
Fango frontend's source stepping rules.
