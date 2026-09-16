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

Factories and frame fields use the same value representation as ordinary code.
Open callbacks retain a Machine member even inside otherwise Direct definitions.
The defining module determines all these members, independent of consumers.

## Dispatch and frame lifetime

The evaluator has an explicit frame slice and uses recursive Core evaluation
only for non-Machine expressions that finish before the next transition. Go
emits typed frames with PC, parameters, and computed live locals. Step runs local
blocks in a loop and returns only for suspension, call/tail transfer, return,
or exit; only the shared runtime dispatcher invokes Step.

Non-tail calls push a frame; tail calls replace it. Return uses one erased
runtime register that the typed caller projects. Exported module-owned frame
constructors avoid runtime imports of generated packages and preserve the DAG.
Tail-call arguments, captures, and evidence evaluate before clearing the old
frame. Slices hold pointers/interfaces to separately allocated frames and handler
boundaries retain integer depths; no pointer to a relocatable slice slot escapes.
Statistics expose maximum live depth, frame capacity, cleanup, and state storage.

Completed frames are cleared. At suspension/non-tail boundaries the evaluator
drops locals outside LiveOut; Go zeros the frame and copies back only live fields.
Closures over mutable locals snapshot only referenced values and can retain both
ordinary bodies and Machine factories. Calling selects a protocol; storage does
not erase either representation.

Producer execution shares its caller's policy and step counter across Core,
tail loops, and dispatch, including loops that never yield. Reopening a producer
cannot reset a staging budget or permit a forbidden native. Dispatch restores
caller evidence on suspension/completion. Evaluator errors drain synchronous
cleanup and retain cleanup errors while attempting outer releases. Terminal and
protocol-error paths clear frames, state, handlers, and suspension storage.

## Handlers and cleanup

Machine handler bodies and clauses are separate typed workers with lexical
evidence in frame fields. Stateful clauses carry an opaque cell token. Abort
routing unwinds only to its exact target before invoking the clause.

A Machine Bracket has Exit acquire/release slots and a Machine body. Explicit
synchronous-argument obligations survive lowering and lint. An adapter may drive
a Machine callback synchronously only in a checked slot; unexpected suspension
drains cleanup and fails an invariant. Actual non-suspension is proved by
[capture analysis](ownership.md#synchronous-acquisition-and-release).

After successful acquisition, register a synchronous release closure before the
body starts. Suspension keeps the LIFO cleanup stack; normal completion pops
exactly once; partial unwind drains only the exited regions. Cleanup uses the
same copying Suppress protocol as synchronous scopes. Explicit abandonment
consumes unfinished production, drains all cleanup, clears storage, and returns
cleanup failure rather than discarding it.

## Cursor owner and advancement

Stream is an ordinary abstract effect-indexed ADT storing a producer closure.
Construction, stages, and terminal consumers are Fango; only ownership,
advancement, and yield need compiler support. Construction and arguments are
strict, while production begins on first pull. There are no terminal-traversal
Core nodes.

IteratorScope appears only in the private Stream.withProducer intrinsic. It
retains producer and consumer callbacks, a fresh ScopeID, owned Yield evidence,
and the opaque Iterator type. Its visible control is the consumer's residual
control: latent producer Machine transport terminates at the owner. Ordinary
compilation activates this boundary by canonical identity, not spelling or a
user-selectable machine flag.

Both backends open a private pull owner and pass it only to the consumer.
Next resumes the prior yield with Unit and drives to one yield or completion;
tagged exits remain distinct. Close abandons unfinished production and is safe
after exhaustion. Failed production leaves the cursor exhausted. Interpreter
frame factories are keyed by the exact semantic lambda identity; the REPL must
lower the same expression it displays/evaluates.

Stream.Yield is a reserved compiler-owned effect. Every cursor supplies a fresh
runtime owner token through normal hidden evidence. Workers, factories, and
closures preserve it; no ambient current-cursor slot chooses the destination.
Stream.yield becomes Suspend with owner, element request, and Unit resumed type.
Source and Core reject ordinary handlers for Yield. Private host fixtures may
use ownerless suspension with other resumed types.

IteratorNext lowers to CursorAdvance carrying exclusive access and a checked
nominal Maybe descriptor. It packages yield as Just and exhaustion as Nothing;
exits propagate before reading the result register. Machine lint independently
checks access, evidence, element type, descriptor, and live locals.

Each dispatcher has an iterative producer/caller transfer stack. A yield to an
enclosing owner parks unfinished inner advancements there, retaining their
exclusive borrows. Abandonment drains inner producers before callers and clears
transfers. CursorOpen creates the frame and registers closure without running
producer instructions. CursorClose and unwind share the cleanup protocol. Lint
tracks exact lexical cursor identities, rejecting a different owner or an
ordinary cleanup pop in place of cursor closure.

Iterator.Traversal is an owned marker with no runtime evidence parameter; the
cursor carries advancement identity. Iterator.next's intrinsic annotation must
expose exactly the cursor's residual row plus nullary Traversal. An owner removes
only Traversal from outward control. Stream.withCursor uses a synchronous
Direct/Exit boundary to drive its Machine consumer, registering producer closure
before the first step. That boundary returns an ordinary value/exit after closing
and rejects unexpected foreign suspension.

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
