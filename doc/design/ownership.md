# Capture contracts and resource ownership

Static non-escape, retention, synchronous-callback, and exclusive-cursor proofs.

[Design index](../design.md). Source and checks: [Capture checking](../../internal/core/capture_check.go), [Flow analysis](../../internal/core/capture_flow.go), [Sharing keys](../../internal/core/capture_flow_key.go), [Scaling tests](../../internal/core/capture_flow_scaling_test.go), [Ownership tests](../../internal/core/iterator_ownership_test.go), [Yield tests](../../internal/core/yield_capture_test.go).

## Capabilities and lifetimes

Functions/type variables can retain captures; scalars cannot; ADTs can when a
field can. A nominal resource pragma marks a type capture-capable independently
of representation. It remains nominal metadata after resolution and requires
opaque exports, hiding constructors, fields, and reflected schema. The defining
module and native code own representation correctness. File and socket handles
use this mechanism over `Runtime.Native.Any`, with no resource-name or native handle
registry.

Handlers and cleanup/cursor scopes have distinct ScopeIDs. Runtime.Scope.bracket binds
its owner to a capture-capable resource; ordinary scalar resources, including
the Unit result of `Runtime.Scope.finally` introduces no borrowed capture. Parameterized handlers are
scoped even if their nominal effect is normally durable.

A result may not retain a scoped activation/resource, including through ADTs,
closures, dictionaries, or another worker. Stores into longer-lived evidence
are checked even when the result is Unit. An inner owner may retain an outer
resource within its lifetime. Proven non-retaining outer resumptive handlers
may borrow synchronously; abort payloads cannot cross a cleanup boundary because
clauses run after release. Independent returned functions remain legal.

Wrappers infer/export these obligations from their implementations. Annotations
do not erase contracts, and no runner/forwarding name receives an exemption.
Source diagnostics and resource declaration syntax belong in
[resources](../reference/resources.md).

## Capture-flow graph and abstract heap

Each definition exports symbolic result captures and a finite flow graph. The
graph erases scalar computation but preserves calls, callback invocation,
constructor fields, handler interpretations, state updates, and scope obligations.
Abstract callback/evidence requirements remain until callers provide meanings.
Schemes, module increments, and REPL checkpoints retain these contracts.

Checking substitutes actual callbacks/evidence and joins branches. An
allocation-site abstract heap tracks closures and fields; closures retain free
values and definition-site evidence, excluding their own binders. Pattern
bindings, partial applications, dictionaries, lifted locals, and adapters retain
the same obligations. Adapter temporaries/dictionary references have binding
identities and participate like source locals.

Context IDs are stable and independent of invocation paths. Sharing compares
callable identity, relevant arguments/free bindings, type substitutions, captured
and invocation evidence, residual rows, live scopes, resume owners, and active
borrow/acquire/release/pull boundaries. Closure fingerprints exclude their own
value/evidence/row binders. Instantiated scalar types erase scalar values; fields
that can carry capabilities or callables remain structural. Traversal preserves
aliases, terminates cycles, and distinguishes concrete owners and unknown versus
known-empty capability states. Globals use immutable callable identities.

Fingerprints describe the current heap, not permanent identities. A fingerprint
reads only an object's fields and a closure object's environment, so a monotone
heap revision invalidates cached ones when either grows, and growth in owners,
results, obligations, ancestry, or a context's widened environment leaves them
intact; lookup refreshes candidates within the callable bucket. A fingerprint is
compared only for equality, never ordered, stored, or shown. Stable invocation
edges merge later input growth into their selected context even after widening.
Sharing entry state and
the widened environment are separate and both reference the evolving heap.
Disagreeing type substitutions are forgotten conservatively so prior scalar
instantiation cannot erase a later resource.

Recursive calls join an enclosing context at a repeated target and lexical
call site only when inputs identify the same existing values or values allocated
inside that activation. Explicit allocation ancestry uses stable context IDs;
invocation ancestry joins even on cached returns. Distinct pre-existing callbacks
and descriptions keep nested helper invocations independent. A fresh cursor
owner remains distinct from a cursor passed to the enclosing invocation, even
when both cursors were allocated inside that invocation. Folding them together
would merge their exclusive-advancement obligations.

Each generation merges incoming environments and evaluates a context once,
joining folded resume owners. Busy/already-evaluated contexts return their current
summaries. Growth requests another generation until a fixed point; there is no
iteration-limit success fallback. Cached/busy returns replay access/suspension
obligations at every invocation. Diagnostic origins do not affect sharing keys.

