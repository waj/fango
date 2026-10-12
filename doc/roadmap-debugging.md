# Fango debugging

The sequential interpreter debugger is implemented in `fango repl --debug`;
see its [reference](reference/debugging.md) and [architecture](design/debugging.md).
[Native source stepping](reference/native-debugging.md) is implemented through
Delve. This page owns remaining presentation, platform, editor, and concurrency work.
[Roadmap index](roadmap.md).

## Direction and existing foundations

Use the interpreter controller for interpreted execution and the Fango source
controller over Delve for compiled Go binaries. The transport-independent
control messages can support an interpreted entry-point launcher and an editor adapter without coupling execution to
terminal input. These are follow-ups to the REPL interface.

[Go's debugger guidance](https://go.dev/doc/gdb) recommends Delve over GDB.
Delve also provides a [DAP interface](https://github.com/go-delve/delve/blob/master/Documentation/api/dap/README.md),
but it exposes Go expressions and representations. GDB and LLDB remain
possible low-level investigation tools, not the initial Fango adapter target.

## Shared source debugging model

Interpreter source snapshots, lexical binding sites, profile-aware checked
objects, and exact-line breakpoint matching are
[implemented](design/debugging.md#metadata-and-profile). Remaining work:

- A raw interpreter inspection view for evidence parameters and generated temporaries.
  The native frontend already has [raw inspection](reference/native-debugging.md#inspection-and-limits).
- Disk path mappings for raw Delve source lookup, plus finer scope
  attribution described under [native variable presentation](#native-variable-presentation).
  The native frontend already uses binary-matched source snapshots;
  [debug emission and cache isolation](design/debugging.md#native-source-stepping)
  preserve ordinary outputs and the self tail-call guarantee.
- Measure stepping gaps from semantic lowering and extend explicit sites where
  a concrete fixture needs finer source attribution.

## Unified debugger frontend

The [shared session API](design/debugging.md#shared-debugger-sessions) and
[terminal frontend](design/debugging.md#shared-terminal-frontend) now serve
interpreter and Delve adapters. Requests, stop events, source snapshots,
breakpoint statuses, selected frames, and pause-scoped handles use structured
data, with parser, session, adapter, and backend transcript tests.
[Structural value descriptions and rendering](design/debugging.md#shared-structural-values)
are also shared, with bounded backend readers and one Fango formatter.
Native frame selection, caller stepping, and paged value expansion follow the
[inspection contract](reference/native-debugging.md#inspection-and-limits).

Remaining steps:

- Close capability gaps, including source type labels on native expansion
  children and consistent argument/local views. Advertise
  supported operations explicitly instead of silently approximating unsupported ones.
- Reuse this session API for the editor adapter described under
  [delivery](#delivery-and-verification).

Preserve the implemented [session invariants](design/debugging.md#shared-debugger-sessions)
as these layers evolve: source precision, stale-reference rejection, native
next/finish performance, tail activations, worker heap ownership, source
generations, pending imports, host cleanup, and ordinary-build isolation.
Matching command vocabulary must not imply identical expression stop
sequences or concurrency semantics.

## Compiled-binary feasibility experiment

The [opt-in harness](design/debugging.md#native-experiment-harness) builds a
fixture through `fango build --debug`, applying `-gcflags='all=-N -l'`, as
[Delve recommends](https://github.com/go-delve/delve/blob/master/Documentation/usage/dlv_exec.md).
Run `nix develop --command make test-debug-native` on a host
allowed to debug its child processes.

Evidence on macOS ARM64 with Go 1.26.7 and pinned Delve 1.27.1:

- Debug build, the fixture's result (`94`), and live source stepping pass.
- DWARF contains `Main.fango`, `Probe.fango`, and `<stdlib>/...` paths, including
  List. These are logical paths, not an exported copy of the Fango sources.
- Generated bindings include `v_n`, `v_acc`, `v_offset`, `v_seed`, and closure
  parameters. Synthetic evidence, adapter, and result variables also
  appear in the raw view. The frontend uses compiler mappings for Fango names
  and source types, including captures and pattern bindings.
- Live tests cover entering stdlib, next, finish, callback breakpoint
  precedence, a native sidecar, a tail loop, and process completion. Mandelbrot
  steps through coordinate calculations at lines 35–37 and returns to Range.
- `FANGO_DEBUG_NATIVE_DWARF_ONLY=1` runs static checks independently of host
  debugger access.
- Linux AMD64 execution remains unverified; run the same target there.

Complete the live platform matrix before broadening native support:
file/line breakpoints, next/step/step-out, source lookup, stack names,
arguments, and locals. The fixture covers an imported module, stdlib calls,
matching, closures, a self tail loop, an effect handler, and a native sidecar.
Fango rejects lexical shadowing, so generation/redefinition coverage belongs
to interpreter sessions rather than an invalid native fixture.

Evaluate Delve [path substitution](https://github.com/go-delve/delve/blob/master/Documentation/cli/substitutepath.md)
and actual stop paths. Keep [native variable presentation](#native-variable-presentation),
finer source-lambda attribution for lowering callbacks, and Fango watch
expressions separate from source-stepping support. Source stack naming and
generated-frame filtering follow the
[native inspection contract](reference/native-debugging.md#inspection-and-limits).
A native launch limitation does not block interpreter debugging.

## Native variable presentation

[Source-name lookup, bounded Fango value rendering, and raw inspection](reference/native-debugging.md#inspection-and-limits)
are implemented in `fango debug`. The binary-matched manifest carries emitted
binding names, source types, lexical scopes, representation layouts, and
expression spans. Unavailable bindings are explicit, and compiler temporaries
are hidden by default. Source excerpts underline recorded expressions,
label enclosing spans, and report ambiguous locations. Remaining work:

1. Disambiguate expression scopes sharing a source line with finer linked
   location metadata. Current lookup conservatively reports a name unavailable
   when those scopes disagree. Resolve precise active subexpressions for
   underlining instead of the current enclosing-span or unavailable fallback.
   Preserve capture and pattern-binding mappings
   and validate against any future debug-profile inlining.
2. Optional Delve terminal helpers using its
   [Starlark commands](https://github.com/go-delve/delve/blob/master/Documentation/cli/starlark.md)
   could reuse the manifest outside `fango debug`. Ordinary Delve commands
   continue to use Go names and expressions; the Fango frontend already
   provides name lookup, filtered locals, and a raw view.
4. After terminal presentation is validated, reuse its mapping and rendering
   rules alongside the native source controller in a Fango editor adapter for
   editor variables, hover inspection, and source-level stack names. Keep
   general watch expressions as distinct work.

Retain coverage for original-name lookup, closure captures, pattern bindings,
unavailable bindings, synthetic-variable filtering, and mismatched-build
rejection. Extend it with exact same-line scopes, any future inlined bindings,
and additional representation decoding cases. Confirm that
inspection does not execute target code and that raw inspection remains
available. Native fixtures must establish these behaviors independently of
the interpreter debugger.

## Interpreter debugger

Sequential breakpoints, stepping, frame selection, bounded inspection,
cancellation, handler-level requests, and worker control are
[implemented](reference/debugging.md). Remaining extensions:

- Launch an interpreted program directly from its entry point.
- Checked and budgeted watch expressions. An empty effect row alone does not
  guarantee termination or prevent forcing top-level cells.
- Reload or redefinition while suspended, with explicit old-generation
  inspection lifetimes and breakpoint rebinding. The current interface
  rejects all such mutations.
- Concurrent debugging: selected-task versus all-task suspension, safe points
  in native IO and cell forces, spawn/join stepping, and cancellation while
  paused. Task creation and parallel map are currently rejected in debug mode.

## Delivery and verification

Extend the native launcher with process attachment, restart, and redirected
target input.

Use DAP for editor integration over the
[shared session API](design/debugging.md#shared-debugger-sessions), backed by the interpreter
controller and native source controller over Delve. Add a
debug configuration and `.fango` breakpoint support to the VS Code extension;
keep language-server analysis separate from execution control.

Retain the [existing verification gates](design/verification.md#verification-commands).
Extend stop-sequence and value assertions with each capability, including
source generations, repeated line visits, captures, patterns, handler aborts,
cleanup, private helpers, cache switching, interruption, and worker boundaries.
Native presentation and concurrent execution require their own acceptance
fixtures rather than inheriting the sequential debugger's claims.
