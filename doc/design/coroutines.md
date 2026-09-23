# Typed coroutine execution

[Public behavior](../reference/library-coroutines.md) owns lifecycle and API
rules. The compiler supports a scoped owner, typed advancement, synchronous
close, and a producer-local pause callback. Stream and Iterator are ordinary
library wrappers over this protocol.

## Protocol and control proof

The nominal `Coroutine request reply result e` fixes three value types and a
residual row. Nullary `Suspension` and `Drive` describe control without runtime
evidence entries. The capture-flow engine separately tracks concrete owners,
producer execution authority, exclusive accesses, and outward suspension.
Aliases retain identity; distinct allocations and unknown owners cannot be
assumed equal. An advance consumes its own producer's suspension; the lexical
owner consumes its own drive obligations. Foreign obligations survive both.

For an inner producer reached during an outer producer's active advancement,
owner subtraction precedes projection to printed labels:

| Execution | Remaining owner obligations | Printed control | Transport |
| --- | --- | --- | --- |
| Inner producer | Pause inner and outer | Suspension | Machine |
| Inner advance | Drive inner, pause outer | Drive, Suspension | Machine |
| Inner lexical boundary | Pause outer | Suspension | Machine |
| Outer advance | Drive outer | Drive | Machine |
| Outer lexical boundary | None | Empty | Direct, or Exit for residual aborts |

An inner boundary cannot erase a foreign suspension merely because it also
owns a pause with the same nullary label. Recursive allocation folding cannot
establish equality or lifetime ordering between uncertain owners; such programs
are conservatively rejected.

Before row closing, inference builds the finite value-flow contract from source
and imported checked contracts. It instantiates callbacks and owner identities,
joins recursive obligations to a fixed point, and projects surviving control
to source rows. Control requirements enter row solving before ordinary bounds;
latent [Work budgets](ownership.md#scoped-work-budgets) survive handlers around
packaging. Source rows are retained separately from runtime evidence rows,
which omit IO and compiler control labels.

Suspension summaries retain the ordinary effects through whose handlers a
pause was reached. A caller that exposes that effect already selects the
handler's transport; inference does not add a separate Suspension label for
that path. Unrelated effects cannot hide a foreign pause. Core still checks
the instantiated execution transport independently. Abstract Drive callbacks
can obtain their handles through ordinary constructor fields; closure captures
alone do not establish advancement authority.

When matching a callback's source rows, outward rows along the curried function
spine are matched before the callback's explicit labels are subtracted. This
preserves foreign Drive and ordinary effect obligations in nested consumers.
Nested row tails are flattened so recursive substitutions compare consistently.
Nominal stored row indices retain precedence over a widened execution view.
Fresh adapters are followed to their pre-existing descriptions before deciding
whether an invocation folds into a recursive summary; nested producers remain
distinct even when an outer description captures an inner one.

Core independently reconstructs capture contracts from executable nodes and
checks protocol types, allocation identity, exclusive access, reply retention,
request/result escape, and actual foreign control across synchronous calls.
Source annotations are not proof of discharge. ANF, lifting, specialization,
module objects, and execution serialization retain source types and contracts.
The object schema version rejects older representations.

## Lowering and dispatch

Core's CoroutineScope/CoroutineAdvance operations and Machine's cursor
operations support one typed coroutine protocol. Close has no reply and returns Unit,
while advance carries a typed reply and checked Step constructor descriptors.
Machine lint rechecks ownership, protocols, evidence, liveness, and cleanup
depth against retained semantic Core.

Each owner starts with a lazy producer factory. First advancement applies the
pause capability and input; later replies fill the suspended call's result.
The dispatcher returns a request at local pause and the final result only after
cleanup. Both the evaluator and generated runtime use private frames and
terminal state, with defensive busy checks and terminal clearing of frames,
factories, and forwarding links. Ordinary synchronous handlers keep their
Direct/Exit path; only suspension-capable execution requires Machine dispatch.

Captured evidence remains fixed. Per-advance forwarding installs the current
residual evidence and restores it when advancement completes or is abandoned.
Foreign suspension preserves forwarding, exclusive access, and intervening
cleanup scopes until that advance actually completes.

## Ordinary pull libraries

Stream stores a reusable producer closure with an ordinary `Yield a` effect.
Its private `withProducer` opens Coroutine.with, then handles Yield inside
the producer by tail-resuming with the pause callback's result. Different
stages can yield different element types: each handler removes its own Yield
before the coroutine boundary receives the residual row. No Stream name or
effect identity participates in lowering or ownership checking.

Iterator is an ordinary resource ADT containing a Coroutine with Unit reply and
result. Its Fango `next` supplies Unit and maps Step to Maybe. Both backends
therefore use the same generic request/result capture checks, nested transfers,
failure propagation, and cleanup as any other coroutine client. The
[Stream reference](../reference/library-streams.md) owns demand and ordering.
The separately compiled [Pull fixture](../../testdata/modules/pull/Pull.fango)
exercises another effect and private wrapper without compiler registration.

## Abandonment and completion

Normal return, outward abort, and owner stop are distinct private outcomes.
Stop discards the producer continuation and drains cleanup once; it never
publishes a successful result or becomes a catchable user abort. Cleanup-local
handlers may handle cleanup failures without resuming abandoned production.
Unhandled failures preserve primary/suppressed precedence. Acquisition/release
remain subject to the synchronous-callback proof.

[Typed completion](completion.md) is a separate capture/replay boundary. It
retains typed abort payloads and reports without saving a runtime exit target;
it does not intercept owner stop. [Work](ownership.md#scoped-work-budgets)
retains the existing coroutine and its checked owner budget without creating
another continuation or dynamic registry.
