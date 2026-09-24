# Cooperative Async tasks

`Async` provides the A1 task surface. A task body uses the nullary `Async.Async`
effect and calls `Async.spawn`, `Async.await`, `Async.yield`, `Async.waitSignal`,
or `Async.signal`. Executor choice occurs at the root runner. The available
runner is `Async.Cooperative.run`. Parallel and mixed executors are not
implemented yet.

```fango
spawn : (() ->{Async | e} a) ->{Async, IO | e} Task a
await : Task a ->{Async, IO} a
yield : () ->{Async} ()
Async.Cooperative.run : (() ->{Async | e} a) ->{IO | e} Result Async.Error a
```

`spawn` registers the child without running it, then enqueues it at the FIFO
tail. The parent continues until its next scheduling checkpoint. The child's
remaining effects flow through `spawn` and `Async.Cooperative.run` even when its Task
is ignored. Allocation and reading of the typed result cell charge `IO`.
`await` returns a published value repeatedly without rerunning the task; while
the cell is empty it parks the caller. `yield` moves the caller to the ready
tail. `waitSignal` parks until a matching signal is delivered. `signal` uses
sticky notifications, so early or duplicate delivery cannot lose or duplicate
a wakeup.

The A1 Task contains a successful value only. Its root runner returns `Err
SelfAwait` for an internal self-wait and `Err Stalled` when parked work has no
remaining wakeup or a finished task omitted publication. Typed child failures,
unobserved failure selection, cancellation, nested contexts, and the common
`runOn` executor selector belong to later [Async stages](../roadmap-async.md#implementation-stages).

`Async` owns the task handle, request type, and generic worker adapter.
`Runtime.Async.Cooperative` imports that protocol and interprets it. The
low-level driver is also used for ownership and edge-case probes. Its runner
accepts a scripted list of integer signals and worker factories. A worker
registers each child in the runner's checked
Coroutine facet, then passes its work package and typed cell reader to `spawn`.
Its protocol is:

```fango
run : List Int
    -> (Runtime.Coroutine.Facet -> Runtime.Cell.Publisher a
        -> (Async.Request ->{Runtime.Coroutine.Suspension} Int)
        -> Int ->{Runtime.Coroutine.Suspension, IO | e} ())
    ->{IO | e} Result Async.Error a

spawn : Runtime.Coroutine.Facet -> Runtime.Work.Work Async.Request Int ()
    -> Runtime.Cell.Reader a ->{Runtime.Service.Invocation Async.Request Int} Async.Task a
await : Async.Task a ->{Runtime.Service.Invocation Async.Request Int, IO} a
yield : () ->{Runtime.Service.Invocation Async.Request Int} ()
waitSignal : Int ->{Runtime.Service.Invocation Async.Request Int} ()
signal : Int ->{Runtime.Service.Invocation Async.Request Int} ()
waitTask : Int ->{Runtime.Service.Invocation Async.Request Int} ()

type Async.Error = SelfAwait | Stalled
```

`Async.Task` has private constructors. `run` creates one
dynamic Coroutine scope, schedules the root as task zero, and drives a FIFO
ready queue. Each worker installs its own invocation authority with
`Runtime.Service.run pause`. The worker publishes its successful result through
the supplied cell before finishing. `spawn` enqueues an already registered
child at the queue tail and returns its typed Task; the parent keeps running
until its next scheduling checkpoint. `await` reads the cell repeatedly and
parks while it is empty. It never advances the child's coroutine itself.

`yield` moves the current task to the ready tail. `waitSignal` parks it until
the matching signal is delivered. `signal` and the runner's script use the same
sticky notification state: arrival before registration is remembered, duplicate
arrival does not enqueue a task twice, and a parked task is absent from the
ready queue. Task completion wakes every waiter for that task. Task and signal
keys are separate namespaces even when their integers match. A task waiting
for its own key through the internal `waitTask` probe makes the runner return
`Err SelfAwait`; no ready work and no
remaining scripted signal while tasks are parked makes it return `Err Stalled`.
Awaiting a finished worker that omitted publication also returns `Err Stalled`.
The runner drains its scope on either error. Completion unlinks the execution
from the dynamic registry and clears its coroutine storage; the cell retains
the value for repeated awaits while the scope is live.

The current API has successful, capture-free results only. External native
readiness belongs to later [Async stages](../roadmap-async.md#implementation-stages).
The [design](../design/async-cooperative.md) describes the driver and its
authority model.
