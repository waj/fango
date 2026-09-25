# Machines, streams, and cursors

Selective state-machine lowering, frame lifetime, pull ownership, and failure inspection.

[Design index](../design.md). Source and checks: [Machine IR](../../internal/machine), [Runtime dispatcher](../../runtime/fangort/machine.go), [Cursor runtime](../../runtime/fangort/iterator.go), [Machine tests](../../internal/codegen/machine_test.go), [Lifecycle tests](../../internal/eval/iterator_lifecycle_test.go), [Failure tests](../../internal/repl/failure_test.go).

## Selective lowering

`internal/machine` is a typed execution IR below semantic Core. It selects
concrete Machine roots and every transport-polymorphic worker's Machine member.
Fixed Direct/Exit definitions keep their ordinary path; their stored Machine
callbacks get separate frame factories. Root lambdas inside synchronous cursor
owners do not force their enclosing definition into Machine execution.

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
The defining module determines all these members, independent of consumers.

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

Suspension needs retained continuation state, but does not require a new heap
frame for every source call. Self-tail re-entry reuses the same state storage;
primitive work without independent continuation state uses its caller's saved
result edge. Both paths preserve dispatcher boundaries rather than recursively
executing the next continuation inside a factory.

The Go emitter can avoid an intermediate advancement result when its only use
is an immediate exhaustive protocol match, following identity bindings. It
selects the suspended/finished/closed edge and binds its typed payload directly;
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
caller evidence on suspension/completion. Evaluator errors drain synchronous
cleanup and retain cleanup errors while attempting outer releases. Terminal and
protocol-error paths clear frames, state, handlers, and suspension storage.

## Handlers and cleanup

Machine handler bodies and clauses are separate typed workers with lexical
evidence in frame fields. Stateful clauses carry an opaque cell token.

An activation the body reaches at Direct is the exception: its clauses neither
exit nor suspend, so they stay ordinary closures and only the body is a machine
region. Their state is one escaping variable they share, held through the
machine's state stack so unwinding still accounts for it and the return clause
reads the final value; the locals they read are snapshotted where the
activation is installed and are live there. This is what lets a closure bound
to such an activation keep its own Direct protocol inside a suspending
computation. Abort
routing unwinds only to its exact target before invoking the clause.

A Machine Bracket allows a Machine acquisition and has a checked Exit release
slot. Explicit synchronous-argument obligations survive lowering and lint. An
adapter may drive a Machine callback synchronously only in the checked release
slot; unexpected suspension drains cleanup and fails an invariant. Actual
non-suspension is proved by [capture analysis](ownership.md#synchronous-release).

After successful acquisition, register a synchronous release closure before the
body starts. A suspending acquisition continues into registration only after
it returns successfully. Suspension keeps the LIFO cleanup stack; normal
completion pops exactly once; partial unwind drains only the exited regions.
Cleanup uses the same copying Suppress protocol as synchronous scopes. Explicit
abandonment consumes unfinished production, drains all cleanup, clears storage, and returns
cleanup failure rather than discarding it.

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
types, descriptors, and live locals.

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
