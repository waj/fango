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
The REPL-only `stop` command is unsupported; `quit` terminates the native process.

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

`frame INDEX` selects a frame from `stack` without running the program and
shows its source excerpt. `next` then runs until that activation reaches its
next Fango location or returns; `finish` runs until it returns. Selecting a
caller therefore runs through younger activations, while explicit breakpoints
and interrupts still take precedence. `step` always starts from the actual
execution point. Every new stop selects frame zero.

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
`print NAME` looks up one Fango binding in the selected frame; it accepts a
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
`stack` lists Fango source frames with zero-based indexes for `frame INDEX`.
Function names use Fango spelling, including modules, operators, local functions,
and instance methods. Closures use their enclosing function's `(lambda)` label,
or `<lambda>` for shared callbacks; native lowering can still produce multiple
such activations. Recursive activations remain separate even at the same line.
Generated and runtime frames are hidden. If execution stops outside Fango,
frame zero remains explicitly `<native>` with its actual native location,
followed by the available Fango callers.

`raw stack` displays the complete native stack with Go function names and
`native #N` indexes for investigation. Those indexes are diagnostic only;
`frame INDEX` always uses the source stack's indexes. Viewing the raw stack
leaves the selection unchanged. All source and raw variable inspection commands
use the selected frame. A native boundary without Fango scope metadata reports
that explicitly; its raw variable view remains available.

Source inspection uses the [shared structural preview rules](debugger-commands.md#structural-value-previews),
including for generic source types. Native decoding reads memory without
invoking `Show`, List accessors, or other target code. Each preview additionally
allows at most 256 debugger reads; pointer and List-spine addresses detect
cycles. Unknown representations remain opaque; the raw view is available for
investigation.

Expandable records, tuples, ADTs, and Lists display `[handle ID]`.
`expand ID [OFFSET]` reads up to 50 children: records use field names; tuple
and constructor fields and List elements use zero-based indexes. Nested
structural values receive their own handles. Child values use the shared
renderer; native child entries do not currently include source type labels.
Functions and opaque resources have no expansion handle. Handles retain the
frame and goroutine where they were obtained, even after `frame INDEX`, and
expire on resume or session exit.

List expansion retains paging checkpoints for the current pause. Each request
visits at most 512 spine nodes and uses at most 256 traversal reads; each child
has its own bounded preview. A distant uncached offset can reach a traversal
limit before producing a page. The diagnostic gives the reached offset;
expand the same handle from that offset to continue. Cyclic or unreadable
spines report a diagnostic instead of pretending the List ends. Native
inspection paths are limited to 16 KiB; loaded addresses normally keep paths
short across pages. Expansion never invokes target functions.

Raw inspection retains its limits of two nested levels, 50 children, and
256 string bytes.

Source stepping supports stacks up to
1024 native frames; deeper or unreadable stacks report an error. Temporary
stepping breakpoints are removed after each command, including failure and
interruption. A cleanup failure prevents further execution through the
frontend; quit and restart the session. After another failed movement command,
the debugger verifies the process state with Delve. If that state cannot be
verified, further inspection and execution report that the debugger is not
stopped; quit and restart rather than inspecting a stale frame.

The frontend owns its launched process; attaching to an existing process,
restarting, editor integration, and redirecting target input are not yet
supported. Delve must have host permission to debug the child. Its launch
errors are reported and startup waits at most 30 seconds for its socket.
Plain Delve remains useful for Go-level investigation; it does not apply the
Fango frontend's source stepping rules.
