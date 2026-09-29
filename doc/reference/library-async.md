# Async tasks and channels

[Reference index](../reference.md). Source: [Async](../../stdlib/Async.fango).

## Runners and tasks

```fango
effect Async err
type Task err value
type Channel value
type Outcome value = Completed value | Cancelled

run : (() ->{Async err, IO, Fail err | effects} value)
      ->{IO | effects} Outcome (Result err value)
within : (() ->{Async err, Fail err | effects} value)
         ->{Async err, Fail err | effects} value
spawn : (() ->{Async err, IO, Fail err | effects} value)
        ->{Async err | effects} Task err value
completed : Result err value ->{Async err} Task err value
await : Task err value ->{Async err, Fail err} value
awaitResult : Task err value ->{Async err} Outcome (Result err value)
wait : Task err value ->{IO} Outcome (Result err value)
```

`run` creates an independent root scope; `within` creates a nested scope.
Every scope drains its children before completing. Closures, partial
applications, functions, native handles, and references may cross the task
boundary. Task and channel handles can outlive their runner. Resource validity
is checked by the resource's operations; sharing a file does not keep it open.

`err` is the common application failure type of a scope. A child executes
against a fresh matching `Fail err` handler, which catches its failure locally.
Effects are never queued for replay at an observation site. `awaitResult` and
`wait` expose the stored outcome; `await` performs a new `Fail.fail` in the
observer when it reads a failed result. An ordinary `Err` returned by a child
is a successful task value, distinct from invoking `Fail.fail`.

An observation that delivers a task's result marks it observed. Repeated
observations retain the same result. Body failure wins over child failure;
otherwise scope completion reports the earliest submitted unobserved failed
child. Application failure cancels existing siblings, but does not cancel the
parent body, which can observe and handle that failure. Go panics are not
translated into task outcomes.

## Handler inheritance and aborts

Children inherit resumptive handler activations and their state cells.
Individual snapshots and commits publish complete values; handlers own any
locking needed for atomic compound operations. Concurrent read-modify-write
operations may lose updates. Install a handler inside the child when independent
state is wanted. A parent's handler return clause does not transform child
results.

Other abort effects must be handled inside the child. A directly known
unsupported abort or local scoped permission is rejected with `ASYNC BOUNDARY`.
Dependencies hidden in inherited handlers or generic rows are checked at
runtime when preparing child evidence, before the user callback executes.
Unsupported dependencies fail with `Async: cannot inherit abort handler …;
handle it inside the task`; a mismatched failure type reports `Async:
incompatible inherited handler …`. These diagnostics terminate execution;
they are not application `Err` values. Merely installing a local abort handler
does not change an inherited handler's lexical dependency: install that handler
inside the child too.

For example, a child may catch an arbitrary abort locally:

```fango
effect Stop
    abort stop : String -> value

localJob() =
    handle stop "finished early" of
        stop reason -> reason
```

`spawn localJob` succeeds with the ordinary String value. An outer `Stop`
handler cannot be unwound from the child, and `await` never invokes it later.

## Cancellation

```fango
cancel : Task err value ->{Async err} ()
cancelScope : () ->{Async err} ()
cancelled : () ->{Async err} Bool
checkpoint : () ->{Async err} ()
sleep : Int ->{Async err} ()
contextToken : () ->{Async err} Runtime.Native.Any
```

Cancellation is cooperative and follows the child tree. Cancelling a child
alone does not cancel its parent. `awaitResult` exposes target cancellation as
`Cancelled`; `await` propagates it by cancelling the observer's scope and
aborting to its local cancellation boundary. Cancelling the observer interrupts
its wait without observing the target's result.

Current-task cancellation is separate from application failure and runs
language cleanup. Sleep, channel transfer, and task waits respond to
cancellation; nonpositive sleeps act as checkpoints. Arbitrary blocking IO and
CPU loops are not automatically interrupted. `cancelled`, `cancel`, `close`,
and `cancelScope` remain usable after cancellation; `checkpoint` exits a
cancelled scope.

In the REPL, Ctrl-C cancels the active runner and its child tree. The runner
finishes language cleanup and drains children before returning `Cancelled`
(subject to the usual failure precedence). This remains cooperative, including
in the root body: CPU loops need a checkpoint and arbitrary blocking IO may delay
completion. Later prompt evaluations receive a fresh host context.

`contextToken()` exposes the current task's opaque native cancellation context
for library sidecars that must interrupt a blocking operation. It is intended
for native adapters such as the cancellation-aware `Net` operations.

## Channels

```fango
channel : Int ->{Async err} Channel value
send : Channel value -> value ->{Async err} Bool
receive : Channel value ->{Async err} Maybe value
close : Channel value ->{Async err} ()
```

Channels have independent lifetimes. Negative capacity means zero; zero is
unbuffered. Close is idempotent. Sending to a closed channel returns `False`;
receiving drains buffered values before returning `Nothing`. Blocking transfers
respond to current-task cancellation. Closing a channel wakes blocked senders
and receivers without a Go channel panic.

## Pure parallel mapping

```fango
parMap : (a -> b) -> List a -> List b
```

`parMap` accepts a pure callback, preserves input order, and uses at most
`GOMAXPROCS` workers per invocation. It exposes no task handles and supports
ordinary partial application. An empty input does not invoke its callback.

The runnable [Sudoku solver](../../examples/sudoku.fango) starts a task for
each candidate at its first unresolved cell, sends solutions through a channel,
and cancels the remaining searches. Its recursive search calls `checkpoint`
to respond to cancellation.
