# Typed detached completion

Completion introduction, replay evidence, and capture preservation.

[Design index](../design.md). Observable behavior belongs to the
[Completion reference](../reference/library-completion.md).

## Core and evidence

The bundled `Runtime.Completion.capture`, `Runtime.Completion.replay`, `Runtime.Completion.failure`,
`Runtime.Completion.fromFailure`, `dropSuspension`, and `dropDrive`
intrinsics elaborate to `core.Completion`. Their public result/row relationship
is checked before row erasure. Core retains the result type, operation identity,
residual invocation row, execution control, and checked Maybe descriptor for
failure inspection. Lint checks intrinsic ownership, nominal identity, callback
and result types, row availability, and packaging metadata. Constructors and
pattern elimination of the opaque representation are not Core operations.

The runtime representation is `Completion[A]`: a typed successful value or a
detached failure tree. Every failure keeps the canonical effect/operation,
operation index, complete payload tuple, and nominal payload descriptors.
Targets, handler frames, resumptions, and invocation evidence are excluded.
Suppressed reports are recursively copied on capture and replay.
`fromFailure` restores a detached context report to a Unit completion without
an old target; replay still selects and validates current evidence. The two
drop operations change only private control labels after capture has finished.

Concrete residual evidence bindings include a typed abort-replay adapter
generated with the binding's effect declaration and instantiated parameters.
The adapter verifies operation identity, tuple arity, and every nominal payload
descriptor before projecting values at their generated Go types. Replay takes
the current row, chooses that row's adapter, and resolves its current target.
An open-row helper forwards the adapter without interpreting an erased payload.
The evaluator checks the same descriptor/parameter relationship against current
evidence before constructing its fresh exit. Suppressed reports remain detached
data; they are never independently dispatched.

## Execution and ownership

Direct and Exit members invoke the callback through their ordinary protocol,
then package its result or outward exit. Machine lowering marks an indirect
callback call as a completion boundary; independent Machine lint checks its
result type and current intrinsic capture contract. Both dispatchers install
the private boundary below the callback's local handlers. Outward exits unwind
cleanup to that depth, discard completed frames and state, and publish detached
data as the call result. Suspension leaves the boundary installed and forwards
the request normally. Ordinary direct handlers keep ordinary calls.

Capture-flow analysis invokes the actual callback and records outward primary
and cleanup payloads at the completion destination. Local handlers remain
ordinary handler analysis. Completed values and failure trees preserve their
captures, while the invocation evidence itself is not retained. Completion
boundaries participate in context sharing keys, and recursive summaries retain
their detached payload obligations. These rules prevent completion and its
Failure projection from laundering resources hidden in suppressed reports.

Module objects and interpreter execution payloads serialize the Core operation,
residual-row metadata, Machine capture marker, and ownership contracts. Restored
programs pass the same Core and Machine validation as newly elaborated programs.

## Private owner stop

Abandonment records a private terminal cause, distinct from a normal result and
from an abort. It never routes through a user handler or publishes a pending
completion. Cleanup still runs. When a cleanup abort targets a retained local
handler, the dispatcher unwinds to it and runs only its abort clause in a
synchronous child dispatcher. Its normal answer is discarded; the producer's
continuation and return clauses remain abandoned. Borrowed state slots delegate
to the stopped dispatcher while the clause executes, so captured state evidence
keeps its identity. A clause failure carries the intercepted cleanup failure
and its suppressed tree as a secondary report before outer cleanup continues.
An outward cleanup abort is reported to the driver through the normal exit path.
