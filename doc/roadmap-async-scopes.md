# Roadmap: task ownership and failure policy

[Roadmap index](roadmap.md#async-task-results-and-failure-policy).

**Status: proposal.** Value-returning tasks and the checked abort boundary are
implemented: the [Async API](../stdlib/Async.fango) owns the task surface and
cancellation rules, the [task design](design/tasks.md#async-runtime-foundation)
owns completion storage, lifetime boundaries, and checked invocation, and the
[effects reference](reference/effects.md#handlers-in-tasks) owns launch
permissions and abort diagnostics. This page covers two unfinished stages:
**TF2**, which makes leaving a task or scope cancel and drain its unfinished
children instead of joining them, and **TF1c**, fail-fast combinators built on
it. Both use native goroutines and synchronous handlers, without coroutine
machines, cooperative scheduling, or general continuation capture.

## Why change the lifetime exit

Today a task belongs to the task that spawned it, and a normal exit joins its
children. Bundled callers show the cost:

- "First answer wins" code cancels explicitly, because the join would wait for
  the losers (`examples/sudoku.fango`).
- `Http.Server` cancels and awaits its watcher and timer helpers by hand before
  its scope can end.
- `Async.within` returns a plain value, so a cancelled body cannot be returned
  to its caller; it stops the caller instead. `cancelScope` is then a
  cancel-self operation whose reach surprises, and a body that merely wants to
  stop its own helpers has no call for it.

All of this follows from the exit rule, not from who owns a task. Ownership
stays per task: a task owns what it spawns, and cancelling a task ends the
helpers it started. Flattening ownership into a shared ambient context was
considered and rejected. It would make cancelling a unit of work depend on
whether its implementation remembered to open another scope, so adding an
internal helper could change a library function's cancellation behavior without
changing its signature. No bundled caller needs a task to outlive its starter.

## TF2 — cancel-and-drain on exit

- **Every task owns an implicit scope.** `spawn` registers the new task with the
  innermost scope of the starting task: its own task scope, or an enclosing
  `scope`/`run` body. Registration is the only relationship; `await` creates
  none.
- **Leaving a scope cancels and drains.** When a task body, `scope`, or `run`
  exits, whether normally, by an abort, or by cancellation, every unfinished
  task registered to it is cancelled and then awaited until it finishes its
  cleanup. A scope cancels unfinished tasks, then waits for their cleanup; it
  never lets them continue normal work. It is not a hard kill: cancellation
  stays cooperative, and a resource's scope must outlive the tasks using it.
- **`scope` is a sub-lifetime on the calling task**, not a task. It runs its
  body on the caller and returns the body's value after its tasks have drained.
  It never produces a cancelled result of its own. When the caller is
  cancelled, the body stops at its next cancellation point and `scope` cleans
  up on the way out. `run` and `runOutcome` are the root scopes, keeping the
  current IO and host-interruption behavior.
- **`cancel task` cancels that task and its descendants**, and does not cancel
  its siblings or starter.
- **Outliving the starter is not offered.** Work that must outlive its starter
  should be spawned by a longer-lived task, such as the root. Spawning into an
  explicitly supplied owner is deferred until a concrete caller needs it.

`scope` replaces `within`. `cancelScope` is removed: a task exits by returning,
and the helpers it leaves behind end with their scope. `cancelled`,
`checkpoint`, `sleep`, and channels keep their meaning.

### Observation

- `awaitOutcome` observes `Completed value` or `Cancelled`. A target's outcome
  never cancels the observer or the target because of it.
- `await` is a convenience over `awaitOutcome`. When the target was cancelled,
  it propagates that cancellation into the **observing task**, never into a
  shared owner, so the observer stops at once and its own scopes drain.
- Cancelling an observer interrupts its wait without cancelling the observed
  task.
- `awaitAll : List (Task value) ->{Async} List value` is the explicit join for
  fork-join work, with the same cancellation propagation as `await`. A scope
  does not provide it implicitly.
- Handles remain valid after their owner exits. An unfinished child's handle
  then observes `Cancelled`, so returning an unfinished child's handle out of
  its owner is a mistake the documentation should call out.

### Invariants

- Normal scope exit cancels its registered tasks without cancelling the task
  executing the scope.
- Cancellation arriving during a drain cannot interrupt that drain.
- A completion is published only after the task's required cleanup and owned
  scopes finish. A later cancellation cannot change a published outcome.
- Registration racing with a scope closing either succeeds and joins the drain,
  or returns a cancelled task without executing its callback.
- The owner's registry holds active tasks only. Completed tasks are removed,
  while surviving handles keep their own completion values, so a long-lived
  root does not retain every finished task and result.
- A grace period means "allow normal work for this long, then request
  cancellation". Draining stays unconditional: a deadline cannot both bound the
  return and guarantee every task stopped. `Http.Server`'s shutdown already
  has this shape.

### Cleanup

Running cleanup is distinct from letting cleanup perform cancellable
operations. Today an Async operation called from cleanup after cancellation
aborts immediately, so a finalizer that must send, wait, or do cancellation-aware
IO cannot complete. The first delivery documents that restriction. Narrowly
scoped cancellation masking is added only when a consumer needs it.

Likewise, `Fail.attemptReport` does not report a cleanup failure when
cancellation is the primary abort, because catching `Fail` does not turn that
cancellation into a failure. Decide whether such reports are dropped or
surfaced some other way, and document it.

### What it simplifies

```fango
-- Leaving the scope cancels and drains the losers.
Async.scope {
    tasks = List.map Async.spawn candidates
    Async.awaitAnyOutcome tasks
}
```

`examples/sudoku.fango` drops its `List.each Async.cancel tasks`.
`Http.Server.runListener` keeps its event channel and worker awaits but loses
the explicit cancel and await of its watcher and timer. A supervisor with
dynamic children needs no group: workers report on a channel, the body receives
and returns, and leaving the scope cancels the rest.

### Accepted tradeoffs

- **Unawaited work is lost when its starter exits.** This is the opposite
  failure from a join that waits. Fork-join code awaits explicitly, with
  `awaitAll` or a combinator.
- **A task that never reaches a cancellation point hangs the drain.** This
  limit exists today and remains at every scope boundary.
- **Background work needs a longer-lived starter**, until explicit owners are
  designed.

### Implementation impact

The runtime already has per-task scopes and tree cancellation. Replace the
join in the task and runner exit brackets with cancel-then-drain, make the
registry active-only, and add the registration-race rule. Check that the Core
lint and proofs for the scope representation, the `AsyncLaunch` sealing, and
`AsyncRebase`'s permitted overrides do not assume a join, without weakening
them. Rewrite the lifetime and cancellation passages of the
[task design](design/tasks.md#async-runtime-foundation) and the
[effects reference](reference/effects.md#handlers-in-tasks). Migrate
`Http.Server`, `examples/sudoku.fango`, and the `testdata/run/async_*` fixtures
that rely on joining ignored children (`async_run` is one). No surface syntax
change is expected; if one becomes necessary, update and test the editor
grammar with it.

## TF1c — fail-fast combinators

Fail-fast policy, where a reported application failure cancels related work, is
an opt-in layer of combinators that own the waiting. They build on TF2, where
leaving the scope supplies the cancellation and drain; before TF2 they would
need explicit cancel-and-join code, so schedule TF1c after it.

```fango
awaitAnyOutcome : List (Task value) ->{Async} Maybe (Int, Outcome value)
awaitAny : List (Task value) ->{Async} Maybe (Int, value)

both : (() ->{Fail error | e} a) -> (() ->{Fail error | e} b)
    ->{Async, Fail error | e} (a, b)

all : List (() ->{Fail error | e} value)
    ->{Async, Fail error | e} List value

race : List (() ->{Fail error | e} value) ->{Async, Fail error | e} value
```

These are candidate library signatures, not settled declarations.
`awaitAnyOutcome` is the sound selection primitive: it returns the index and
outcome of the first task to finish, or `Nothing` for an empty list. `awaitAny`
is a convenience that propagates a selected cancellation into the observing
task, as `await` does. The combinators are ordinary polymorphic functions over
these, with no group capability or participant registry.

`all` works as follows:

1. Inside a `scope`, spawn each action as `Async.spawn { Fail.attempt action }`.
   The child handles Fail itself, so no parent abort target crosses the task
   boundary and the [abort boundary](reference/effects.md#handlers-in-tasks) is
   satisfied.
2. Block in `awaitAnyOutcome` over the outstanding tasks.
3. On an `Err`, leave the scope, which cancels and drains the others, then raise
   the error with `Fail.fail` in the *caller's* goroutine, where the caller's
   handler is legitimately reachable.
4. If every task returns `Ok`, return the values in input order.

`race` handles Fail locally and rethrows in the caller exactly as `all` does,
so a race between a network operation and a failing timeout works without
callers wrapping each thunk. A returned `Err` inside a thunk's value remains
ordinary data and does not trigger the policy.

Heterogeneous errors are normalized at the combinator call:

```fango
type LoadError = Database DatabaseError | Network NetworkError

loadBoth : () ->{Async, Fail LoadError} (Int, String)
loadBoth() =
    both
        { Fail.fromResult (Result.mapError Database (Fail.attempt lookup)) }
        { Fail.fromResult (Result.mapError Network (Fail.attempt fetch)) }
```

The caller handles `LoadError` with an ordinary `Fail.attempt` around the call.

Required semantics:

- **Selection.** `race` selects the first *terminal* outcome, whether a value,
  a failure, or a cancellation. Simultaneously ready outcomes may be selected
  nondeterministically; the promise is "first observed", not a global
  chronological order. A combinator that skips failures until a success arrives
  is a separate operation with its own name.
- **What fail-fast promises.** The combinators observe task *completion*, which
  is published after that task's cleanup. If B raises Fail and enters slow
  cleanup while A runs, A is cancelled only once B publishes its `Err`. They are
  fast relative to observable failed completion, not to the moment Fail occurs.
  Cancelling siblings at the raise would need a separate failure notification
  before completion; start with the simpler contract and state it.
- **Prompt request, not prompt return.** Cancellation is requested at once, but
  `race` and the others return only after the losers drain, which can be
  unbounded.
- The first `Err` observed is the reported error. Later errors from tasks that
  finish during cancellation are not reported; dropping them is acceptable only
  if documented, and retaining them needs a specified type and bound, not an
  opaque value.
- Failure is recorded before cancellation is requested, and a success path
  cannot win after an accepted failure.
- Cancellation of the caller cancels and drains the combinator's tasks before
  propagating, and remains distinguishable from a reported failure.
- Tasks outside the combinator's scope are unaffected.

**Limits.** Combinators cover fixed or list-shaped fan-out. A long-lived
supervisor with dynamic children uses the channel-and-scope pattern above, as
`Http.Server` does. A reusable group capability would need participant
registration, private cancel-after-report completion, nested scopes with
different error types, and membership following explicit scope evidence rather
than a process-global current group. Add one only when a concrete caller shows
this pattern is insufficient.

## Decisions to close

- Confirm per-task ownership with cancel-and-drain as one contract, the name
  `scope` (replacing `within`), the removal of `cancelScope`, and whether
  `awaitAll` ships with TF2.
- Choose the cleanup policy: the documented restriction on cancellable Async
  operations in cleanup, or a masking primitive, and how a cleanup failure
  under cancellation is reported.
- Decide whether an individually cancelled participant counts as `race`'s first
  terminal outcome (propagating cancellation like `await`) or is skipped, and
  the contract for `race` on an empty list, where no value can be returned.
- Choose the name and exact contract of the skip-failures selector.
- Choose combinator error precedence among failures observed close together.
- A discarded `Task (Result _ _)` warning or mandatory consumption discipline
  remains a separate checker proposal, outside these stages.

## Acceptance and verification

| Area | Required evidence |
| --- | --- |
| Exit | A normal, aborting, or cancelled task or scope cancels and drains unfinished children, including ignored handles, before completion is published; cleanup output ordering; cancellation arriving during a drain does not interrupt it; a published outcome cannot change |
| Registration | Spawn racing with a scope closing either joins the drain or returns a cancelled task whose callback never runs; completed tasks leave the registry while their handles keep values |
| Ownership | `cancel task` ends its descendants but not siblings or starter; a handle observed after its owner exited reports `Cancelled` when unfinished; nested `scope` drains only its own tasks |
| Observation | `awaitOutcome` leaves the observer and target uncancelled; `await` cancels only the observing task; cancelling an observer interrupts its wait without cancelling the target; repeated and concurrent observations are stable |
| Cleanup | Documented behavior for cancellable operations in cleanup; cleanup failure under cancellation follows the chosen policy; a grace period requests cancellation after normal work and still drains |
| Resources | Scoped Reader/Writer permissions stay enforced; a task cannot outlive a resource scope that encloses its Async scope |
| `awaitAnyOutcome` | Already-completed task, simultaneous completion, a cancelled participant, waiter cancellation, empty list returning `Nothing`, repeated calls on the same tasks |
| `both` / `all` | First observed failure cancels still-running siblings and drains them before Fail is raised in the caller; success preserves input order; outside tasks are unaffected; a failure behind a slower earlier sibling still cancels it; a failure inside slow cleanup is observed only at completion |
| `race` | A failing thunk is rethrown in the caller; first terminal outcome selected; losers cancelled and drained; the winner's value is stable; the cancelled-participant and empty-input contracts hold |
| Compiler/runtime | Malformed Core proofs rejected; interpreter/generated-code parity; cached/imported callbacks and inherited handlers retain correct evidence and result indices |

Use barriers or channels instead of schedule assumptions from sleeps. Keep the
implemented permission, value, abort-boundary, root/nesting, observation, and
cancellation gates passing while adding these tests. Run strict stdlib
documentation checks and the full correctness suite under the
[verification rules](design/verification.md). Build-check benchmarks with
`go vet ./benchmarks`; performance timing is not part of ordinary delivery.
Scheduling effects, explicit-owner spawning, mandatory result consumption,
groups, and general abort transport remain outside these stages.