Live scope/evidence IDs distinguish owners. Folded recursion cannot prove two
dynamic owners equal, so retention requiring that equality is rejected.
Concrete scalar results cannot carry captures. Handler interpretation propagates
actual payloads, resumed results, snapshots, and abort answers.

## Evidence and independent reconstruction

Closure lowering computes free evidence through handlers and nested functions.
An inner same-effect activation shadows its outer binding. Unused evidence and
evidence received on invocation are not captured. The interpreter uses the same
selection. Machine lint checks captured/invocation evidence against semantic
lambdas and typed workers.

Core lint reconstructs flow graphs from executable Core, rejects missing/stale
contracts, recomputes result summaries, checks scope introduction and exact
lexical evidence availability, and repeats the proof after ANF, lifting, callback
adaptation, and specialization. Source summaries alone are not trusted.

Reconstruction is unconditional; discharging the reconstructed obligations again
is not. A caller that has just discharged them for these same definitions, with
no transform in between, says so, and lint then compares what it reconstructs
without re-deriving the same answer from the same inputs. Any disagreement is
stale, is reported, and discharges them anyway. The whole-program path states
nothing and always discharges.

## Suspending acquisition and release

Capture contracts retain resource lifetime and definition-site evidence
through acquisition and release callbacks, including helpers and stored
values. Acquisition registers a release only after success. A release may
suspend while the resource stays owned by its closing scope. Recursive
summaries retain outward control obligations; an inner pull consumes only its
own producer's suspension. Core lint independently reconstructs these checks.

## Exclusive cursor advancement and yield

[Coroutine ownership](coroutines.md#protocol-and-control-proof) extends these
contracts to typed replies/results, close, and a separate producer pause
capability.

Iterator is an opaque resource carrying its owner's fresh capability. Aliases,
helpers, named consumers, constructor fields, and stored callbacks preserve it.
Independent nested owner sites remain distinct even through the same wrapper.

CoroutineAdvance carries exclusive-advancement metadata. Substitute the actual
cursor and execute its producer contract under that borrow; consumer callbacks
run after advancement completes. Access summaries remain active at recursive
joins and across unfinished foreign suspension. Possible overlap is rejected,
while sequential reads, including reads after exhaustion, are valid.

The producer pause callback tracks actual request flow into advancement results
and terminal callbacks; Stream's ordinary Yield handler calls that callback.
Elements may borrow enclosing resources but cannot carry producer-local resources
past a yield. Recursive accumulator captures reach a fixed point, including
callback effects reached through a prior iteration. Retained outer residual rows
also carry their handler captures. Core independently reconstructs access and
scope metadata; Machine lint preserves it on CursorAdvance/Suspend transitions.

## Failure snapshots

Reports may retain opaque captures from secondary cleanup payloads only when
those owners enclose the destination. This includes failures targeting another
handler that become secondary during release. Release-local handlers can consume
their failures before exiting. Ordinary fields, ADTs, and closures preserve
snapshot captures; inspectability is separate from lifetime safety.

## Scoped work budgets

Work owners use the same lexical cleanup scope and finite capture-flow engine
as other scoped resources. A package stores two independent identities: its
Work owner determines membership and the hidden effect budget, while its
coroutine determines lifetime, producer authority, and exclusive advancement.
Opening a package returns that original coroutine to the checked advancement
node; it cannot manufacture a driver from an erased thunk.

Source effect rows survive erasure as `SourceType` on definitions, closures, and calls,
and as source-row proofs on Work introduction and packaging nodes. Capture
contracts retain these fields. Source substitutions accompany the existing
value-flow interpretation, independently of runtime type arguments and
residual evidence rows; the latter omit IO and therefore cannot establish a
complete effect budget. The owning scope retains packaging obligations even
when an intervening handler removes their immediate charge.

Core lint reconstructs each Work capture graph, validates the nominal protocol
and source-row proof against the intrinsic declaration and the actual retained
coroutine, and rechecks owner membership and lifetime. Missing source contracts
are rejected; an exact eta forwarding adapter can reconstruct its contract
from the underlying call. A call's source proof preserves the runtime callee's
explicit effects; an open-row ABI may acquire additional source effects, which
must still fit the caller's source row. ANF, substitution, module objects, and interpreter
execution objects preserve the proof metadata. Both runtime paths keep an
opaque owner identity beside the underlying coroutine; neither opens the
package through an unchecked protocol cast.

Shared native payloads and retained service contexts follow
[the shared-capability contract](shared-capabilities.md). Transfer traverses
hidden evidence as well as ordinary closure/data fields; synchronization never
erases capture or execution-owner obligations.
