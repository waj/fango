# Roadmap: resources and execution for IO interception

This companion to the [IO interception proposal](roadmap-io-interception.md)
owns unresolved representation and execution choices. The
[API catalogue](roadmap-io-interception-api.md) owns proposed public signatures.
File resource ownership, provider identity, and retirement are implemented in
[File](../stdlib/File.fango) and the [resource design](design/ownership.md).
This page now owns the remaining Network resource requirements and the
unresolved shared-handler-state question.

## Required separation of identities

The implemented separation of handler activation, resource provider, and
cleanup scope lives in the [resource design](design/ownership.md#file-provider-identity).
Network interpretation must preserve the same distinction; inheriting a handler
or closing one connection must not change the ownership of unrelated resources.

## Resource representation

[File](../stdlib/File.fango) owns File.Handle and its checked virtual-provider
API. [Console](../stdlib/Console.fango) owns standard streams through a closed
output choice. Generic code takes Source/Sink or Reader/Writer values. Net
currently exposes only its native resource representations; virtual connections,
listeners, pools, and their checked construction API remain IOI6 work.

### Provider identity within a domain

File's provider tokens, fresh handle identities, closed/foreign checks, and
checked native projection are specified in the
[design](design/ownership.md#file-provider-identity). A future Network backend
must provide equivalent checks for its own nominal resources. It must not use
an integer alone as a provider identity or promote Runtime.Native.Any into an
unchecked public construction API. Http.Client's current `Memory Int` mock is
not a safe resource-provider model.

### Scoped capabilities remain deferred

Acquisition could instead pass a row-indexed Source/Sink record whose
operations call the provider selected at acquisition, so no ambient handler
dispatch happens per read. The acquisition operation would then have to return
a record whose row the handler chooses: a real provider's `{IO}`, or a mock's
fresh local permission. Operation signatures cannot express that today; it
needs row-kinded effect parameters or existential rows. Revisit capabilities
only if that language work is undertaken for other reasons.

## Nested providers and foreign handles

File runners are closed interpretations and reject foreign handles without
fallback; custom forwarding retains the outer FileSystem row. The public
[File API](../stdlib/File.fango) owns the exact errors. A future Network runner
must settle the corresponding policy for connections, listeners, and pools.
Pure fallback must remain pure, and a missing handler must not access the OS.

## Lifetimes, escape, and cleanup

The [cleanup reference](reference/resources.md) and
[File design](design/ownership.md#file-provider-identity) own implemented scope
ordering, provider retirement, escape checks, and task ownership. IOI6 must
preserve those obligations for virtual and native Net resources: failed
acquisition releases nothing, scopes release in reverse order, body failures
stay primary, and escaped resources cannot alias later acquisitions. Raw Net
acquisition and pool leases need explicit close/release obligations, and
cancellation must not bypass their cleanup.

## Mutable fixtures and concurrency

Use immutable values for fixture inputs and returned observations. Mutable
execution state belongs to a runner activation or checked resource backend.
Starting two runners from the same fixture must not share mutable state.
Publishing a snapshot must not expose a buffer that later writes can mutate.

Open question; the decision is a prerequisite of IOI7.

The current [task runtime](design/tasks.md#async-runtime-foundation) can inherit
resumptive handlers and publish their state for synchronized access. Protected
cell loads and stores keep every observed value complete, but do not make an
entire handler clause an atomic read-modify-write operation. Under tasks, a
clause that appends to a snapshot can overwrite another task's commit. This
applies to every stateful handler, not only mocks: `State.run`,
`Random.runSeeded`, `Clock.runStepped`, and `Http.Client.stub` lose updates the
same way. Even atomic operations do not make a sequence such as `State.get`
followed by `State.put` atomic; that needs a single operation such as `modify`.

Candidate mechanisms:

1. Restrict the test runners to sequential use, documenting that concurrent
   use may lose updates.
2. Implement the stdlib test runners natively with internal locks. User mocks
   remain unprotected.
3. Run clauses that perform no effects before their `resume` atomically by
   holding the cell's lock from snapshot to commit. A clause that performs
   effects, such as one with an added debug `print`, silently loses atomicity.
4. Let a handler declare itself atomic, and reject any of its clauses that
   performs effects at compile time. Undeclared handlers keep today's rule.
5. Hold the lock across every clause of a shared handler, faulting on task
   waits inside a clause. Waits through real IO can still deadlock, and slow
   clauses serialize every task using the handler.

Unshared cells use plain accesses and would not pay for any of these. A choice
must answer the following questions for each operation:

| Operation | Required consistency question |
| --- | --- |
| Console write | Is each write appended as one indivisible event? |
| Console read | Can two tasks consume the same scripted line? |
| File read | Does one successful read advance the shared offset exactly once? |
| Append | Are placement and data publication atomic at the promised operation boundary? |
| Close versus read | Which operations may complete, and which observe closed state? |
| Connection write | Can bytes from separate calls interleave? |
| Pool release/acquire | Can a connection be leased to two callers? |

Whatever is chosen, no handler may claim concurrent correctness the mechanism
does not provide, and no task or channel is added to every synchronous
operation simply to serialize it.

Tests should assert per-resource order and valid causal relationships. A
global trace can record observed order but does not make that order repeatable
across native schedules. Fixed clock readings and seeded randomness do not
establish deterministic ordering between tasks.

## Cancellation

Cancellation must remain attached to the requesting task and supported
operation lifetime. The public Net driver cannot expose an unchecked Go
context payload as its contract. A checked token/capability must specify
whether it can be retained, inherited, copied, and used after its owner ends.

Evaluate synchronous and cancellable variants independently. If a system
handler clause performs Async operations, account for that dependency in the
runner type and in child-handler reconstruction. Do not capture a parent's
task identity and accidentally use it for every child call. Native default
selection must not depend on an undocumented search for optional ambient
effects.

Real cancellation acceptance includes blocked accepts, reads and writes where
supported, deadline races, close interrupting a blocked operation, and cleanup
after cancellation. A custom handler that immediately returns a
cancellation-shaped error tests application handling but does not establish
that the real native operation can be interrupted.

Keep application failures, EOF, timeouts, and cancellation distinct. Decide whether task cancellation is observed through the existing
task cancellation boundary or translated to a transport error at a particular
API; preserve whichever distinction the public operation promises. Retain
the current restrictions on abort-only handlers in child tasks.

Whole-request timeout policy, early HTTP responses during upload, and other
HTTP-specific choices remain in the
[client roadmap](roadmap-http-client.md#open-questions). A custom Net handler
can provide acceptance scenarios without settling those policies by accident.

## Native and compiler boundary

The implemented [native contract](reference/native.md),
[backend](design/backend.md), and [resource design](design/ownership.md)
own File/Console separation and checked native projection. Future Network
operations should likewise be ordinary source-defined operations with system
handlers calling private natives. Do not generalize native operation defaults,
exchange arbitrary Go payloads, revive coroutine machinery, or add a task/channel
per operation merely to implement interception.

Network work must preserve operation identity through imports, reexports,
partial application, staging, evidence adaptation, and module emission. Real
runners must call private natives rather than recursively invoking their own
operations. Optimizations must preserve handler selection and observable IO.

## Review checklist for provider identity

1. Can the same ordinary function run with real, fake, and mixed services?
2. Do its effect rows reveal every residual real IO dependency?
3. Can a custom handler be written using public checked types alone?
4. Can two providers and two live resources be used together unambiguously?
5. What happens to a foreign, closed, escaped, or forged resource?
6. What exactly is observable after a failure?
7. Can inherited handlers share state without lost updates or deadlocks?
8. Which cancellation behavior is simulated, and which is verified on real IO?
9. Do whole-file, TLS, pool, and other native fast paths cross the boundary?
10. Are unsupported sidecar actions and scheduler behavior clearly outside the
    interception claim?
