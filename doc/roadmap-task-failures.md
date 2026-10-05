# Roadmap: task failure handling

[Roadmap index](roadmap.md#async-task-results-and-failure-policy).

**Status: TF1a and TF1b implemented; TF1c is a proposal.**
[Async](../stdlib/Async.fango) owns the value-returning task API and cancellation
rules. The [task design](design/tasks.md#async-runtime-foundation) owns completion
storage, lifetime boundaries, and checked invocation; the [effects reference](reference/effects.md#handlers-in-tasks)
owns launch permissions, abort diagnostics, and root handler placement.

Ordinary Async manages lifetimes without interpreting returned application
errors. The remaining proposal adds opt-in failure policy over task selection.
It builds on native goroutines and synchronous handlers, without coroutine
machines, cooperative scheduling, or general continuation capture.

## Fail-fast combinators

Ordinary Async supplies lifetime management. Fail-fast policy, where a reported
application failure cancels related work, is a separate opt-in layer made of
combinators that own the waiting:

```fango
awaitAny : List (Task value) ->{Async} (Int, value)

both : (() ->{Fail error | e} a) -> (() ->{Fail error | e} b)
    ->{Async, Fail error | e} (a, b)

all : List (() ->{Fail error | e} value)
    ->{Async, Fail error | e} List value

race : List (() ->{e} value) ->{Async | e} value
```

These are candidate library signatures, not settled declarations. `awaitAny`
returns the index and value of the first task to complete. The policy
combinators are ordinary polymorphic functions over it; there is no group
capability and no participant registry.

`all` works as follows:

1. Spawn each action as `Async.spawn { Fail.attempt action }`. The child
   handles Fail itself, so no parent abort target crosses the task boundary
   and the [abort boundary](reference/effects.md#handlers-in-tasks) is satisfied.
2. Block in `awaitAny` over the outstanding tasks.
3. On an `Err`, cancel the remaining tasks and join them, then raise the error
   with `Fail.fail` in the *caller's* goroutine, where the caller's handler is
   legitimately reachable.
4. If every task returns `Ok`, return the values in input order.

Because the parent does the waiting, there is no earlier blocked `await` that a
later failure could hide behind, and the registration race of a group reduces
to `awaitAny` observing an already-completed task, which any completion
notification must handle anyway. `awaitAny` and `race` are independently
useful.

Heterogeneous errors are normalized at the combinator call:

```fango
type LoadError = Database DatabaseError | Network NetworkError

loadBoth : () ->{Async, Fail LoadError} (Int, String)
loadBoth() =
    both
        { Fail.fromResult (Result.mapError Database (Fail.attempt lookup)) }
        { Fail.fromResult (Result.mapError Network (Fail.attempt fetch)) }
```

A network failure stops a still-running database lookup even though the lookup
is listed first. The caller chooses the policy by choosing the combinator and
handles `LoadError` with an ordinary `Fail.attempt` around the call.

The required semantics are:

- The first `Err` observed by `awaitAny` is the reported error. Later errors
  from children that finish during cancellation are not reported. Dropping
  them silently is acceptable only if documented; retaining them requires a
  specified type and bound, not an opaque value.
- Failure is recorded before cancellation is requested, and a success path
  cannot win after an accepted failure.
- Cancellation of the caller cancels and joins the children before
  propagating, and remains distinguishable from a reported failure.
- Ordinary `Async.spawn` tasks outside the combinator are unaffected.
- `race` cancels and joins the losers; the winner's value is stable.
- Tasks inside a thunk that return `Err` as data are ordinary values and do
  not trigger the policy.

**Limits.** Combinators cover fixed or list-shaped fan-out. They do not cover a
long-lived supervisor whose children are spawned dynamically while the body
keeps running. The HTTP server currently coordinates fatal worker errors through
an explicit event channel. If a future caller needs a reusable group policy,
it would need a capability with participant
registration, private cancel-after-report completion for failed participants,
nested scopes with different error types, and membership following explicit
scope evidence rather than a process-global current group. A library-only
group implementation must be demonstrated, not assumed; registration,
completion, shutdown, and cancellation race. Defer it until a concrete caller
shows combinators are insufficient, and design it then with `awaitAny` and the
combinators as precedent. No new execution model is justified by this API.

## Delivery stages

Stage identities remain stable. TF1c builds on the implemented value-returning
task and abort-boundary contracts.

### TF1a — abort-boundary spike

DONE

Known direct aborts are rejected statically; generic rows and inherited handler
dependencies are validated deterministically before user code runs. The
[effects reference](reference/effects.md#handlers-in-tasks) specifies the
guarantee and diagnostics. Polymorphic helpers provide runtime safety.

### TF1b — value-returning tasks

DONE

The [Async API](../stdlib/Async.fango) and [task design](design/tasks.md#async-runtime-foundation)
specify value-returning tasks, nullary Async, root abort propagation, cancellation,
and ordinary Result observation. Bundled HTTP workers use local recovery and
explicit coordinated shutdown rather than failure-driven sibling cancellation.

### TF1c — fail-fast combinators

Implement `awaitAny`, `both`, `all`, and `race`, with race and cleanup coverage.
Document them as the recommended API for fork-join work and raw `spawn`/`await`
as the lower-level layer. Preserve the implemented sealed completion index,
evidence reconstruction, source permissions, and independent Core checks.

### Conditional — groups

Only if a real caller cannot be expressed with the above. See the combinator
limits; no participant registry or group capability is part of TF1c.

## Decisions to close for TF1c

- Choose combinator error precedence and whether cleanup failures in cancelled
  children surface, for example through `Fail.attemptReport`.
- Specify `awaitAny` on an empty list and `race` on an empty list or when every
  participant is cancelled.
- A discarded `Task (Result _ _)` warning or mandatory consumption discipline
  remains a separate checker proposal, outside these stages.

## Acceptance and verification

| Area | Required evidence |
| --- | --- |
| `awaitAny` | Already-completed task, simultaneous completion, cancellation of the waiter, empty list, repeated calls on the same tasks |
| `both` / `all` | First failure cancels still-running siblings and joins them before Fail is raised in the caller; success preserves input order; outside tasks are unaffected; a failure behind a slower earlier sibling still cancels it |
| `race` | Losers are cancelled and joined; a winner's value is stable; all-cancelled behavior is defined |
| Races and cleanup | Immediate completion before waiting; simultaneous failures; cancellation versus failure; cleanup ordering and error precedence; no completion published before child cleanup finishes |
| Compiler/runtime | Malformed Core proofs rejected; interpreter/generated-code parity; cached/imported callbacks and inherited handlers retain correct evidence and result indices |

Use barriers or channels instead of schedule assumptions from sleeps. Preserve
the implemented permission, value, abort-boundary, root/nesting, observation,
and cancellation gates while adding these tests. Run strict stdlib documentation
checks and the full correctness suite under the [verification rules](design/verification.md).
Build-check benchmarks with `go vet ./benchmarks`; performance timing is not part
of ordinary delivery. Scheduling effects, detached tasks, mandatory result
consumption, groups, and general abort transport remain outside TF1c.
