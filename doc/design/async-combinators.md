# Concurrent Async combinators

The [reference](../reference/library-async-combinators.md) owns observable
behavior. The [cooperative driver](async-cooperative.md) owns the underlying
task and native readiness protocol.

## Completion selection and targeted drain

`AwaitAny` parks one Work package on one registration containing task keys.
Completion publication wakes that registration once and supplies the global
task number. The driver scans recorded notifications oldest first when a
selection arrives after completion; this preserves driver observation order
without polling ready children. `DoneKey` is separate from the early task
notification sent when a failed completion is published. A targeted
`CancelTask` removes the victim's queued or parked authority and schedules
one stop; `WaitDone` resumes only after its worker finishes or its shielded
cleanup closes. After the combinator drains a handle, `ForgetTask` removes
its historical completion key, keeping a long Stream traversal's scheduler
metadata bounded by its active batch. A task already publishing failure continues to its report
instead of losing the context's failure obligation. Root or context
cancellation retains authority over the same victim and prevents a second
stop entry.

`race` brackets both handles and drains them on every exit. Timeout races an
action against the existing scoped native timer, so timer cancellation uses
the same request join and bridge slot release as `Async.IO.sleep`.

## Bounded mapping and subscriptions

`Stream.Concurrent` lives outside the base `Stream` module. A traversal pulls
up to its capacity before spawning a batch. Its bracket owns every handle in
that batch. Ordered emission awaits in input order; unordered emission selects
the first completed handle, removes it from the pending list, and then awaits
its saved value. Early producer stop runs batch cleanup before the enclosing
source cursor closes. The batch boundary bounds both live tasks and retained
results without a mutable queue or a child capturing the upstream cursor.

The tick adapter allocates a bounded native queue per traversal. Its emitter
holds only that queue and a shared bridge pointer; a mutex protects the queue,
overflow state, and current ticket. The producer checks the queue, reserves a
ticket only when empty, arms it with a recheck, and parks on the bridge.
Disarming precedes ticket release. A late duplicate notification is rejected
by the bridge generation check. Closing the subscription cancels and joins
the emitter before releasing its queue and bridge pointer. Native unit tests
exercise exact overflow choices and notification/unregistration overlap;
differential fixtures exercise the same Fango API in both backends.
