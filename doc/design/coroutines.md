# Typed coroutine execution

[Public behavior](../reference/library-coroutines.md) owns lifecycle and API
rules. The compiler supports lexical and dynamic scope owners, typed advancement,
synchronous close, and a producer-local pause callback. Stream and Iterator are ordinary
library wrappers over this protocol.

## Protocol and control proof

The nominal `Coroutine request reply result e` fixes three value types and a
residual row. Nullary `Suspension` and `Drive` describe control without runtime
evidence entries. The capture-flow engine separately tracks concrete owners,
producer execution authority, exclusive accesses, and outward suspension.
Aliases retain identity; distinct allocations and unknown owners cannot be
assumed equal. An advance consumes its own producer's suspension; the lexical
owner consumes its own drive obligations. Foreign obligations survive both.
When one advance site selects among queued handles, each candidate keeps a
separate owner-indexed pause capability. Sharing a dispatch site does not merge
producer authority.

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
An innermost pause retains only the producer's saved result edge; parked
traversal storage is needed only when a foreign pause also retains unfinished
inner advancements.

## Dynamic scope registry

`Runtime.Coroutine.scope` uses the same Core scope boundary and Machine cleanup stack as
`with`. Its resource protocol identifies a registry rather than one producer;
a checked Unit producer sentinel distinguishes that boundary in Core and Machine
IR. `Runtime.Coroutine.create` introduces a distinct child execution owner beneath the
selected registry. Capture-flow lifetime checks use the destination's ancestry,
not the helper's current scope stack. Drive discharge stops at that registry's
boundary only for a child with proven membership. Foreign Drive and Suspension
obligations remain outward. Distinct destination registries retain separate
execution identities even at a shared allocation site.

Both runtimes install a lazy child and its close capability in a doubly linked
live list before returning the handle. Cleanup never reconstructs a typed handle
from an integer or untyped payload: it invokes the existing child's synchronous
close operation. Completion, failure and explicit close unlink in constant time,
clear both neighbor links and the parent link, and release the producer factory,
frames and evidence forwarding. Scope exit marks the registry closing before
walking remaining entries backwards, drains every synchronous cleanup, and uses
the ordinary primary/suppressed failure precedence. Completed entries are not
retained until scope exit. A scope's saved evidence supplies the baseline for
child cleanup; advances still install their own current residual evidence.

`Runtime.Coroutine.facet` retains registry identity while hiding its source row.
`Runtime.Work.register` combines checked child creation and packaging; `Runtime.Work.owner`
selects the same registry's Work identity. Source-row inclusion is reconstructed
by the existing [Work budget analysis](ownership.md#scoped-work-budgets), including
latent charges through handlers. The three typed protocol indices remain in the
package and its opening adapter. Dynamic handles separate lifetime from execution
identity, so the Work transfer check also walks retained handles in closures and
ADTs rather than inferring execution authority solely from lifetime captures.
Recursive context sharing makes the same distinction, keeping nested observation
handlers separate when their callbacks retain different dynamic executions.
A closed producer contract supplies its actual residual budget; callback
adaptation can widen an invocation view without adding effects to stored work.
Open producers retain their declared row proof. Deferred registration provenance
also excludes aliases introduced by a producer's own Suspension protocol.
Core checks declaration shapes, source budgets and protocol edges independently;
object and execution serialization preserve those contracts.

The [differential lifecycle fixture](../../testdata/run/coroutine_dynamic_lifecycle.fango)
checks lazy allocation, reverse cleanup, repeated completion, and descendant
creation after a context-body helper returns. The
[registration fixture](../../testdata/run/coroutine_registration.fango) uses a
nullary service to package different protocols and residual rows. Negative
fixtures reject short-lived captures, escaped queues/facets/packages, wrong
owners and erased latent effects. The
[storage gate](../../cmd/fango/coroutine_dynamic_test.go) pins all allocated
children in temporary instrumented versions of both backends: 2 and 200 repeated
completions have the same peak of three live registry entries and leave no
registry links or execution storage. These are structural checks, not timings.

## Ordinary pull libraries

Stream stores a reusable producer closure with an ordinary `Yield a` effect.
Its private `withProducer` opens Runtime.Coroutine.with, then handles Yield inside
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

## Cooperative dispatch demonstration

The ordinary [scheduler fixture](../../testdata/run/coroutine_scheduler.fango)
keeps two coroutines and its FIFO ready queue inside both lexical owners.
Requests are an ADT with voluntary yield and a fake wait key; replies are Unit
and completion carries a named integer result. Each advance returns a Step to
the tail-recursive dispatch loop. Yield appends the job to the queue, wait
removes it until a deterministic signal, and completion drops it.

One task pauses its scheduler owner from inside `Iterator.next` on a Stream.
The inner pull remains pending and exclusively borrowed until the task resumes;
the scheduler can run the other task in the meantime. Early abandonment closes
the pending pull and both tasks, with each cleanup running once. Repeated close
and terminal advance do not rerun cleanup. The differential trace checks FIFO
alternation, no advance of a waiting task before its signal, typed results, and
normal and abandoned cleanup order.

The [storage gate](../../cmd/fango/coroutine_scheduler_test.go) instruments only
temporary interpreter and generated-runtime sources. It pins observed owners
and sessions, counts retained frames across them, and checks bounded live storage
at 2 and 200 yield rounds per task, including abandonment inside `next`.
Both sizes peak at three live owners (two tasks and the Stream cursor), with
15 interpreter frames or 13 generated-runtime frames across all sessions;
terminal owner and frame counts are zero. The gate allows a fixed ceiling of
32 frames so harmless frame-layout changes do not change the storage contract.
Terminal owners must release factories and exclusive access; frames, cleanup,
handlers, state and traversal links must be empty. These are structural counts,
not timing benchmarks. This demonstrates shared Stream/scheduling control;
Dynamic spawn and reusable successful task results use the separate
[cooperative task driver](async-cooperative.md). Concurrent execution remains
outside this scoped example.

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
