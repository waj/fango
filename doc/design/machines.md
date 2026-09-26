# Machines, streams, and cursors

Selective state-machine lowering, frame lifetime, pull ownership, and failure inspection.

[Design index](../design.md). Source and checks: [Machine IR](../../internal/machine), [Runtime dispatcher](../../runtime/fangort/machine.go), [Cursor runtime](../../runtime/fangort/iterator.go), [Machine tests](../../internal/codegen/machine_test.go), [Lifecycle tests](../../internal/eval/iterator_lifecycle_test.go), [Failure tests](../../internal/repl/failure_test.go).

## Selective lowering

`internal/machine` is a typed execution IR below semantic Core. It emits a
Machine member for every worker and function closure, including fixed Direct
and Exit definitions. Their ordinary members remain the synchronous path;
the Machine members preserve continuations when scheduled Work reaches them
through direct calls, stored callbacks, or module imports. The defining module
owns these members independently of consumers. A synchronous cursor owner
still runs through its ordinary member unless a scheduled Work advance reaches it.

Lowering splits ANF Let/Seq/If computations at suspensions and Machine calls,
shares branch continuations, and marks calls whose only continuation is return
as tail transfers. Blocks have one explicit terminator and typed result binding.
Coverage includes decision trees with edge-specific field bindings, generic
known workers, indirect callbacks, Direct/Exit expressions, and suspension.

Backwards fixed-point liveness computes LiveIn/LiveOut. Frames store parameters
and locals live across suspension/non-tail calls; a transition's returned result
is defined on its outgoing edge, not saved before it exists. Machine lint
independently recomputes liveness/layout, reachability, successors, and types.
It proves one cleanup depth at every normal join and no worker-owned pending
cleanup at return, and checks retained semantic evidence/ownership contracts.
ANF-generated references retain their local-variable identity so liveness also
preserves intermediate call results used by later constructor expressions.

Factories and frame fields use the same value representation as ordinary code.
Open callbacks retain a Machine member even inside otherwise Direct definitions.

## Dispatch and frame lifetime

The evaluator has an explicit frame slice and uses recursive Core evaluation
only for non-Machine expressions that finish before the next transition. Go
emits typed frames with PC, parameters, and computed live locals. Step selects
its entry or resumption block from the saved PC, then uses direct Go jumps for
local edges. It returns only for suspension, call/tail transfer, return,
or exit; only the shared runtime dispatcher invokes Step.

Calls requiring a frame push it; tail calls replace the active frame. Return uses one erased
runtime register that the typed caller projects. Advancement uses a separate
typed `CursorResult` register, consumed and cleared without interface boxing;
terminal clearing and handled exits clear that register as well.
Exported module-owned frame
constructors avoid runtime imports of generated packages and preserve the DAG.
Tail-call arguments, captures, and evidence evaluate before clearing the old
frame. Slices hold pointers/interfaces to separately allocated frames and handler
boundaries retain integer depths; no pointer to a relocatable slice slot escapes.
Statistics expose maximum live depth, frame capacity, cleanup, and state storage.

Generated self-tail calls at identity type instantiation can reset the current
frame instead of allocating a replacement. The complete argument/evidence tuple
is evaluated before overwriting it; unused fields are cleared. Re-entry returns
MachineContinue to the dispatcher, giving each iteration a fresh Step activation
and preserving captured-local snapshots. Stateful clause frames and captured
completion calls retain their ordinary path.

Machine callables and operation records return a lazy MachineStart value: a
frame, deferred synchronous work, or an owned pause request. Ordinary calls save
their result continuation before entering it. Deferred work executes on its own
dispatcher turn without an adapter frame; pause returns to the same saved result
edge after receiving its reply. Owning and completion boundaries materialize an
entry frame when needed. Factories never execute user code.

