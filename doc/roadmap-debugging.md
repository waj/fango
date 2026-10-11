# Fango debugging

Proposal for source breakpoints, stepping, and variable inspection. No debugger
commands, debug build profile, or editor debug adapter are implemented yet.
[Roadmap index](roadmap.md). This work extends
[tooling](roadmap-tooling.md), the [interpreter](design/repl.md), and the
[Go backend](design/backend.md#source-positions-and-stack-traces).

## Direction and existing foundations

Use the interpreter for the first Fango-aware debugger, with the REPL as its
terminal interface. Keep the execution controller independent of prompt input
so it can also debug a program from its entry point and serve an editor.
Use Delve for compiled Go binaries, first directly and later through a Fango
adapter if the experiment below supports it. These paths should share source
identity, breakpoint resolution, and value presentation rules where practical;
they need different mechanisms to suspend and inspect execution.

The interpreter already has lexical value frames and a source-aware call
stack. These are separate structures: trace frames do not expose the lexical
environment of each suspended caller. Execution artifacts retain the origins
needed for traces as line-only spans, not a complete source debugging model.
The REPL also has generations, transactional reload, cancellation, and handler
levels whose lifetimes debugging must respect.

Generated Go already carries `//line` directives and reversibly named Fango
symbols. The build invokes ordinary `go build` without explicit stripping
flags. This is a useful starting point for native debugging, but trace
attribution covers selected operations rather than every executable source
expression or binding. Several generated operations can share one Fango line;
some generated lines reset to the Go file.

[Go's debugger guidance](https://go.dev/doc/gdb) recommends Delve over GDB
because it understands Go's runtime, stacks, and values. GDB and LLDB can be
investigated for low-level inspection, but neither is the initial Fango
integration target. Delve already provides a
[Debug Adapter Protocol interface](https://github.com/go-delve/delve/blob/master/Documentation/api/dap/README.md)
for editor clients. That interface exposes Go expressions and representations;
it does not by itself provide Fango expression evaluation or Fango variable names.

## Shared source debugging model

Retain optional debug metadata through checking, elaboration, optimization,
object encoding, and lowering. Identify executable source sites, lexical scopes,
source binders and their types, and the corresponding Core/generated bindings.
Preserve shadowing, pattern-bound variables, closure captures, and lifted local
function identities. Hide evidence parameters and generated temporaries in the
default view, with a raw view available for compiler/runtime investigation.

Resolve a file-and-line breakpoint to an executable site and report its actual
location. A line with no site must be reported as unresolved or explicitly
relocated; do not silently claim it is a verified breakpoint. Internal site
identities must distinguish repeated execution of one line, including tail-loop
iterations, even if the first UI supports only line breakpoints.

Use a source manifest to resolve logical entry/module names and `<stdlib>` paths
to real files without baking consumer-specific absolute paths into shared module
output. Record source content identity so a debugger can detect edits made since
the debuggee was checked. Test the actual paths written to DWARF: Go interprets
relative directive paths relative to the generated Go file's directory
([line directives](https://pkg.go.dev/cmd/compile#hdr-Line_Directives)).

A debug profile should disable Fango inlining and binding elimination where
they destroy the source view. Turning off Go optimization cannot reverse
[Fango's earlier inlining](design/core.md#inlining), which also affects the
interpreter. Preserve required semantic lowering and the
[self tail-call guarantee](reference/functions.md#tail-call-guarantee); tail
iterations update the current frame rather than manufacturing a recursive
history. Decide other optimization policies from concrete stepping fixtures.
Debug profile and metadata version must participate in checked-object,
emission, and link cache identities so ordinary artifacts cannot satisfy a debug
request. Debug and ordinary outputs should coexist without overwriting one
another's running executable or generated sources.

## Compiled-binary feasibility experiment

Before committing to a native adapter, export a small program through the
existing `build --emit-go` command. Build that project with
`go build -gcflags='all=-N -l' -o debug-main ./entries/Main`, then open it with
`dlv exec ./debug-main`. Delve
[recommends these compiler flags](https://github.com/go-delve/delve/blob/master/Documentation/usage/dlv_exec.md)
for prebuilt binaries. This is an experiment using existing export behavior,
not a promised Fango debugger workflow; the export still contains Fango's
optimized Core.

Check file/line breakpoints, next/step/step-out, source lookup, stack names,
arguments, and locals on macOS and Linux. Include an imported module and a
stdlib call, shadowed locals, a match, a closure, a self tail loop, an effect
handler, and a native sidecar. Inspect the binary's line and variable tables
alongside actual debugger stops. Determine which gaps need richer directives,
debug-specific Go emission, binder metadata, or an adapter rather than assuming
that a correct crash trace implies correct stepping.

Start with raw Go names and values. Record exactly what can be recovered about
Fango bindings and value layouts before promising native variable inspection.
Full ADT/record/List presentation, synthetic frame filtering, source-level step
filtering, and Fango watch expressions are follow-up work. Delve path
[substitution](https://github.com/go-delve/delve/blob/master/Documentation/cli/substitutepath.md)
may help source lookup; evaluate it against the generated package layout.

## Interpreter debugger

Add optional execution hooks at explicit source sites, function entry/return,
and tail-loop iteration boundaries. Pausing should block the evaluator at a
safe boundary while retaining its continuation, lexical frames, and evidence.
Do not unwind and replay evaluation: that would repeat IO and change handler,
resource, and cleanup lifetimes. Keep compile-time evaluation outside debugger
sessions.

The first controller should support line breakpoints, continue, step into,
step over, step out, stack selection, and inspection of parameters and locals.
Step over uses the selected activation's identity/depth; step out waits until
that activation exits, including abort routing. Stepping should expose source
evaluation order without stopping on every administrative Core node. Define
whether pauses happen before evaluating a binding or after its value becomes
available, and test that distinction. Treat a native call as an atomic step
initially; report its boundary and do not promise stepping inside Go sidecars.

Inspection should traverse already available values with bounded depth and
collection size. It must not force an unevaluated top-level cell or invoke
user `Show` code automatically. Preserve opaque native values. Frame selection
must obey that frame's lexical scope and source generation, including private
locals, rather than adding those names to the ordinary import interface.
Pure watch evaluation needs a separate checked, budgeted contract and can wait
until inspection works; an empty effect row alone does not ensure termination
or prevent forcing cells.

Debug control must reach the evaluator that owns the suspended frames. The
interpreter can execute callbacks in its persistent native worker, and prompt
levels can run there too. Extend that protocol with pause/control messages and
bounded inspection handles where needed; serializing an ordinary display result
is insufficient. Keep debug controls and program stdin independent so a paused
console read cannot consume debugger commands. Cancel/stop must wake a paused
evaluation and follow the existing cleanup and task-draining rules.

Start acceptance with sequential programs. Specify task behavior before enabling
debugging for concurrent programs: selected-task versus all-task suspension,
cooperative pause boundaries, tasks blocked in native IO or cell forces, stepping
across spawn/join, and cancellation while paused. Do not advertise all-task pause
until the controller can confirm which tasks have stopped. Reload/redefinition
should initially be rejected while execution is suspended; later support needs
explicit breakpoint rebinding and old-generation inspection lifetimes.

## Delivery and verification

Suggested order: native feasibility experiment; shared debug metadata/profile;
sequential interpreter controller and REPL controls; editor integration; compiled
Fango presentation; concurrent debugging. These are work packages, not changes
to existing milestone identities. A failed native experiment should narrow that
path without blocking interpreter debugging.

Use Debug Adapter Protocol for editor integration, with a Fango adapter over
the interpreter controller and, if needed, a translation layer over Delve for
native sessions. Extend the VS Code extension with a Fango debug configuration
and breakpoint support on `.fango` files. Keep language-server analysis separate
from execution control. Exact CLI and REPL command names remain open until the
controller's lifecycle is specified.

Acceptance fixtures should assert stop sequences and visible scoped values,
including multiple expressions on one line, repeated line visits, shadowing,
captures, patterns, handlers/abort cleanup, tail loops, and private helpers.
Compare debug and ordinary execution outcomes and IO across both backends.
Cover cold/warm cache switching between profiles, moved source lines, stale
sources, interruption, worker boundaries, and reload rejection while paused.
Keep the existing [verification gates](design/verification.md#verification-commands).
When each behavior ships, move its contract into reference/design and remove
the corresponding unfinished proposal here.
