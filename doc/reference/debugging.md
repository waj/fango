# Debugging in the REPL

Start a sequential debugging session with `fango repl --debug [dir]`. The
profile applies to the whole session, including imports and later reloads.
It retains source bindings and disables Fango inlining and scalar
specialization. Ordinary REPL sessions keep their usual profile.

[Reference index](../reference.md). [REPL](repl.md) owns imports, evaluation,
handler levels, and redefinition. [Architecture](../design/debugging.md).

## Breakpoints

At either prompt, `:break FILE:LINE` sets a breakpoint and returns its ID.
Use a source-root-relative module path such as `Geometry/Point.fango`, an
absolute path beneath the source root, or `<stdlib>/List.fango`. A stopped
prompt definition shows a content-specific name such as `<repl:abc123def456>`;
that name can also be used in a breakpoint.

Breakpoints bind to executable expressions starting on exactly the requested
line. Blank lines, comments, and declarations without executable sites remain
unresolved; the debugger does not move them to another line. An unloaded file
is pending until imported. `:breaks` lists IDs and their status; `:delete ID`
removes one. Several expressions on one line can produce several stops.

A breakpoint identifies a checked source snapshot. A changed file or a reload
with different source invalidates it. Reload as appropriate, then set the
breakpoint again to bind the current snapshot. Retained closures continue to
show their original source, and a breakpoint bound to new source does not
match an older generation. Prompt definitions retain distinct source names
when later inputs are entered.

## Running and stepping

Evaluate an expression normally to run until a breakpoint. `:step EXPR` starts
stepping an expression from the ordinary prompt. At a stop the debugger prints
the reason, current source location and excerpt, and a `debug>` prompt.

| Command while stopped | Action |
| --- | --- |
| `:continue` | Resume until another breakpoint or completion |
| `:step` | Stop before the next source expression, entering Fango calls |
| `:next` | Stop at the next expression in the selected activation, stepping over calls |
| `:finish` | Resume until the selected activation exits, then stop at the next source expression |
| `:stack` | List source frames, innermost first |
| `:frame INDEX` | Select a frame for inspection and `:next`/`:finish` |
| `:stop` | Cancel the current input and return to the ordinary prompt |
| `:quit`, `:q` | Cancel the input, end handler levels, and leave the session |
| `:help` | List debugger commands |

Stops occur **before** evaluating the indicated expression. A binding becomes
inspectable in subsequent expressions after its initializer completes. Call
arguments have their own sites. Breakpoints take precedence over step-over and
step-out requests. Finishing the input returns directly to the ordinary prompt
when there is no next source expression.

Native Go sidecars are atomic boundaries; stepping does not enter their Go
code. Eligible self tail calls reuse one activation and update its parameters,
so their iterations do not accumulate stack frames. Compile-time splices run
outside the debugger. Task creation and parallel mapping fail with
`debugger: concurrent task creation is not supported` in this profile.

## Inspecting values

`:locals` lists the selected frame's parameters, available lexical locals, and
captures, with their source names and types. `:print NAME` inspects one such
binding. It accepts a name, not an expression. It does not force global cells,
run `Show`, or add private names to the REPL scope.

Previews use at most two structural levels, 50 children per collection, and
256 characters per string. Records, ADTs, and materialized lists expose
structural children. Functions and native/resource values remain opaque.
Expandable values show a handle: `:expand HANDLE [OFFSET]` returns up to 50
children from that offset. Handles expire on every resume, step, or stop.

## Input, cancellation, and handler levels

While paused, only debugger commands are accepted. Declarations, expressions,
imports, reloads, and handler-level mutations are rejected. Pausing preserves
the evaluator and its worker heap; resuming never replays IO.

Program input and debugger input share the REPL's input pump but are requested
separately: a debugger command is read only while execution is stopped.
Ctrl-C cancels the active input, including a suspended one. Ctrl-D at an
interactive debug prompt also cancels only that input; EOF in a piped session
cancels it and exits. Cancellation follows existing resource cleanup rules.
Existing handler levels remain available after cancelling their current input.

For native processes, use [`fango build --debug` and `fango debug`](native-debugging.md).
