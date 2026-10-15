# Roadmap: resources and execution for IO interception

This companion to the [IO interception proposal](roadmap-io-interception.md)
owns the execution questions that remain open. Resource ownership, provider
identity, retirement, and foreign-resource checks for File and Net are
implemented; see the [resource design](design/ownership.md#file-provider-identity).
Network cancellation is implemented; see
[network services](design/effects.md#network-services).

## Resource representation

Implemented: File and Net resources are either provider-owned native objects
or Fango values naming their provider and a handler key, and Console owns
standard streams through a closed output choice. Generic code takes
Source/Sink or Reader/Writer values. Http.Client's `Memory Int` mock
connection is still not a safe resource-provider model; IOI7 replaces it when
it routes mocks through the checked Net boundary.

## Scoped capabilities remain deferred

Acquisition could instead pass a row-indexed Source/Sink record whose
operations call the provider selected at acquisition, so no ambient handler
dispatch happens per read. The acquisition operation would then have to return
a record whose row the handler chooses: a real provider's `{IO}`, or a mock's
fresh local permission. Operation signatures cannot express that today; it
needs row-kinded effect parameters or existential rows. Revisit capabilities
only if that language work is undertaken for other reasons.

## Mutable fixtures and concurrency

Use immutable values for fixture inputs and returned observations. Mutable
execution state belongs to a runner activation or checked resource backend.
Starting two runners from the same fixture must not share mutable state.
Publishing a snapshot must not expose a buffer that later writes can mutate.

Decided: a runner whose operations must stay consistent under tasks keeps
its mutable state in an enclosing [`State.runAtomic`](../stdlib/State.fango)
under a private state type, and each clause changes it with one pure
`State.update`; see [atomic state](design/effects.md#atomic-state).
`Random.runSeeded` and `Time.Clock.runStepped` do this. Handler `with` cells
keep their per-access synchronization, so `State.run` and user handlers built
on `with` may still lose concurrent updates, and a sequence such as
`State.get` followed by `State.put` is never atomic under either runner.

Remaining for IOI7: move `Http.Client.stub` and `mock` state onto this
mechanism, and decide the operations below whose consistency cannot be a pure
state step, such as a mock read that must wait for a write.

Each mock must still answer the following questions for its operations:

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
