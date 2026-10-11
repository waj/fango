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

- A raw inspection view for evidence parameters and generated temporaries.
- Disk path mappings for raw Delve source lookup, plus the binding and scope
  mapping described under [native variable presentation](#native-variable-presentation).
  The native frontend already uses binary-matched source snapshots;
  [debug emission and cache isolation](design/debugging.md#native-source-stepping)
  preserve ordinary outputs and the self tail-call guarantee.
- Measure stepping gaps from semantic lowering and extend explicit sites where
  a concrete fixture needs finer source attribution.

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
  appear. This establishes raw tables, not faithful Fango variable presentation.
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
synthetic-frame filtering and Fango watch expressions separate from
source-stepping support. A native launch limitation does not block interpreter
debugging.

## Native variable presentation

Present native arguments and locals using their Fango names, types, and
scopes. This requires compiler metadata and a presentation layer; the current
source-stepping frontend retains raw Go variable names and representations.
Proposed implementation order:

1. Extend the versioned, binary-matched source manifest with a mapping from
   generated bindings to original Fango names, types, and lexical scopes.
   Account for captures and inlined bindings; stripping `v_` alone
   cannot recover those relationships. Mark unavailable or optimized-away
   bindings explicitly, and reject a mapping from a different build.
2. Prototype Delve terminal helpers using its
   [Starlark commands](https://github.com/go-delve/delve/blob/master/Documentation/cli/starlark.md),
   which can inspect arguments and locals. Proposed `flocals` and `fprint NAME`
   commands would display source names, resolve a name in the selected frame,
   and hide compiler temporaries by default, with access to a raw view.
   These are custom commands: ordinary Delve commands would continue to use
   Go names and expressions. Start with binding lookup, not arbitrary Fango
   expression evaluation.
3. Add bounded structural rendering for ADTs, records, and Lists, such as
   `Just 42` and `[1, 2, 3]`, using type and representation metadata. Inspect
   memory without invoking `Show` or other target functions. Preserve raw
   inspection and report unreadable or truncated values explicitly.
4. After terminal presentation is validated, reuse its mapping and rendering
   rules alongside the native source controller in a Fango editor adapter for
   editor variables, hover inspection, and source-level stack names. Keep
   general watch expressions as distinct work.

Verify original-name lookup in the selected frame, closure captures, pattern
bindings, inlined and unavailable bindings, synthetic-variable filtering,
representation decoding, and mismatched-build rejection. Confirm that
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

Use DAP for editor integration, with a Fango adapter over the interpreter
controller and the native source controller over Delve, extended with the
[native presentation layer](#native-variable-presentation). Add a
debug configuration and `.fango` breakpoint support to the VS Code extension;
keep language-server analysis separate from execution control.

Retain the [existing verification gates](design/verification.md#verification-commands).
Extend stop-sequence and value assertions with each capability, including
source generations, repeated line visits, captures, patterns, handler aborts,
cleanup, private helpers, cache switching, interruption, and worker boundaries.
Native presentation and concurrent execution require their own acceptance
fixtures rather than inheriting the sequential debugger's claims.
