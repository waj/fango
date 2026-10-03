# Interpreter and REPL

Core evaluation, persistent prompt state, imports, generations, transaction boundaries, and handler levels.

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

A prompt expression's checked effect row decides how it runs. IO is ambient;
each `Fail` application gets an abort handler around the input, keyed by its
type argument, and any other effect is an `UNHANDLED EFFECT` before
elaboration. The observed expression evaluates to one tagged String, value or
failure, because only simple values return from the native worker.

Print is ordinary Display-constrained Fango over display and IO.write. Tooling
evaluates an observed expression once. The REPL echoes a result through available
Show evidence, the representation, so a String result is quoted; a program whose
entry is a value prints it through Display evidence, as `print` would. Without
evidence either one shows an opaque typed placeholder or `<function>`, and
observation never adds a constraint to the observed expression.

## Persistent stores and generations

A session retains one checker, fresh-name supply, evaluator environment, module
graph, resolver scope, and buffered IO context. Prompt values become memo cells;
functions become workers. Redefinition creates a new generation without rebinding
old memoized values or closures. Class redefinition is rejected; type redefinition
has fresh nominal identity and may install new instances. Identity allocations are
not reused on rollback.

The prompt and its native host RPCs consume one line pump. Its one reader is a
line source: plain buffered stdin, or in a terminal the line editor, which then
reads program lines as well as prompt lines so no second reader competes for
the terminal. Ctrl-C revokes the current prompt input or signals the active
worker; the editor reads Ctrl-C as a key and cancels the evaluation through the
same path as the signal. A host read interrupted in the middle leaves the line
pump as the sole reader; the next prompt receives the next line. The worker handles the signal without discarding its persistent
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

## Handler levels

A [level](../reference/repl.md#handler-levels) needs the head's handlers to stay
active while the prompt reads more input, but handlers here are synchronous Go
calls with no capturable continuation. So the session parks a running
evaluation instead: it checks `head { patterns -> Runtime.Prompt.level binders }`,
where `binders` packs the patterns' variables as nested pairs, and the evaluator
answers that native call by serving inputs from inside it. The call is a
templated native rather than an intrinsic, since only the REPL's evaluator ever
reaches it; compiled code panics.

The checker's type for the callback lambda gives the level its granted row and
its binders' types, after elaboration's defaulting. Later inputs are checked
against those: a label an input performs must match a granted one, which
unifies their arguments. Binders become prompt values whose cells the
evaluator fills, from the packed value, with the level's first input.

The session keeps one request and reply channel pair. The bottom level runs in
a goroutine, deeper levels in the evaluation parked at the level above, and
each reply carries the depth that produced it. A reply from a shallower level
than the request's means an abort escaped the input and unwound the levels
between, and it answers the outermost of them. An input evaluates in a child
interpreter whose evidence is what the level gave its own input, overridden by
what is in effect at the native call, plus granted effects the callback holds
only in a residual row: a callback with a pure body keeps no evidence of its
own for the effects it was granted.

In the native worker, a parked level is a sequence of `host_level` requests.
Each reports a step, an entry or an input's outcome, and its reply carries the
next input as a payload, or ends the level. The host serves them within the
running exchange, so its lock is never taken twice. Each input is decoded from
its own payload, so a value can outlive the payload that created it: handler
clauses and native wrapper arguments are matched by name, as operations already
were, rather than by pointer. The worker keeps its contexts as a stack and
interrupts only the innermost, and the host never cancels an exchange's
context, which would drop the connection and every level with it.

Ending a level puts back what its names replaced. A level-local definition, one
whose declaration refers to a level's names, is recorded with what its name
meant before. If a shallower level or the session redefines the name, the
record moves to that owner or is dropped, so ending a level never undoes a
later definition outside it.

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
