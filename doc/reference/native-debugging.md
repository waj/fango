# Native source debugging

`fango debug BINARY [--] [args...]` launches a binary built with
[`fango build --debug`](commands.md#native-debug-builds), using Delve on macOS
or Linux. It stops at the first executable Fango location reached from main
and presents a `debug>` prompt. For the repository example:

```sh
./fango build --debug examples/mandelbrot.fango
./fango debug ./mandelbrot
```

The companion source snapshots supply excerpts, so source files do not need
to be present in the current working directory. The displayed text belongs
to the binary's build, even if the files have since changed. Rebuild and start
a new session to debug changed source. Startup does not stop in module
initialization before main.

## Stepping and breakpoints

Commands have no leading colon. Blank input repeats the previous command.

| Command | Behavior |
| --- | --- |
| `step`, `s` | Stop at the next Fango source location, entering calls and callbacks |
| `next`, `n` | Advance in the current activation, skipping called activations |
| `finish`, `f` | Resume until the current activation returns or unwinds, then stop at the next Fango location |
| `continue`, `c` | Run until a user breakpoint, interruption, or process exit |
| `break FILE:LINE`, `b FILE:LINE` | Set a breakpoint at a linked Fango statement |
| `breaks` | List user breakpoints |
| `delete ID` | Remove a user breakpoint |
| `help`, `h` | List commands |
| `quit`, `q` | Terminate the launched process and debugger |

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

Stepping follows the goroutine at the current stop; other goroutines may run.
User breakpoints in other goroutines still stop execution. This is not an
all-task stepping model. Ctrl-C interrupts execution; an interruption can
stop inside native or generated code, from which `step` seeks the next Fango
location. EOF at the prompt terminates the launched process. Program stdout
and stderr are forwarded; program stdin is EOF because terminal input belongs
to the debugger.

## Inspection and limits

`args` and `locals` inspect the current frame. `print EXPR` evaluates a
read-only Go expression there; it does not inject target function calls.
`stack` lists native frames, including generated frames. Names, types, and
values currently retain their Go representation, for example `v_real`.
Fango name translation, value rendering, and stack-frame filtering remain
[roadmap work](../roadmap-debugging.md#native-variable-presentation).

Inspection bounds string previews to 256 characters, collection children to
50, and nested values to two levels. Source stepping supports stacks up to
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
