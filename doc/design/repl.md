# Interpreter and REPL

Core evaluation, persistent prompt state, imports, generations, and transaction boundaries.

[Design index](../design.md). Source and checks: [Evaluator](../../internal/eval/eval.go), [Session](../../internal/repl/repl.go), [REPL tests](../../internal/repl/repl_test.go), [Failure tests](../../internal/repl/failure_test.go).

## Evaluation

internal/eval executes Core with a uniform Go value representation. Environments
distinguish typed workers, lazy memoized top-level cells, and eager block frames.
Each cell publishes a successful memo under a mutex. Concurrent forces of the
same cell wait for that evaluation to finish; recursive force by its current
evaluator still reports a cycle. Active waits between evaluators form a checked
dependency graph, so a cross-worker force cycle also reports an error rather
than deadlocking. The shared tail-loop analysis cache is also
locked. A per-evaluator instance keeps effect evidence, frames, and execution
steps local to its worker.
EvalIO/ForceIO are explicit IO entry points. List and represented Unit use shared
fangort runtime types. Structural list equality must precede the scalar Go-equality
fallback because a list is deliberately not comparable.

Both backends share observable formatting and the
[self tail-loop predicate](backend.md#self-tail-call-loops). The differential suite
checks agreement. Native call forms use the [persistent worker](backend.md#interpreter-native-worker).

Print is ordinary Show-constrained Fango over show and IO.write. Tooling evaluates
an observed expression once and uses available Show evidence, otherwise an opaque
typed placeholder or `<function>`. Show renders String/Char raw even inside derived
ADTs; tooling literal forms are quoted. Display never adds a Show constraint to the
observed expression.

## Persistent stores and generations

A session retains one checker, fresh-name supply, evaluator environment, module
graph, resolver scope, and buffered IO context. Prompt values become memo cells;
functions become workers. Redefinition creates a new generation without rebinding
old memoized values or closures. Class redefinition is rejected; type redefinition
has fresh nominal identity and may install new instances. Identity allocations are
not reused on rollback.

The prompt and its native host RPCs consume one line pump. Ctrl-C revokes the
current prompt input or signals the active worker. A host read interrupted in
the middle leaves the line pump as the sole reader; the next prompt receives
the next line. The worker handles the signal without discarding its persistent
heap. Each evaluation owns a fresh host context. Interruption cancels that
context and all Async roots derived from it. Outside an Async runner, evaluator
checkpoints restore the prompt. Inside a runner, the [supervisor](tasks.md#async-runtime-foundation)
leaves cancellation to Async operations so language cleanup and child draining
finish before returning an outcome.
Runtime errors leave installed definitions and the worker available for subsequent inputs.

Parser incompleteness/layout drives multiline input. Prompt inputs are sequential,
even though imported module functions have module-wide visibility. Effectful ordinary
declarations are rejected; effectful expressions run and installed functions wait
for explicit calls. Surface limitations and commands live in [REPL](../reference/repl.md).

## Imports and resolution

Every input passes through the batch resolver as a synthetic private module.
Its persistent scope begins with Prelude imports and grows only on accepted inputs.
The checker holds canonical names. Prompt mode permits rebinding prompt-owned names
and cumulative re-imports; other collision/visibility rules are ordinary module rules.

A session bootstraps and imports through the shared compilation session, one
module at a time in dependency order, so its Prelude and syntax roots and every
imported module reuse the same checked objects a build of those sources
produces and publishes. Module values generalize there whatever the prompt's
own monomorphism rule is: that rule is for its memo cells alone. Elaboration
installs stable lifted names, specialization, intrinsics, native metadata, and
solved capture summaries, then lints against everything already installed.
Visibility merges per owner and expands the prompt's set. Sidecar imports
rebuild the worker's module set. An increment records the operator table in
effect when it was resolved; a later increment may widen it without changing
how an accepted input was read.

## Transactions and staging

Every input is a transaction over checker tables, resolver scope, and module graph.
Checkpoints include effects, natives, instance visibility, and the operator table,
restored in place because the graph shares its identity. Extend the evaluator only
after acceptance; staged failures also restore the completion log and evaluator
state. [Metaprogramming](metaprogramming.md#reproducibility-and-rollback) owns that seam.

An input naming several imports prepares them all — resolution, checking,
elaboration, lint, and the native worker its sidecars need — before any of them
reaches the installed set, the evaluator, or the running worker. A failure
part-way therefore installs nothing and retires no worker, and retrying after
the correction behaves like a clean session. The checked objects the successful
modules published are immutable and stay valid, so the retry reuses them.

Core lint checks structural evidence summaries against installed definitions.
Native handles check validity at runtime, using the same rules as batch programs.

Future reload and editing work belongs in the
[tooling roadmap](../roadmap-tooling.md#repl-hardening).
