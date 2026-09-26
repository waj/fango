# Cooperative Async driver

The [Async reference](../reference/library-async-cooperative.md) owns the public
behavior. The cooperative runner drives all tasks in one dynamic Coroutine
scope. `Async.run` and `Async.runOn Runtime.Executor.cooperative` create that scope;
`Async.Cooperative.run` is the same policy-specific entry point.

## Execution and context authority

Each task is a checked `Runtime.Work.Work Request Int ()` under the root scope's
facet. Its typed `Runtime.Cell` holds a `Runtime.Completion` for repeated
observation; the ready queue contains only uniform Work packages. A worker
installs `Runtime.Service.run` with its own pause, then handles `Async` with a
fixed facet and logical context path. The service invocation uses the active
worker's pause even when a bound closure retains an earlier context. This
keeps execution authority separate from context ownership.

The driver is the sole Work advancer. A `Job` carries its assigned task key,
next reply, logical owner path, and Work. A spawn request carries the owner
path chosen by the `Async` evidence used at the call. The driver assigns a
monotone key before it enqueues the child, so registration order determines
failure order. `Async.bind` captures the current facet and path in a closure
whose later service invocations still use the active worker's pause. Its
resource capture cannot outlive the runner.

A packaged Work cursor reports `Polled` after a bounded number of generated
Machine steps. The driver appends that job to the FIFO tail and checks native
bridge and cancellation state before taking another job. Its reply remains
pending, and no other job can advance that cursor. A poll during cancellation
uses the same stop and drain path as an explicit yield. Release work keeps
polling while repeated interruption is shielded until the owner is quiescent.

The root also owns one shared native event bridge with a bounded ticket table.
Each native adapter reserves a ticket before starting its own scoped
`NativeRequest` host. Its native worker stores a stable result, publishes
readiness, and uses the request's `OnDone` hook to notify the bridge before
quiescence. A ticket is never reused. Completion before wait registration is
read from bridge state; duplicate and stale events cannot wake another task.
The driver drains scalar notifications between task turns and blocks on the
bridge only when no task is ready and a native wait remains. Capacity waiters
retry when a drained request releases a slot. The bridge coalesces capacity
notifications and removes stale completion entries on release, bounding its
queue by admitted work.

`Async.context` allocates a child path, registers its body as another root-scope
Work package, and parks the caller on a context key. The driver keeps a record
for that path and wakes the caller only when no live job remains under it.
Children can therefore create grandchildren after the context body has
finished. A task spawned through evidence bound to an outer path stays in that
outer context. One Coroutine scope is necessary here: parking an outer worker
while it drives a separate inner Coroutine scope would abandon the inner
scope before outer work could make progress.

## Queue, completion, and failure

The FIFO queue uses two linked lists and one tail-recursive dispatcher.
Spawn continues the parent before the child starts; yield appends the current
job. Await and signal waits remove jobs from ready and install a single
registration. Completion and sticky signal notification wake matching waiters
once. A failed worker publishes its completion, then sends a `Published`
notification that places its awaiters ahead of unrelated ready work and its
own failure report. An awaiter can replay and catch that saved failure; the
later report still fails the owning context. The driver diagnoses self-await
and lack of runnable or notified work as
`Async.Error` rather than spinning. An already published completion still
passes through a scheduling checkpoint before replay.

The worker captures the whole task action, including its cleanup,
then publishes one completion. A failed completion also sends a detached
failure report and a replay Work package to the driver. The completion's row
retains the task's effect obligations; its value and primary/suppressed
payloads must pass the Cell publication capture check. Await replays from the
cell through the awaiter's current evidence without rerunning the action.

The first reported failure schedules still-live jobs in the owning logical
context for stop, newest first. A stopped job remains live while its release
requests move through the ordinary ready, signal, and native-wait queues.
After its terminal `Closed` step, `Work.stopCompletion` captures typed cleanup
failures in release-attempt order. The driver selects the context body first when it
failed; otherwise it selects the lowest registered failed child, then attaches
other child reports and cleanup failures. A nested context publishes the
resulting detached failure tree to its caller after its jobs drain. The caller
replays it through fresh evidence; the root replays its selected report after
drain. Scheduler errors use the separate `Result Async.Error` path unless a
typed failure was recorded. Closing unstarted Work does not execute its body.
An interrupt schedules live jobs for stop at a cooperative checkpoint. During
the drain, the bridge still delivers native readiness but shields repeated
interrupts from a release waiting on that readiness. The driver returns
`Err Interrupted` after quiescence. The bridge closes after its owning
Coroutine scope has drained. A release that never terminates keeps its owner
and native requests live and prevents runner completion.

`Runtime.Completion.dropSuspension` and `dropDrive` remove private control
labels only after an action has completed, so its saved result cannot resume
that producer or driver. `fromFailure` reconstructs a Unit completion from a
detached context report; replay validates the observing row's typed abort
adapter and selects a fresh target. The interpreter and generated runtime use
the same detached payload and suppressed-tree checks.

The [A2 differential fixtures](../../testdata/run/async_a2_contexts.fango)
cover nested lifetimes, outer waits, bound definition-site context, typed
failure ordering, cleanup, caught awaits, expected failures, parent and
unstarted cancellation, and
direct/indirect escape rejection. The low-level
`Runtime.Async.Cooperative` probes continue to exercise the A1 request and
signal protocol independently of the public runner. The
[A3 IO fixture](../../testdata/run/async_a3_io.fango) exercises overlapping
fetches and a Stream pull; the [capacity fixture](../../testdata/run/async_a3_capacity.fango)
exercises saturated admission, cancellation, and cleanup failure. The
[A4 cancellation fixtures](../../testdata/run/async_a4_cancel_release.fango)
exercise suspended release, typed failure selection, and native readiness
during drain in both backends.
[A8 CPU fixtures](../../testdata/run/async_a8_cpu.fango) cover progress,
[Exit helpers](../../testdata/run/async_a8_exit.fango) cover typed failure,
[nested cancellation](../../testdata/run/async_a8_cancel.fango) covers polled
release, and the [module fixture](../../testdata/modules/async_a8_cpu/Main.fango)
covers stored callbacks and separately compiled Direct helpers.
