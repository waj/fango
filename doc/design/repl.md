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

A value declaration's row is granted the same way, through a checker hook the
session installs. The hook runs before the declaration's predicates are
qualified, so a label's type arguments unify with the level's grant rather
than defaulting first. A declaration whose row holds labels is installed and
then forced once, by evaluating a tagged outcome that reads the value under
the same abort handlers, in the evaluator that holds the levels. A cell
memoizes only on success, so a failure, a runtime error, or an interruption
leaves nothing to undo but the installation, which the session restores from
the names' prior bindings before any level it may have ended is popped.

Print is ordinary Display-constrained Console code over `display` and the
`Console` effect's byte write; the session's console runner answers it with
the session's standard streams. An input or an effectful value definition
that uses the console is checked a second time under `Console.runSystem`, so
one that does not keeps its own shape, laziness, and stack. Tooling evaluates
an observed expression once. The REPL echoes a result through available Show
evidence, the representation, so a String result is quoted. Without evidence
it shows an opaque typed placeholder or `<function>`, and observation never
adds a constraint to the observed expression.

## Call stack

The interpreter keeps the call stack generated Go reads from its line table
([backend](backend.md#source-positions-and-stack-traces)), so both backends
report one trace. Each evaluator holds frames outermost first: a worker call
pushes the definition's name as a trace spells it (`fangort.SymbolName`
recovers lifted locals and instance methods), a closure call pushes the label
its lambda was created with, and a forced top-level value pushes a hidden
frame, since generated Go runs those initializers in package `init`, which a
trace never shows. Before every call, perform, and native call the executing
frame records the node's `Origin`, so a frame reports the line it is at,
tail loops included, and the innermost frame reports the crash site. A
lambda created inside a worker is `worker (lambda)`, one created inside a
hidden value frame is `Module.<lambda>` (Go names both `init.funcN` and
shared callback bodies without an enclosing function), and a lambda nested in
a lambda keeps its label, as Go's `.func1.2` keeps the enclosing name. A
call site with no frame at all, a prompt input, opens an `<expression>` frame.
Child evaluators for tasks, parallel maps, and prompt levels start from a
copy of their parent's stack.

A failure acquires its stack once, at the first frame boundary it crosses,
as a `TraceError` whose message carries the rendered trace, so it survives
the worker protocol as text; exits, cancellation, and the compile-time
restrictions are not failures and keep their types, and compile-time
evaluation never traces, since its errors are diagnostics. Go panics inside
`EvalIO`, `ForceIO`, and prompt levels are recovered into the same error
while the frames are still on the stack. `Trace.captureRaw` is answered from
the stack rather than its sidecar, which in the worker would see only Go
frames. The execution payload keeps exactly the origins the stack reads, as
line-only spans ([object encoding](pipeline.md#pipeline)); every other span
is dropped as before.

Language aborts remain separate from runtime errors. When a resolved handler
entry requests diagnostics, `ControlExit` records its source site and copies
the interpreter's current frames before returning the exit. Ordinary handlers
do not copy the stack. Reporting binds the stored primary frames; detached
snapshots keep capture presence and each suppressed exit's frames, matching
the compiled [abort protocol](effects.md#abort-and-cleanup-protocol).

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
even though imported module functions have module-wide visibility. Effectful
expressions run, effectful value declarations run once when entered, and
installed functions wait for explicit calls. Surface limitations and commands
live in [REPL](../reference/repl.md).

A value an effectful declaration computed carries a session-unique stamp.
Forcing a stamped cell seals its memo under the name and stamp in the
evaluator that forced it, and a program that reinstalls a stamped value
reinstalls the sealed memo for the same stamp instead of a fresh cell: that is
what keeps the value across the per-input program the session ships to its
native worker. Once the value is sealed, its shipped body becomes a trap,
`Runtime.Prompt.stale`, so an evaluator that never sealed the memo, a worker
started or rebuilt after the declaration ran, fails rather than performing
the effect again. A redefinition or a pure binding of the name drops its
stamp; a binding put back brings its stamp, and so its memo, back with it.
Such a value is not added to the compile-time evaluator's prefix, as a
level's binders are not, since its body cannot run again.

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
Visibility merges per owner and expands the prompt's set, in one direction:
the prompt's names are bare, as a private module's are, and the checker
treats a bare name as invisible while a named module is checked, so an
imported module's binders are measured against the module's own scope and a
prompt declaration cannot shadow-collide with them. Sidecar imports rebuild
the worker's module set. An increment records the operator table in
effect when it was resolved; a later increment may widen it without changing
how an accepted input was read.

Interactive import progress is session-owned presentation over the shared
compilation observer, composed with the caller's observer. It is active only
during prompt imports, so Prelude bootstrap events remain silent. Resolution
starts under the requested module's name; check starts and checked-cache hits
update the current module. Progress is cleared before diagnostics, and the
summary is printed only after the entire input commits. Noninteractive sessions
retain dependency-ordered module echoes; exact output belongs to the
[REPL reference](../reference/repl.md#imports).

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

## Transactional reload

The session records accepted prompt imports and fixity declarations.
`:reload` discovers a separate graph rooted in those imports and the Prelude
and syntax roots. Local files are read again; bundled files are reparsed from
their retained bytes because checking may rewrite a resolved AST. The candidate
rebuilds interfaces and fixities together, prunes unreachable dependencies, and
reapplies imports before copying prompt-owned resolver names. Vanished explicit
selectors are pruned; all other resolution errors reject the candidate.

The shared installer checks this graph in a fresh checker using the session's
monotonic identity supply and the ordinary module-object cache. Preparation
publishes no resolver or runtime state. Cancellation is checked between stages
and before commit. Valid immutable cache objects may survive rejection.

Checked nominal contracts are compared without source positions or allocation
numbers. Matching types, effects, and class dictionaries retain their previous
identities only while the nominal declarations their contracts reference also
retain theirs. A fixed point handles recursive contracts. Reconciliation copies
the candidate compiler graph, preserving sharing and recursive metadata, and
rebinds reflected schemas to the adopted checker. Cached objects remain
unchanged. Prompt-owned declarations and historical nominal metadata are merged
after current modules pass validation; retained instances for retired types or
classes remain available without competing with replacement instances over
unchanged types.

Runtime module definitions have session-only generation names. All module calls
inside accepted prompt Core are frozen to those names before installation;
direct expressions use the current mapping. Changed modules and their transitive
dependents receive new runtime definitions, including dependents whose checked
objects were cache hits. Unaffected modules keep their runtime generations.
Historical helpers, dictionaries, memo cells, and worker definitions remain
installed until session close. Source dependencies are retained for reporting
even when inlining or sealing an effectful value removes their Core calls.

Staging is retained as completed Core, without replaying prompt declarations.
Historical stage calls receive generation names; new quotation indices are
rebased after the retained template table. New splices see current module
definitions; retained prompt generators keep their old helpers and resolved
evidence. New instance selection continues to use the current declaration view.

The final commit replaces graph, resolver, checker, staging, and installer state,
then installs the prepared runtime definitions. It neither evaluates user
expressions nor replaces the native worker. Active handler levels reject reload;
changed sidecars or incompatible native contracts require a new session.
Ordinary imports also refuse a worker replacement while a level is parked,
avoiding retirement of a worker whose exchange lock is held by that level.
Observable output and limitations belong to the
[reload reference](../reference/repl.md#reloading-modules); further editing work
belongs in the [tooling roadmap](../roadmap-tooling.md#repl-hardening).
