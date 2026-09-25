# Roadmap: structured Async and execution models

This document owns the remaining Async work: parallel and mixed execution,
suspending cleanup, and concurrent combinators. It depends on the shared
[coroutine foundation](roadmap-coroutines.md), whose ownership/control
contracts apply without compiler recognition of Async names. The
[effects roadmap](roadmap-effects.md) owns general language extensions.

The structured APIs and examples below specify the full target. The
[cooperative Async surface](reference/library-async-cooperative.md) implements
structured tasks and native IO, with an internal scripted A1 driver.
Ordinary calls, trailing Unit lambdas, callback subsumption, scopes,
and effect instances are implemented foundations; their contracts remain in
the [reference](reference.md). Promote completed behavior there and architecture
into design as stages land; remove completed work or mark its stage DONE under
the [repository milestone rules](../AGENTS.md), without appending implementation
histories or changing stage IDs and titles.

The [API tour](#proposed-api-and-behavior) describes intended programs;
[representation](#api-representation-gate),
[registration](#registration-races), and [failure selection](#failure-selection-gate)
specify selected contracts and remaining implementation gates. The
[stage table](#implementation-stages) gives the delivery order and links the
language prerequisites to their owning roadmap.

## Goals and boundaries

The same task program should run under three explicit executor choices:

- **Cooperative:** one driver advances ready coroutines until they yield,
  wait, finish, or fail. This is the default.
- **Parallel:** independently scheduled tasks, implemented with one goroutine
  driving each task's owned coroutine.
- **Mixed:** a bounded pool of goroutines advances cooperative coroutines;
  suspended tasks do not occupy a worker.

Scheduling, contexts, task handles, completion storage, wait queues, and
combinator policy belong to ordinary Fango and trusted native sidecars. The
compiler supplies general scoped execution, retention/transfer checking, and
native callback contracts. It must not recognize `Async.spawn`, `Async.await`,
or an executor constructor as language primitives.

An executor chooses where/how legal work runs; it does not loosen ownership.
A program safe under the initial cooperative executor must not rely on sharing
parent-local mutable state that would become unsafe merely by changing executor.
The same typing, completion, lifetime, and cleanup rules apply in every mode;
results that depend on effect interleaving or race winners need not be identical.

### Boundaries and non-goals

No `async`/`await` keywords, special blocks, raw continuation callbacks,
continuation cloning, detached tasks, general public channels/select, multicast,
or replay are included. Shared mutable state has its own [STM roadmap](roadmap-stm.md).

The first release uses explicit yield/wait checkpoints; it does not preempt a
Fango coroutine running on the same cooperative worker. Compiler-generated
polling is a committed later responsiveness stage, [A8](#a8-cpu-responsiveness),
over [general execution checkpoints](roadmap-coroutines.md#c7-general-execution-checkpoints).
Preserve existing evaluator interruption/budget checks; before that stage they
are not a portable promise that arbitrary generated loops can be cancelled and
cleaned up promptly. Polling does not imply forced termination of native calls
or nonterminating cleanup.

Native code may execute concurrently for readiness or IO while the cooperative
executor still runs at most one Fango coroutine at a time. Cooperative does
not mean one OS thread for the whole process. No executor uses goroutines as a
replacement for captured effect-continuation frames.

## Proposed API and behavior

### Ordinary calls and overlapping work

```fango
-- Proposed APIs; fetch is domain code, and routine imports are omitted.
fetch : String ->{IO, Async, Fail Error} String
fetch url = Network.get url

Async.run \_ ->
    first = Async.spawn (\_ -> fetch firstUrl)
    second = Async.spawn (\_ -> fetch secondUrl)
    a = Async.await first
    b = Async.await second
    combine a b
```

`Network.get` here is a proposed adapter, not a currently shipped module/API.
`Async` describes scheduling, `IO` external interaction, and `Fail Error` typed
failure. Handling Async cannot erase the other effects. A domain handler can
perform suspending work before its ordinary tail resume:

```fango
effect Catalog
    lookup : String -> String

inProduction action =
    handle action() of
        lookup key ->
            value = fetch key
            resume value
```

A direct call to `fetch` waits in the calling task. `spawn` creates independently
scheduled work; merely mapping `fetch` over a stream is still sequential.
Creation of a task handle does not promise that the child has already executed
or yielded before spawn returns. The cooperative baseline enqueues the child
and continues its parent until a scheduling checkpoint.

### Contexts and ownership

`Async.run` establishes an implicit root task context. `spawn` takes only its
action, without a context argument at each call. `Async.context` establishes a
nested lifetime/cancellation boundary on the same executor:

```fango
Async.run \_ ->
    outer = Async.spawn (\_ -> fetch firstUrl)
    value = Async.context \_ ->
        inner = Async.spawn (\_ -> fetch secondUrl)
        earlier = Async.await outer
        combine earlier (Async.await inner)
    combine value (Async.await outer)
```

The inner context owns `inner`; the root owns `outer`. Awaiting the outer task
inside the inner context does not move its ownership. Context callbacks receive
Unit. Successful context exit waits for every owned task, including work
spawned by children after the context body's result became available.

Ownership flows through scoped effect evidence. Ordinary helpers use their
caller's context; a closure capturing a definition-site context retains that
owner when invoked inside another context. Do not use an ambient mutable
current-context slot, worker identity, or whichever handler is innermost at
resumption to choose a task's owner.

Task handles and closures retaining their context cannot escape it through
returns, ADTs, dictionaries, or stores into longer-lived handlers. Spawning
without a runner remains an unhandled effect. A nested context belongs to its
parent's lifetime, and parent cancellation reaches it and its descendants.

### Results and child captures

`await` observes stored completion; it does not rerun work or advance a shared
continuation a second time. Repeated and multiple simultaneous awaits observe
one completion. Initial reusable success values are immutable and transitively
capture-free. Completion storage must not turn a resource or a bound state
closure into a shareable result.

The same publication check covers primary and suppressed failure payloads.
General completion capture preserves resource obligations; the Async wrapper
rejects a completion retaining a scoped value, including through an opaque
failure snapshot. A capture-free result may contain ordinary immutable ADTs
such as `Result error a`; a task handle remains scoped even after completion.

Children cannot capture parent-local mutable handlers, a borrowed cursor, or
another capability whose access/lifetime is incompatible with independent
execution. The same restriction applies cooperatively: a parent doing
`get; await; put` is not an atomic state update. A child may create and use its
own local state and resources; its cleanup is owned by that child.

Expected failure should be caught inside the child and returned as a Result.
An unobserved child failure still fails its owning context. Catching an await's
failure does not erase the child's failed completion or prevent that context
from reporting it. Awaiting never consumes the task handle or changes its owner.

### Yield and waiting

`Async.yield()` offers scheduling to other ready work and checks cancellation.
It does not promise which task runs next or that another task exists.

Waiting is different:

```text
yield: save current position, put task at ready-queue tail
wait:  save current position, register waiter, leave task off ready queue
wake:  claim that registration, put task on ready queue once
```

Waiting for an already-completed task need not suspend, but still performs the
applicable cancellation check. A task waiting on itself is a deterministic
error to diagnose during execution; broader wait-cycle detection is deferred.
For a cooperative context with only internal waits and no possible producer or
external registration, the driver must report stalled progress rather than
spin. The selected typed vocabulary is `Async.Error` with
`InvalidWorkerCount Int`, `SelfAwait`, and `Stalled`. A1 must implement detection,
not choose new error meanings. Stalled means the relevant driver has no
ready/draining work and no external registration capable of progress; an inner
context awaiting live outer work is not stalled because its own queue is empty.

### Executor selection

```fango
Async.runOn Runtime.Executor.cooperative \_ ->
    Async.await (Async.spawn (\_ -> fetch url))

Async.runOn Runtime.Executor.parallel \_ ->
    Async.await (Async.spawn (\_ -> fetch url))

Async.runOn (Runtime.Executor.mixed 4) \_ ->
    Async.await (Async.spawn (\_ -> fetch url))
```

The proposed constructor names are `cooperative`, `parallel`, and `mixed`.
`parallel` names the execution policy; its goroutine-per-task implementation
does not promise an OS thread per task. `mixed n` bounds the number of cooperative
execution workers, while `parallel` has no configured worker-count bound.
`run` means cooperative execution, and `runOn` also establishes a root context.
Mixed worker count must be positive; `runOn` validates this before starting
the root action, using `InvalidWorkerCount n`.
Nested `context` keeps the executor; executor switching within an active task
and nested independent runners are deferred pending explicit lifetime rules.

The proposed public signatures are:

```fango
spawn : (() ->{Async | e} a) ->{Async | e} Task a e
await : Task a e ->{Async | e} a
yield : () ->{Async} ()
context : (() ->{Async | e} a) ->{Async | e} a
run : (() ->{Async | e} a) ->{e} Result Error a
runOn : Runtime.Executor.Executor -> (() ->{Async | e} a) ->{e} Result Error a

type Error = InvalidWorkerCount Int | SelfAwait | Stalled
```

`Runtime.Executor.Executor` is abstract; `Runtime.Executor.cooperative : Runtime.Executor.Executor`,
`Runtime.Executor.parallel : Runtime.Executor.Executor`, and
`Runtime.Executor.mixed : Int -> Runtime.Executor.Executor` describe
policy values. `runOn` validates a mixed count. `run` is `runOn cooperative`.
Both runners return `Ok` only after successful drain, or `Err Async.Error` after
a scheduler error and drain; domain failures still propagate through `e` under
the [failure policy](#failure-selection-gate). This avoids trying to add
`Fail Async.Error` to a row already containing `Fail DomainError`. Nested
`context` routes scheduler errors to its enclosing runner instead of adding
another Result layer. The earlier run examples therefore return Result values.
The driver records a scheduler error and initiates private owner stop; a source
`Fail` handler cannot swallow that stop. The runner constructs `Err` after drain.

`Async` is nullary. `Task a e` retains the residual row needed to replay its
typed failure; it does not parameterize scheduling or require every child to
have the same row. Spawn charges the child's residual effects even when its
handle is ignored. Yield has no child effects to charge. These signature schemas
also carry the inferred scoped-effect, capture/control and service obligations
below; matching printed rows alone never authorizes a capture or registration.

### API representation gate

The selected representation uses ordinary polymorphic functions around a
**nullary** scheduling effect, with the execution scope's permitted effects
tracked in a hidden contract. The selected names are `Task a e`,
`Runtime.Coroutine.scope`/`create`, and `Async.Error`. The following
tempting operation declarations remain unsupported:

```text
effect Async
    spawn : (() ->{Async | e} a) -> Task a
    await : Task a -> a
```

Each call would choose its own `a`. Instead, private monomorphic operations
obtain `Context` and submit `Request`; polymorphic `spawn`/`await` are ordinary
functions. A context has a hidden owner identity and effect budget `b`, inferred
from its subject and registered work. Each child's independent row `e` must be
included in that budget. Neither `Context`, `Request`, nor the queue's `Job`
needs a public row parameter. This is not unchecked row erasure: C0's general
[scoped work contract](roadmap-execution-contracts.md#scoped-effects-and-work-packages)
preserves and independently verifies the association.

Budgets may contain IO, resumptive domain effects and multiple distinct abort
labels. Two incompatible parameterizations of the same nominal `Fail` label
still cannot occupy one budget; normalize them in child code. Nested contexts
may have narrower budgets, and awaiting an outer task introduces that task's
observation effects without moving its owner. Captured context evidence keeps
its original owner and budget, not the invoking context's.

The representation, in dependency order:

1. `Context` retains a checked registration facet of a C4 `Scope b`, stable
   context identity, and authorized shared services. The facet hides `b` while
   retaining its owner/effect contract. Ordinary helpers obtain caller evidence;
   a bound closure retains its definition-site context. C6c's **split service evidence** supplies
   the invoking task's producer authority separately. A context contains no
   parent pause closure or mutable parent State cell. See the exact general
   [C0 prerequisites](roadmap-coroutines.md#c0-control-and-ownership-contracts).
2. For each spawn at type `a`, allocate a scope-owned C6a write-once cell holding
   a typed completion indexed by `a` and that child's `e`. A `Task a e` contains
   its read capability, owner identity and registration key. It does not expose a coroutine
   handle, publisher or unchecked cast. All public observations retain `a`/`e`.
3. Package the action into a Unit-producing coroutine. Its local scheduling
   interpretation turns operations into `Request` through that producer's
   scoped pause callback. The general typed-completion boundary intercepts
   outward failures and publishes only after cleanup. An adapter closes over the
   cell at its concrete `a`; its outer coroutine is
   `Coroutine Request () () e`. A general work-package operation seals this
   coroutine with its owner, evidence adapters and a proof that `e` fits `b`.
   The resulting `Job` hides `e` without exposing a cast. Captured local
   dictionaries are checked with the rest of the explicit and hidden captures.
4. The Fango driver's queue contains these checked packages, never arbitrary
   result payloads. Requests distinguish yield, wait keys and enqueue of a Job.
   The owning driver opens a package under its scope's budget and sole execution
   authority. A same-shaped job for another owner is not interchangeable.
   Enqueue acknowledges immediately to the spawning parent after adding the child
   at the FIFO tail;
   it does not switch to the child merely because spawn ran.
5. Await registers against the handle's key, then reads its typed cell. Success
   is reusable; failure replays the typed completion through the **awaiting**
   execution's current residual evidence. No operation reruns the action.
   The context keeps its independent failure record even if an await is caught.
   A checked injection from the child's completion row to the owner's budget
   carries unobserved failures, including nested suppressed reports, to context
   drain. It supplies fresh owner evidence there, never a stale exit target.

Registering work exports an owner-indexed latent obligation in addition to the
ordinary call row. A handler around the spawn call cannot consume that
obligation: the independently scheduled child has not failed there. A handler
inside the child can remove an effect before registration. The owning runner
includes remaining obligations in its outward row even if all handles are
ignored; annotations or imported wrappers cannot erase them. C0 owns the exact
inference and packaging contract, rather than granting Async-specific privilege.

Queue mutation and task-state transitions belong to the driver. C4 owns the
cleanup registry and scoped registration facet; C6a owns opaque completion
storage; C6c owns publisher/reader and shared-context permissions. Native code stores a completion
as an opaque same-type value, including any generated replay adapter, and never
invokes it. Merely storing an opaque value is not C6b callback invocation.
No native retained request or Fango callback service is needed by A1.

This is a conditional implementation contract, not a claim that the whole
encoding already typechecks. C0 selects owner-indexed control and scoped effects,
checked work packages, detached typed completion/replay, and private owner stop;
C6c adds shared service evidence with invocation authority. Current Fango can check
ordinary row-indexed Task/Job packaging, nullary scheduling arrow shapes,
scoped Work packages, typed Completion capture/replay, and
[dynamic registration](reference/library-work.md#dynamic-registration),
[typed cells](reference/library-cells.md), and
[shared service authority](reference/library-services.md). A1 must integrate
these implemented prerequisites.
Row-kinded effect parameters are not a prerequisite for this encoding.
The [source probes](../internal/infer/async_feasibility_test.go) and
[scoped-row model](../internal/feasibility/scoped_rows_test.go) distinguish those facts.

## Scheduler state and authority

A context owns task records and their completion cells. A coroutine owns
execution frames. The scheduler has sole authority to advance a runnable task;
a waiter registration temporarily owns the right to make a parked task ready.

| State | Holder of execution/wakeup authority | Legal next states |
| --- | --- | --- |
| Ready | Exactly one queue entry, or a reserved initial goroutine launch | Running or cancelling |
| Running | Exactly one driver/worker | Ready, waiting, cancelling, completed |
| Waiting | Exactly one claimed registration protocol | Ready or cancelling |
| Cancelling | The selected cleanup driver, possibly parked on a cleanup wait | Cancelling or completed |
| Completed | No execution authority; immutable completion remains | Completed |

Status flags are coordination data after the language has proved legal capture
and access. They do not replace the compiler's exclusive-advancement proof.
Completion publication happens once, after required cleanup. Wake all registered
awaiters without duplicating advancement authority. Native cross-goroutine
publication establishes the necessary synchronization before readers observe
results or cancellation state.

The first cooperative policy is FIFO. Requeue voluntary yield at the tail;
append ready completions at the tail; do not spin over parked tasks. Fake
readiness has a scripted order for deterministic tests. Real external arrival
order and parallel execution are not deterministic contracts.

Return to the dispatcher after an event instead of recursively calling the
next task from an effect clause. The count of yielded switches must not grow
the host stack. Bounded worker count is not a bound on queued tasks: explicit
concurrent combinators impose their own admission/result capacities.

### Registration races

Registration and completion must share a protocol that handles completion
before, during, and after waiter publication. This naive sequence loses wakeups:

```text
observe incomplete
completion happens
publish waiter
```

Use a synchronized check/register/recheck or an equivalent atomic state
transition. A completion source can enqueue a notification; it must not invoke
a public raw resumption callback or advance a task concurrently with its driver.

Each registration needs identity/generation sufficient to reject stale delivery
after cancellation or reuse. Duplicate readiness is harmless. Readiness and
cancellation race to claim the same pending registration; only one transition
may publish runnable/draining work. A late notification must not resurrect a
completed task or access a freed native resource.

Test readiness before registration, while publication is in progress, after
cancellation, and after completion. Count live registrations and native requests,
not just goroutines.

## Cancellation and cleanup

### Checkpoint policy

Initial checkpoints are explicit yield and wait operations, including awaiting
completed work and native wait entry/return. Structured-context entry/exit and
task startup also check pending cancellation. Spawn must not admit unbounded
new work into a context already cancelling; reject it through the same internal
cancellation path rather than adding an orphan task.

An arithmetic loop with no checkpoint can occupy a cooperative worker forever.
Go may preempt a goroutine in the parallel executor, but that does not make the
Fango action observe cancellation or run cleanup. No stage promises to kill such
action and safely free resources underneath it. Generated loop polling belongs
to the later C7/A8 execution-checkpoint milestone. Until it lands, the first
release's supported cancellation points remain exactly those listed above.

### Cancellation and draining

Context failure or cancellation requests cancellation of remaining children,
then drains them before exiting. Distinguish cancellation from ordinary typed
failure and early successful stream stop. Application code cannot accidentally
catch an internal cancellation marker as a normal `Fail error` and suppress the
context's obligation to drain. C0's private owner-stop outcome is the selected
encoding. It is not an abort-only source effect and has no application-level
handler. At a checkpoint it unwinds through cleanup to the owned execution
boundary, which publishes cancellation separately from success/failure.

For unstarted work, cancel without executing its body. For parked work, revoke
its pending normal wait and schedule the cleanup path. For running work, record
cancellation and let its driver observe it at a checkpoint; never close its
coroutine from a competing worker. Parent exit waits until that authority has
returned and child cleanup has completed.

The [general cleanup stage](roadmap-coroutines.md#c5-suspending-acquisition-and-cleanup)
owns acquisition registration, release order, and suspension-safe ownership.
Async adds shielding: repeated cancellation cannot restart or interrupt a
release already draining. A cleanup wait remains serviceable even after normal
task waits have been revoked. Keep the cancelling owner alive while it waits.

Cancellation of native work must either cancel and join the request or await
its completion before releasing resources it may still use. Closing a resource
may be the documented interruption mechanism for a particular adapter; do not
apply that policy generically to every handle or assume arbitrary Go calls can
be interrupted. Partial acquisition failure belongs to the acquisition routine.

A nonterminating cleanup prevents context completion. Timeout and race must
not promise immediate return while losing work still owns resources.

### Failure selection gate

Preserve [typed failure snapshots](reference/library-effects.md#fail-and-failure)
and [cleanup precedence](reference/resources.md#cleanup-failures). Child failures
cross a task boundary as completion, not as an attempt to unwind a parent's
currently executing stack. The parent routes an exit only from its own execution
after the necessary drain. Snapshots alone do not authorize a stale exit target.

The selected policy applies to the **recorded set after drain**, not to a
promised parallel interleaving:

1. A non-cancellation context-body failure is primary. Its within-task cleanup
   failures remain attached in release order.
2. Otherwise the failed child with the lowest context-local spawn sequence is
   primary. Assign sequence numbers at registration, before publishing handles;
   never reuse them during the context lifetime. Each nested context contributes
   one report at its owning task's position, preserving its internal tree.
3. Attach other child reports in increasing spawn sequence, then context-owner
   cleanup failures in release-attempt order (LIFO). If no body/child failed,
   the first owner cleanup failure becomes primary. Copy reports when combining
   them; do not mutate a reusable task's completion or flatten nested reports.
4. Cancellation alone contributes no ordinary failure payload. A cleanup failure
   encountered during cancellation is still a failure; an externally stopped
   root propagates private stop to its host after drain. Structured cancellation
   of a nested context propagates through its parent's same private path.

The first observed failure starts cancellation immediately; selection waits for
drain. Another already-running child may still fail, so neither this ordering
nor FIFO can promise the same observed failure set across executors. A body
cancelled because of a child contributes no synthetic body failure that could
hide the child. Scheduler errors use the separate runner error channel selected
in the public signatures; they trigger the same cancel/drain protocol. If a
typed body/child/cleanup failure is also recorded, that failure takes precedence
over a scheduler error. Otherwise choose the first driver-recorded scheduler
error; only the driver's observation order is promised for competing errors.

Await replays the stored primary through the awaiter's live evidence with the
same suppressed tree. It never consumes the completion or acknowledges away the
context failure. Context exit replays its selected report through the context
caller's evidence **after** draining. C0 owns the new typed replay capability;
the [model](../internal/feasibility/tasks_test.go) exercises different typed
payloads and two different observing targets without storing an old exit target.

Before A7 ships, separately settle race winner and timeout deadline/completion
ties. Promptly initiating sibling cancellation and deterministically ordering
already-recorded reports are different requirements. No policy can promise the
same set of observed failures under all schedules. These are explicit entry
gates, not permission to flatten errors into strings or defer cleanup.

## Native integration and executor mechanics

### General native prerequisites

The [C6 contracts](roadmap-coroutines.md#c6-native-retention-and-transfer)
are selected precisely as follows. The general compiler contracts in C0 are
additional prerequisites; none of these native contracts substitutes for them.

| Consumer | Required contracts | Boundary crossed |
| --- | --- | --- |
| A1 fake-readiness tasks | C4, C6a, C6c | Scope-owned same-type completion cells; checked phantom/row indices; one publisher/multiple readers; split shared context evidence and child captures |
| A1 queue/driver | C0 checked work packages, C4 registration facet; no C6b or C6d | Fango queues of scoped packages; no native callback invocation or background request |
| A3 native readiness | C6b, plus C6a already delivered | Scoped registration and host/request retention, generation checks, bounded admission, cancellation and quiescent drain; opaque adapter payloads keep their same-type obligation |
| A4 suspending cleanup | C5 in addition | Retained request/owner survives suspended release |
| A5 and A6 concurrent executors | C6d in addition to C6a–C6c and C5 | Concurrent callback invocation, launch/join, synchronized publication, runtime and native-host audit |

C6a must support the initial completion cell's entire typed payload, including
the residual-row completion package, records/recursive ADTs and generated replay
adapters. A nominal phantom around `Runtime.Native.Any` alone is insufficient. Storage
retains captures and never executes a callable. C6c grants only the specific
cell protocol and scoped shared-service operations; it must not make arbitrary
State, cursor or resource wrappers shareable. C6b is **not** an A1 prerequisite
for this encoding; it becomes mandatory at A3 even if notifications contain only
scalar keys. C5 is required whenever native completion needs suspending cleanup.

Async sidecars implement these general contracts for goroutine launch/join,
synchronized queues, timer registration, and readiness/completion signalling.
General intrinsics may enforce the contracts; task policy must not migrate into
an Async-specific compiler node. C6d's runtime audit includes List's shared chunk
frontier: capture-free immutable values alone do not prove their runtime
representations safe under concurrent use.

Sidecars do not receive Fango callback functions. Typed payloads use
[checked opaque storage](reference/native.md#indexed-native-storage); background
work uses [NativeRequest](reference/library-native-requests.md). FangoHost may
only be used during a native call and cannot be retained for asynchronous work.
The [A3 adapters](reference/library-async-cooperative.md) use these scoped
retention and drain contracts.

Interpreter Core executes beside sidecars in a worker process, but that shared
heap does not make evaluator state thread-safe. Isolate per-execution evidence,
frames, counters, and context state; audit shared lazy globals, caches, output,
input ownership, and reverse-host RPC correlation. Preserve panic/exit translation
and REPL session recovery. Generated Go requires the same semantic contract.

### Readiness and blocking operations

A1 supplied deterministic readiness simulation; the A3 timer and HTTP adapters
submit scoped requests and return control to the cooperative driver. The driver
blocks on its native event source only when no task is runnable. The
[implemented protocol](design/async-cooperative.md) is the basis for later
adapters.

The current Net/File sidecars perform blocking Go calls. Go's runtime can let
other goroutines run, but it cannot advance other Fango coroutines assigned
to the blocked cooperative driver. Use documented readiness operations or a
bounded blocking bridge with explicit queue capacity and admission backpressure.
Do not depend on Go netpoll internals.

Keep request capacity separate from execution-worker count in later adapters.
A slow operation must not create an unbounded goroutine, result queue, or
retained buffer. Admission must wait and respond to cancellation where a
bounded worker bridge is used.

Specify the lifetime of a wait request separately from the lifetime of the
resource used by native work. A producer-local connection cannot simply be
yielded to a scheduler under the coroutine output rules. A registration token
must keep a request within the resource's live execution and guarantee drain
before release. Replacing the resource with an integer ID is not a proof that
native retention is safe. Future connection adapters must retain this
ownership case and its cancellation coverage.

### Cooperative executor

One driver advances task coroutines. The root action is itself scheduled so
its waits and yields obey the same ownership protocol. A pause returns a library
request; the scheduler interprets it, then advances another ready task or waits
for external progress. No goroutine per task is required. A bounded native IO
bridge may have its own goroutines, accounted separately.

All transitions can first be tested without wall-clock sleeps using a fake
event source. FIFO is the initial policy, not a fairness guarantee for work
that never reaches a checkpoint.

### Goroutine executor

`Runtime.Executor.parallel` allocates one goroutine per task to drive its coroutine to
events. On yield,
check cancellation and call a general sidecar implementing `runtime.Gosched`
before continuing. This offers other goroutines a scheduling opportunity;
it is not a wait primitive or a promise that a particular task runs next.

On a pending wait, park the task's driver goroutine on the registration's
completion signal. Blocking that goroutine does not occupy another task's
driver. The saved Fango continuation remains explicit frames; no goroutine is
created for an individual perform, resume, handler, or pipeline stage.

Use the same task/context lifecycle and failure rules as cooperative execution.
No automatic limit on total spawned tasks is implied by Go scheduling; bounded
combinators must enforce their admission limits. Direct blocking native calls
are permitted only where their cancellation/resource contract is satisfied.

### Mixed executor

Use a positive configured number of goroutine workers and, initially, a shared
synchronized FIFO ready queue. Work stealing and affinity are later measured
optimizations. At most that many Fango coroutines execute concurrently;
IO bridge workers are separate and separately bounded.

A worker owns a task only while advancing it. Yield requeues it and releases
the worker; waiting parks the coroutine and releases the worker; completion
publishes its result after cleanup. Waking a task claims a ready entry once.
A resumed task may run on a different worker, so no handler, context, native
resource, or input owner may be selected using worker-local ambient state.

Blocking IO on a worker reduces the usable pool and can stall it entirely.
Use readiness/bridges for adapters intended for this model. Go preempting one
worker allows other goroutines to run; it does not choose a different task from
that same worker's cooperative queue. Count task storage, queue occupancy,
registrations, and native capacity in addition to worker goroutines.

## Concurrent streams and events

The sequential pipeline remains ordinary Stream code:

```fango
Async.run \_ ->
    requests
        |> Stream.map fetch
        |> Stream.filter wanted
        |> Stream.forEach consume
```

`map` waits for each result before requesting the next; suspension alone does
not imply concurrency. The coroutine foundation must let fetch suspend while
an upstream pull is unfinished, without losing exclusive advancement.

The proposed concurrent variants explicitly introduce children:

```fango
requests
    |> Stream.mapConcurrent 8 fetch
    |> Stream.forEach consume
```

`mapConcurrent` preserves input order. Its positive capacity bounds admitted
work and retained completed results together: a slow first result can fill the
bound with later completions, at which point upstream admission stops.
`mapConcurrentUnordered` delivers in readiness order under the same storage
bound. Early downstream stop cancels and drains children before closing upstream
traversal. No compiler recognizes either combinator name.

`Async.race` and `Async.timeout` return only after cancelling/draining losing
work. Their exact result types, ties, and typed configuration errors are A7
entry decisions linked to the failure gate. A deadline requests cancellation;
it is not permission to free resources still used by losing work.

```fango
-- Proposed scoped event adapter.
Events.withSubscription source 32 Events.DropOldest \events ->
    events
        |> Stream.take 10
        |> Stream.forEach consume
```

Subscription starts on traversal, not stream-description construction. Each
reopening creates a fresh subscription; traversal cleanup unregisters it before
subscription-scope exit. Capacity is positive and bounded. Policies are
`Events.Fail`, `Events.DropOldest`, and `Events.DropNewest`; overflow failure is
typed and drains traversal. Backpressure may be offered only if the external
source supports it. External callbacks enter an adapter queue, never a public
resume callback. Test notifications racing with overflow, cancellation, and
unregistration. General multicast/replay remain outside scope.

## Implementation stages

The [Stream performance prerequisite](roadmap-coroutines.md#performance-prerequisite)
is satisfied with explicitly accepted remaining costs. Continue these stages
with the same comparison as regression evidence and their own acceptance gates.

Stage IDs are local to Async. The dependency table is authoritative; general
language work is specified once in the coroutine roadmap.

| Stage | Dependencies | Usable result |
| --- | --- | --- |
| A0: Library representation contract | C0/C4 contract drafts; no C1–C4 implementation prerequisite | DONE: selected representation and proof models; review before C1 |
| A1: Deterministic cooperative tasks | A0, C4, C6a, C6c (including shared service evidence); C1 implements C0's general extensions | DONE: executor-neutral Async operations with cooperative dynamic spawn/yield/await and scripted waits; no C6b prerequisite |
| A2: Structured contexts and failures | A1 | DONE: root/nested lifetimes, cancellation, reusable results, synchronous cleanup |
| A3: Native readiness and IO | A2, C6b; C6a if adapter values require it | DONE: bounded native timers and HTTP GET, cooperative wakeup and REPL interruption |
| A4: Suspending cleanup integration | A3, C5 | Cancellation/drain through asynchronous acquire/release |
| A5: Goroutine executor | A4, C6d | One driver goroutine per task |
| A6: Mixed executor | A4, C6d | Bounded pool advancing cooperative tasks |
| A7: Concurrent combinators and events | A4; repeat executor coverage after A5/A6 | Bounded mapping, race, timeout, subscriptions |
| A8: CPU responsiveness | A2, C7; repeat coverage for every delivered executor | Scheduling/cancellation checkpoints in generated CPU work |
| A9: Measured optimization | Functional stages under measurement complete | Evidence-backed improvements without semantic changes |

A7 need not wait for parallel execution: it can first ship cooperatively after
A4. A5 and A6 share the C6d safety gate; implementing goroutine-per-task first
may provide a useful test harness, but it is not a semantic prerequisite for the
worker pool. A8 can begin after A2/C7 without waiting for parallel execution,
and its tests must be repeated for subsequently delivered executors.

A3 supplies the first practical cooperative IO release. The concurrent server
still needs the wider adapter and executor work in the
[IO roadmap](roadmap-io.md). The full Async target includes all three executors
and A8 responsiveness.

### A0: Library representation contract

**DONE — feasibility contract and focused proof models, conditional on the
explicit general prerequisites below.** No public Async behavior is implemented.

The selected [representation](#api-representation-gate),
[failure policy](#failure-selection-gate) and
[native prerequisite table](#general-native-prerequisites) are authoritative.
C0 owns owner-sensitive control, scoped effects/work packages, detached typed completion
and replay, and private owner stop. C4 owns scope/create, the cleanup registry
and its checked registration facet. The public effect is nullary `Async`;
`Task a e` retains its own observation row. Row-kinded effects are not required.
C6a supplies typed completion cells; C6c supplies checked child captures and
shared service evidence whose context and execution authorities are separate.
Current ordinary captured evidence cannot supply the latter automatically.
C6b is not needed for fake waits, and C6d is not needed for cooperative execution.

**Evidence and limits:** the [source probes](../internal/infer/async_feasibility_test.go)
check ordinary Task/Job packaging and nullary scheduling arrow shapes, helpers
and stored callbacks with Int/String results, wrong-result rejection, unsupported
polymorphic operations and native boundaries, and rejected omission of unawaited
IO. The [scoped-row model](../internal/feasibility/scoped_rows_test.go) additionally
checks latent registration effects through handlers, nested owners and imported
summaries; its unindexed queue seals work with distinct child rows and typed
failure injections into a hidden owner budget. It passes the budget skolem and
adapters explicitly at the compiler-contract boundary. C1 implements the
[Work contract](reference/library-work.md) and
[Completion contract](reference/library-completion.md). The
[executable representation probe](../testdata/run/async_c1_representation.fango)
rechecks these APIs in both backends: Int/String Task observations, homogeneous
Unit work packages, a nullary effect interpreted with a local pause callback,
cleanup before typed failure publication, and repeated success/failure replay.
Its ordinary publication callbacks stand in for the typed Cell API;
it does not implement dynamic spawn or the shared scheduling service. The
[typed model](../internal/feasibility/tasks_test.go)
has no erased heterogeneous payload register: generic task/cell types feed
uniform execution closures. It exercises repeated observation, outer task inside
inner context, captured outer service with a different producer, typed expected
Result, child failure plus cleanup, fresh-evidence replay, unstarted cancellation,
stable failure ordering, descendant allocation after body completion and removal
of finished execution entries. The [ownership model](../internal/feasibility/control_test.go)
rejects escaped handles/pause, short-lived replies and unsafe child captures.
The models cover proposed behavior beyond that executable probe. C4 additionally
verifies real [dynamic allocation and registration](design/coroutines.md#dynamic-scope-registry)
in both backends. C6a/C6c additionally verify real typed storage and implicit
service adapters, including independently registered children and nested pulls.
No public scheduler is delivered here.

**Stopping point:** joint C0/A0/C4-design gate, with C1's executable facilities
rechecked by C2 and scope allocation/storage/registration verified by C4. Retain
the C6a/C6c API proofs when implementing A1; never treat a successful Go
model as proof that the current source language accepts those APIs.

### A1: Deterministic cooperative tasks

**DONE.** The [A1 Async API](reference/library-async-cooperative.md)
and [driver design](design/async-cooperative.md) own the implemented contract.
Its differential fixtures cover alternating tasks, parked work, early and
duplicate notification, repeated await, Stream pull suspension, self wait, and
stalled progress. Task bodies call `Async` functions; `Async.Cooperative.run`
selects the executor only at the root. `Async` has no dependency on the
cooperative driver. A closed-row check rejects an unawaited child's
unhandled effect. The dynamic storage gate checks A1 terminal execution in the
generated backend and the shared C4 cleanup primitive in the interpreter. A2
extended this task protocol with structured contexts, typed child failures,
and `runOn`.

### A2: Structured contexts and failures

**DONE.** The [structured Async API](reference/library-async-cooperative.md)
and [cooperative driver design](design/async-cooperative.md) own the implemented
contract. The [A2 differential fixtures](../testdata/run/async_a2_contexts.fango)
cover nested and root ownership, outer awaits, bound definition-site context,
grandchildren, caught await without failure erasure, parent cancellation,
and typed cleanup ordering in both
backends. A4 adds suspending cleanup.

### A3: Native readiness and IO

**DONE.** [Cooperative Async](reference/library-async-cooperative.md) owns the
timers, HTTP GET, capacity, errors, and cancellation contract;
[the driver](design/async-cooperative.md) and
[native requests](design/native-requests.md) own its lifetime and wakeup
invariants. The [IO differential fixture](../testdata/run/async_a3_io.fango)
requires two local HTTP requests to overlap and suspends a Stream pull. The
[capacity fixture](../testdata/run/async_a3_capacity.fango) covers saturation,
immediate completion, cancellation during native work, and cleanup failure in
both backends. Native bridge and request runtime checks cover duplicate and
stale events, queued readiness at interruption, and quiescent drain. The
[REPL integration check](../cmd/fango/repl_interrupt_test.go) covers Ctrl-C
during native wait and readLine, with prompt and session recovery. C6b's
retention checker verifies enclosing and shorter-lived bridge captures.

### A4: Suspending cleanup integration

Use C5 for asynchronous acquisition/release. Keep cancelling owners scheduled
until drain finishes; shield releases against repeated cancellation. Keep the
same Runtime.Scope.bracket surface and domain IO/failure effects.

**Acceptance:** cancel during acquire, body, and release; deliver duplicate
readiness; fail nested releases; verify one release attempt and reverse order.
No owner or native request is freed while release waits. Nonterminating cleanup
has the documented non-completion behavior.

**Stopping point:** the lifetime foundation for race, timeout, and subscriptions.

### A5: Goroutine executor

Implement runOn with Runtime.Executor.parallel over C6d's checked concurrent callbacks,
launch/join, synchronization, and host protocol. Use one goroutine per task and
Gosched for voluntary yield; preserve explicit coroutine frames.

**Acceptance:** CPU tasks overlap; pending await parks its own goroutine; safe
child-local state/resources work; unsafe explicit and hidden captures fail
statically. Repeat context, cleanup, typed completion, and cancellation scenarios
in both backends. Run targeted race detection on runtime/native coordination
and emitted concurrent fixtures, including concurrent extension of a shared List.
The C6d runtime audit is an entry gate, not follow-up hardening. Compare
invariants, not interleaving traces.

### A6: Mixed executor

Implement Runtime.Executor.mixed with a positive worker count and the shared ready queue,
after A4 and C6d. A5 may precede it for test reuse but is not required.
Make ownership handoff explicit from worker to registration/queue and back.
Start without work stealing or worker affinity.

**Acceptance:** one worker behaves cooperatively; multiple workers run safe CPU
work concurrently. More parked tasks than workers do not exhaust the worker
pool. A task can migrate between workers without changing handler/context
identity. Readiness/cancellation/worker-completion races cannot double-advance.
Blocking bridge capacity is independent of execution worker count.

**Stopping point:** a worker-pool executor with the same public lifetime contract
as cooperative execution. Together A5 and A6 complete the three-executor choice,
regardless of which concurrent policy is implemented first.

### A7: Concurrent combinators and events

Choose result types and tie/error policies, then implement bounded ordered and
unordered mapping, race, timeout, and subscriptions as library combinators.
Add only source-specific native event registration where needed.

**Acceptance:** slow-first ordered mapping bounds retained results; unordered
mapping follows readiness; take/early failure cancels/drains before upstream
close. Race/timeout drain losers. Exercise all overflow policies, reopening,
zero/negative capacities, and callback/unregister races. After completion no
registration, unfinished task, or native request remains owned accidentally.
Repeat the applicable cases across all delivered executors.

### A8: CPU responsiveness

Integrate C7's general checkpoints with the task lifecycle and every delivered
executor. A cooperative poll can return the task to the ready queue; a pool poll
releases its worker; parallel mode observes cancellation and offers a scheduling
opportunity. The same program must not lose cancellation coverage when executor
selection changes. Specify the relationship between automatic polling and
explicit yield without promising identical schedules or strict fairness.

**Acceptance:** a CPU-only task and another ready task both make progress; cancel
CPU work reached through generated loops, self-tail calls, Direct helpers,
stored callbacks, and module boundaries. Verify typed failure propagation,
one release attempt per acquired resource, shielding, and sole advancement.
Exercise the interpreter and emitted Go, including already-completed awaits and
polls racing with context cancellation. Native requests still obey their own
drain contract, and a release that never finishes still prevents context exit.

**Stopping point:** every delivered executor supports documented scheduling and
cancellation safe points for generated CPU work as well as explicit waits;
later executors must satisfy the same coverage before delivery.
Deterministic progress fixtures gate correctness; idle-host measurements cover
polling overhead and latency without turning load-sensitive timing into ordinary
test requirements.

### A9: Measured optimization

Measure synchronous completion paths, queueing, frame reuse, result retention,
callback/evidence adapters, and selective stage fusion. Preserve demand,
effect order, cleanup, captures, and exclusive advancement. Coordinate with
[calling conventions](roadmap-calls.md). No optimization gates earlier API use.
The implemented [cooperative comparison](design/verification.md#cooperative-async-comparison)
retains the historical native Async baseline and a 1.10 sustained-yield target;
extend its coverage when native readiness is implemented.
Reduce the setup and completion overhead exposed by its short-task and
no-yield controls.

Use an otherwise idle machine. Separate setup, hot execution, allocation,
maximum live frames, cancellation/drain latency, native capacity, compilation
latency, and output size. Keep a change only with measured benefit and unchanged
semantic gates. Source non-escape does not promise stack allocation; bounded
workers do not promise bounded total task memory.

## Acceptance and verification

| Area | Required evidence |
| --- | --- |
| Composition | Pure/IO/failing/suspending functions through the same helpers, stored callbacks, ADTs, dictionaries, and modules |
| Context identity | Caller evidence versus captured evidence; nested contexts; grandchildren; rejected direct/indirect escape |
| Completion | One execution; repeated/concurrent awaits; immutable capture-free results; typed child failures |
| Cancellation | Every supported checkpoint; unstarted work; running-to-park race; shielded release; native drain |
| CPU responsiveness | Generated loops and Direct callees; same checkpoint coverage across executors after A8; no forced native termination |
| Wait protocol | Complete before/during/after registration; duplicate/stale delivery; competing cancel; no resurrection |
| Stream integration | Suspension during an unfinished pull; exclusive borrow retained; early downstream stop drains upstream |
| Native host | Interpreter/generated parity; input ownership; output coordination; panic/exit handling; REPL recovery |
| Executors | Same lifetime rules; unsafe captures rejected; bounded mixed workers; goroutine-per-task accounting; migration |
| Runtime sharing | C6d audit; shared List frontier; publication, lazy globals, evidence, native host, and cleanup race safety |
| Capacity | Task admission, completed results, event queues, blocking bridge, timers/registrations counted separately |
| Failures | Selected primary/secondary policy; nested cleanup reports; race/timeout ties; no stale parent-stack unwinding |

Retain [repository gates](../AGENTS.md) and
[verification contracts](design/verification.md): independent Core/Machine lint,
malformed IR tests, interpreter/compiler differential tests, functional tests,
formatting, deterministic emission, and go vet. Include early curried effects,
equation groups, nested handlers, staging restrictions, step limits, and REPL
rollback where the general contract applies. Pure synchronous pipelines must
retain no mandatory Async scheduler/goroutine/channel machinery.

Use exact expected traces for deterministic scheduling fixtures and invariants
for parallel fixtures. Use synchronization barriers rather than arbitrary sleeps
to expose races. Count live owners, frames, handles, queued results,
registrations, native requests, and workers; goroutine counts alone cannot show
that cancellation drained resources. Run grammar verification if implementation
changes syntax, and update exact TextMate rules and cases in that change.

Build-check benchmarks with `go vet ./benchmarks` during implementation; do not
run timing benchmarks under ordinary load. Documentation-only changes need link,
anchor, dependency, API, and proposed/implemented-label checks, not execution or
performance suites.
