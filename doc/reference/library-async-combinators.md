# Concurrent Async combinators and events

[Reference index](../reference.md). The cooperative executor implements these
operations; the [Async task contract](library-async-cooperative.md) supplies
their context, failure, and cancellation rules.

## Race and timeout

`Async.race left right` starts both actions as children. It returns
`LeftWon a` or `RightWon b` from `Async.Race a b`. The first
completion notification observed by the driver wins. If both notifications
are already recorded, their recorded order decides; the cooperative driver
does not create a simultaneous notification. A failed child retains the
ordinary context failure obligation. The combinator cancels and fully drains
the other child, including suspending release, before returning.

`Async.IO.timeout millis action` returns `Err (InvalidTimeout millis)` for a
negative duration, `Ok (Just value)` if the action completes first, and
`Ok Nothing` if the native timer completes first. A zero duration participates
in the same completion-order rule. The deadline starts cancellation of losing
work; return waits for its release and native request drain. A release that
does not finish prevents the timeout from returning.

These operations work under `Async.run`, so the runner's own `Result
Async.Error` encloses the timeout's result. The exported
`firstCompleted`, `selectCompleted`, `cancelAndWait`, and
`requirePositiveCapacity` helpers support scoped library combinators. A
`cancelAndWait` call waits for the target worker's terminal state, including
its cleanup, rather than for early publication of a failure value. It drops
that task's historical completion notification after draining; an already
published success remains readable through its task cell.

## Concurrent stream mapping

Import `Stream.Concurrent` in addition to `Stream`:

```fango
requests
    |> Stream.Concurrent.map 8 fetch
    |> Stream.forEach consume
```

`Stream.Concurrent.map` yields in input order. `mapUnordered` yields in
completion-notification order. Both pull at most a positive capacity of
inputs into a batch, spawn one child per input, and finish that batch before
pulling the next. Each batch therefore bounds active work plus retained
completed results. A slow first result can hold a full batch; upstream
admission stops until it is delivered. The callback may perform `Async`, its
shared service invocation, and `IO`; it may be pure. The source Stream keeps
its own residual effects. The child callback's explicit row prevents
transferring the producer's `Yield` authority into an independently driven
task. A nonpositive capacity reports `InvalidConcurrentCapacity Int` through
the enclosing Async runner before the first upstream pull.

Each batch has a cleanup scope. Early `take` or a downstream failure cancels
and drains remaining children before the upstream cursor closes. New
traversals spawn fresh batches. Ordinary sequential `Stream.map` retains its
existing demand behavior and needs no Async runner.

## Native tick subscriptions

`Async.Events.ticks interval count` describes a finite native tick source.
`Async.Events.withSubscription source capacity policy consume` passes a
Stream of zero-based event numbers to `consume`. A traversal registers the
source on its first pull; reopening starts a new registration. The traversal
unregisters and joins the native emitter on exhaustion, early stop, or
failure. `interval <= 0` emits without a timer delay; `count <= 0` emits no
events.

Capacity must be positive. `InvalidCapacity Int` and `Overflow` are typed
`Async.Events.Error` values raised through `Fail`, so `Fail.attempt` can
handle them. The bounded queue applies one of three policies:

| Policy | When the queue is full |
| --- | --- |
| `Async.Events.Fail` | Raise `Overflow` and stop the source. |
| `Async.Events.DropOldest` | Replace the oldest queued event. |
| `Async.Events.DropNewest` | Discard the incoming event. |

The native emitter never invokes a Fango callback. It only enqueues an event
and notifies the runner's bridge. A notification that races with unregistration
cannot reopen a closed traversal. The emitter, bridge ticket, and queue are
released before traversal cleanup finishes.
