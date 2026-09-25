# Cooperative Async tasks

`Async` supplies structured tasks on the cooperative executor. `Async.run`
creates the implicit root context; `Async.runOn Runtime.Executor.cooperative` and
`Async.Cooperative.run` select the same policy. `Runtime.Executor` currently exposes
only `cooperative`. Scoped native waits and HTTP GET are available through
`Async.IO`. Parallel and mixed policies remain
[roadmap work](../roadmap-async.md#implementation-stages).

```fango
spawn : (() ->{Async, Runtime.Service.Invocation Request Int, IO | e} a)
    ->{Async, Runtime.Service.Invocation Request Int, IO | e} Task a {IO | e}
await : Task a e ->{Async, Runtime.Service.Invocation Request Int, IO | e} a
context : (() ->{Async, Runtime.Service.Invocation Request Int, IO | e} a)
    ->{Async, Runtime.Service.Invocation Request Int, IO | e} a
run : (() ->{Async, Runtime.Service.Invocation Request Int, IO | e} a)
    ->{IO | e} Result Error a
runOn : Runtime.Executor.Executor -> (() ->{Async, Runtime.Service.Invocation Request Int, IO | e} a)
    ->{IO | e} Result Error a
runWithCapacity : Int -> (() ->{Async, Runtime.Service.Invocation Request Int, IO | e} a)
    ->{IO | e} Result Error a
```

`Async` is a nullary service effect. An explicit annotation for a task body
that calls Async operations includes both `Async` and the shared
`Runtime.Service.Invocation Async.Request Int` row. Inferred annotations can
leave those labels implicit. `IO` accounts for typed completion cells. The
child's remaining effects flow through `spawn` and the runner even if its
handle is ignored.

`spawn` registers a child without executing its body, then enqueues it at the
FIFO tail. The parent continues until a scheduling checkpoint. `await` parks
while the completion is unavailable. It observes one stored result, so repeated
and simultaneous awaits do not rerun work. `poll : Task a e ->{IO} Maybe
(Runtime.Completion.Completion a e)` checks readiness without waiting. A
published completion can be replayed later under fresh failure evidence.

`Async.context` makes a nested lifetime boundary on the same executor. It
waits for every task it owns before returning, including grandchildren made
after its body produced a value. A task created in the root remains root-owned
when awaited inside the nested context. The root similarly waits for its
outstanding children. Task handles retain their owning scope and cannot escape
through a return, ADT, or store. An unhandled Async operation outside a runner
is a compile-time effect error.

Ordinary effectful callbacks use the context supplied at their call. To retain
a definition-site context in a callback used later inside another context,
call `Async.bind` while the intended context is active:

```fango
Async.run (\_ ->
    makeOuter = Async.bind (\_ -> Async.spawn (\_ -> 5))
    Async.context (\_ ->
        outerTask = makeOuter()
        Async.await outerTask))
```

The bound callback retains the original owner, while its scheduling requests
use the worker currently executing it. A bound callback is scoped and cannot
outlive that owner. The [driver design](../design/async-cooperative.md)
explains the two authorities.

A child may fail with an ordinary typed effect. The task stores its completion;
`await` replays its saved failure through the awaiter's current handler.
Catching that replay does not change the stored failure or its context's
obligation to report it. Catch expected failure inside the child and return a
`Result` to make the child successful. After a failure, the driver cancels its
siblings at supported checkpoints, schedules unfinished work for a suspendable
cleanup drain, and waits for it before the context exits. Stopping work that has not
started does not run its body. The context body has failure precedence;
otherwise the lowest registered failed child is primary. Other child failures
follow in registration order, then owner cleanup failures in reverse close
order. Primary and suppressed payloads retain their types. Nested reports
remain nested and replay under the caller's current evidence.

Acquisition, body, and release can pause on Async requests. Cancelling a
parked task revokes its ordinary wait, then drives its pending release requests
through the same scheduler. Repeated interruption does not interrupt that drain;
native readiness remains available to a waiting release. The owner and its
native requests stay live until cleanup completes. A release that never
finishes keeps the context open.

`Async.yield()` gives other ready tasks a turn; `Async.waitSignal key` parks,
and `Async.signal key` publishes a sticky notification for scripted readiness.
`Async.IO.sleep millis` parks a task on a native timer. `Async.IO.get url` makes
an HTTP GET request and returns `Result String String`; it accepts UTF-8 text
responses up to one MiB and response headers up to 64 KiB. Network, timeout,
HTTP status 400 or higher, invalid
UTF-8, and size failures return `Err` with a message. A request is scoped to
its task, and cancellation drains native work before releasing its state.
`Async.runWithCapacity n` selects the maximum number of native requests in one
runner, including completed requests whose callbacks are not yet claimed.
`run` and `runOn cooperative` use 32 slots. Admission above the limit parks the
task until a slot is released. A nonpositive capacity returns
`Err (InvalidNativeCapacity n)` before running the action.

Yield, waits, native completion, startup, context entry/exit, and await of an
already published result are the supported cancellation checkpoints. In the
REPL, Ctrl-C during a cooperative run returns `Err Interrupted` after native
requests and cleanup drain. `Async.Error` also has `SelfAwait`,
`Stalled`, and `InvalidWorkerCount Int`. `SelfAwait` detects a task waiting on itself;
`Stalled` reports no runnable work or possible notification. The worker count
case is reserved for the later mixed executor. Scheduler errors are returned as
`Err`; a typed failure recorded during drain takes precedence and exits through
its ordinary effect handler.

`Async.IO.get` cancels its Go HTTP request when the task is closed. DNS and
host network calls may take time to return after cancellation; scope exit waits
for them. Existing synchronous `File` and `Net` operations still occupy the
cooperative driver while their host call blocks.

`Runtime.Async.Cooperative` remains a low-level A1 probe driver. It takes a
scripted signal list and worker factories and directly handles `Async.Request`
with `Runtime.Service.run`. Its private Task contains only successful values;
it does not implement public structured contexts or typed child failure
selection. The [A1 fixtures](../../testdata/run/async_a1_cooperative.fango)
and [A2 fixtures](../../testdata/run/async_a2_contexts.fango) run in both the
Core interpreter and generated backend. The [A3 fixtures](../../testdata/run/async_a3_io.fango)
cover overlapping HTTP fetches and suspension inside a Stream pull; the
[capacity fixture](../../testdata/run/async_a3_capacity.fango) covers admission,
cancellation, and cleanup failure.
The [A4 cancellation fixture](../../testdata/run/async_a4_cancel_release.fango)
covers two releases suspended during cancellation; related fixtures cover
typed failure precedence and native release readiness.