Packaging a cursor as Work gives it a shared poll budget. Each 256 Machine
dispatch steps, before the next frame step, the dispatcher returns an internal
poll event. Nested cursor and cleanup machines borrow the same budget. Traversal
parks the entire live frame stack at the Work cursor, restores its evidence,
and reports `Polled` to the driver. The next advance resumes those frames
without consuming the poll reply as a suspension answer. This is an execution
capability of Work packaging, independent of Async names. Synchronous calls
outside packaged Work have no poll budget. A single opaque native call or an
individual primitive evaluation is not interrupted by these dispatch polls.
The interpreter keeps its compile-time budget and ordinary host interruption
checks; scheduled Work lets its driver route interruption through cleanup.
The [typed reply fixture](../../testdata/run/work_poll_reply.fango) checks that
both backends return `Polled` and preserve the pending reply through resumption.
The [close fixture](../../testdata/run/work_close_poll.fango) checks that a
synchronous close drains CPU-heavy cleanup before returning.

Suspension needs retained continuation state, but does not require a new heap
frame for every source call. Self-tail re-entry reuses the same state storage;
primitive work without independent continuation state uses its caller's saved
result edge. Both paths preserve dispatcher boundaries rather than recursively
executing the next continuation inside a factory.

The Go emitter can avoid an intermediate advancement result when its only use
is an immediate exhaustive protocol match, following identity bindings. It
selects the suspended/finished/closed/polled edge and binds its typed payload directly;
failure is propagated first. The same non-escape check removes a constructor
immediately consumed by a case, including conversions between different ADTs.
An escaping or separately observed result keeps its ordinary representation.
The checked Machine graph remains the reference, and the emitter proves this
local use restriction before bypassing its packaging blocks.

Identity bindings and Unit erasure on a return path also permit a tail
transfer. In particular, a stateless forwarding handler need not retain an
otherwise empty continuation across a pause. A bounded entry check additionally
recognizes atomic aliases followed by a tail call to a tagged pause capability.
Its start factory forwards the typed request and lexical owner directly, without
a handler or pause frame. Identity argument adapters retain the tag only when
both request and reply types are unchanged; forwarding an existing row is allowed,
but extending evidence is excluded. An opaque callback takes the ordinary frame
path; no factory recursively invokes its unknown callee. State updates, cleanup, and
observable work prevent this forwarding shortcut.

Completed frames are cleared. At suspension/non-tail boundaries the evaluator
drops locals outside LiveOut; Go zeros the frame and copies back only live fields.
Closures over mutable locals snapshot only referenced values and can retain both
ordinary bodies and Machine factories. Calling selects a protocol; storage does
not erase either representation. A recursive binding ties its self-reference in
that snapshot; its surrounding body still evaluates in the full lexical frame.

Producer execution shares its caller's policy and step counter across Core,
tail loops, and dispatch, including loops that never yield. Reopening a producer
cannot reset a staging budget or permit a forbidden native. Dispatch restores
caller evidence on suspension/completion. Evaluator errors drain
cleanup and retain cleanup errors while attempting outer releases. Terminal and
protocol-error paths clear frames, state, handlers, and suspension storage.

## Handlers and cleanup

Machine handler bodies and clauses are separate typed workers with lexical
evidence in frame fields. Stateful clauses carry an opaque cell token.
When a nested cursor starts under a live handler, its Machine inherits a view
of the parent's state stack. Captured clause operations update the owning
Machine's cell through that view, including across foreign suspension.

An activation the body reaches at Direct is the exception: its clauses neither
exit nor suspend, so they stay ordinary closures and only the body is a machine
region. Their state is one escaping variable they share, held through the
machine's state stack so unwinding still accounts for it and the return clause
reads the final value; the locals they read are snapshotted where the
activation is installed and are live there. This is what lets a closure bound
to such an activation keep its own Direct protocol inside a suspending
computation. Abort
routing unwinds only to its exact target before invoking the clause.

A Machine Bracket has Machine acquisition and release callbacks. After
successful acquisition it registers a release factory before starting the body;
failed acquisition retains responsibility for its own partial cleanup. A
release starts as a child Machine and may suspend. The cleanup stack pops each
obligation exactly once, then waits for that child to finish before proceeding
to the outer obligation. The closing owner retains captured state and lexical
evidence throughout the wait. Exit routing gives a cleanup failure to its
definition-site handler and preserves typed primary/suppressed order when it
remains unhandled.

