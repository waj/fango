# Cooperative task driver

The [A1 API](../reference/library-async-cooperative.md) puts a nullary `Async`
effect, `Task`, `Request`, `Error`, and ordinary task operations in `Async`.
`Async.Cooperative.run` selects the current executor at the root. `Async` imports
only the shared Coroutine, Work, and Cell mechanisms; the cooperative driver
imports `Async` to interpret its requests. The implementation uses C4 dynamic
Coroutine scopes and C6a typed cells. The runner owns one registry and one `Runtime.Work.Owner`
for its root and children. A child is registered before a `Spawn` request reaches
the driver. The request carries a uniform checked Work package; its typed
publisher and reader remain in the worker and Task, never in the queue.

Each `Async` worker handles the effect locally and translates an operation to
its own scoped pause callback. The worker's explicit effect annotation retains
the action's residual row in its Work registration and discharges `Async` before
the driver advances it. `Async` exports this generic worker adapter for runners;
it does not contain queue or executor policy. The lower-level
`Runtime.Async.Cooperative` probes use C6c `Runtime.Service.run` to exercise the
same request protocol through shared service invocation.

The driver alone advances Work and interprets requests. Its state has a FIFO
ready queue, a list of parked registrations, disjoint task and signal
notification keys, the next task ID, and a finite scripted signal list. A queue
entry also stores the reply for its next advance: a spawn reply is the assigned
child ID, while other replies are zero. This keeps task identity out of ambient
worker state.

One tail-recursive `dispatch` computes and applies each transition in the same
Machine frame. Its ready queue is separate from waiting/notification state,
which a yield retains unchanged. Its private linked ready lists avoid the
unused chunk capacity of the general-purpose List for small queues.
Queue inspection avoids an intermediate
dequeue result; yielding reuses jobs whose reply is already zero. With just one
ready job, it retains the existing queue while still returning through the
coroutine's suspension and advancement boundaries.
Spawn appends the child at the FIFO tail and resumes the parent next, so
the parent receives its ID and continues before the child starts. Yield enqueues
only the current task.
Wait removes it from ready and adds one registration unless its key is already
notified. Signal and completion claim matching registrations and enqueue them
once. A wait for a completed task whose cell is still empty reports Stalled
instead of requeueing forever. When ready is empty, the driver consumes the
next scripted signal; with no script and parked work it reports Stalled. Self
wait reports SelfAwait immediately. Scope exit closes all unfinished children; finished children
unlink and clear execution storage before the driver returns.

The [neutral task fixture](../../testdata/run/async_a1_cooperative.fango),
[low-level edge cases](../../testdata/run/async_a1_edges.fango), and
[Stream suspension](../../testdata/run/async_a1_stream.fango) check exact traces
in both backends. The [dynamic storage gate](../../cmd/fango/coroutine_dynamic_test.go)
pins allocated owners and sessions in temporary instrumented runtimes and
checks that A1 completion leaves no registry links, frames, or traversal state
in the generated backend. The existing C4 interpreter probe checks the same
dynamic registry and execution cleanup primitives without a native cell worker.