Explicit abandonment builds a stop Machine from the unfinished advancement
chain, innermost first. It replays a pending release request for the driver,
then resumes the release with the reply. A cursor or scope is cleared only after
the drain returns; a release that never returns keeps it live.

## Cursor owner and advancement

[Stream and Iterator](coroutines.md#ordinary-pull-libraries) are ordinary
library wrappers over typed coroutines. Construction and arguments are strict,
while production begins on first pull. Core has only the general
CoroutineScope/CoroutineAdvance boundaries; no Stream or Maybe protocol is
recognized by lowering.

Both backends open a private pull owner with a lazy pause factory and pass its
handle to the driver. Advance supplies the initial input or a suspended call's
reply and drives to a request, completion, or exit. The generated caller and
interpreter package requests/completion into checked Step constructors; close
returns Unit. Machine lint independently checks access, evidence, protocol
types, descriptors, and live locals. `stop` uses the same Step protocol to
expose cleanup requests; `advance` supplies their replies.

Each dispatcher has an iterative producer/caller transfer stack. A pause to an
enclosing owner parks unfinished inner advancements there, retaining their
exclusive borrows. Abandonment drains inner producers before callers and clears
transfers. CursorOpen retains the factory and registers closure without running
producer instructions. CursorClose and unwind share the cleanup protocol. Lint
tracks exact lexical cursor identities, rejecting a different owner or an
ordinary cleanup pop in place of cursor closure.

The producer's pause callback retains a fresh runtime owner token. Workers,
factories, and closures preserve it; no ambient current-cursor slot chooses the
destination. Interpreter frame factories are keyed by the exact semantic lambda
identity; the REPL lowers the same expression it displays/evaluates. Private
host fixtures can also drive unowned Machine suspension directly.

Drive and Suspension are owned control labels without runtime evidence
parameters. Owner-sensitive checking determines which obligations a lexical
boundary consumes; see the [control proof](coroutines.md#protocol-and-control-proof).
A synchronous Direct/Exit boundary drives its Machine consumer, registering
producer closure before the first step. It returns an ordinary value/exit
after closing and rejects unexpected foreign suspension.

## Residual rows during pulls

The cursor's stable forwarding reference selects the current pull's residual
row. Immutable extensions preserve lexical evidence. Completed pulls restore
the boundary row; unfinished advances retain their reference across foreign
suspension. Abandonment restores nested boundaries before cleanup; completed
cursors clear references. Empty forwarding adds no link and subsequent pulls
replace the cursor link instead of accumulating chains. A Machine producer may
call a synchronous interpretation without changing that interpretation's lexical
evidence. The [Core row contract](core.md#residual-evidence-rows) applies in both
backends.

## Typed failure snapshots

Exit payloads carry nominal descriptors with type arguments and transitively
capture-free constructor information. Generic workers receive descriptors as
hidden parameters; returned closures and suspended frames retain them. Compiled
descriptors use canonical names; interpreter descriptors also retain REPL type
generations. Complete descriptor equality gates inspection. Unknown declarations
and function/resource-bearing types are opaque.

Snapshots copy payload/descriptor lists and recursively retain secondary failures
without copying targets or resumptions. Fail.attemptReport elaborates to an
ordinary abort handler with one compiler-owned suppressed-payload binder, filled
only after cleanup unwinds. Other result construction uses ordinary Report/Result
ADTs. Core checks the binder's snapshot/list identities and intrinsic owner;
Machine lint checks its typed clause parameter and retained contract. FailureInspect
uses checked Maybe packaging and never invokes payloads. Snapshot lifetime checks
remain part of [ownership analysis](ownership.md#failure-snapshots).

Replayable [typed completion](completion.md) retains the complete result/row
contract and adds checked current-evidence replay; a Failure snapshot alone
does not authorize an abort.
