# Roadmap: structured Async and execution models

This document owns the **proposed** Async library: tasks and contexts,
cooperative scheduling, parallel and mixed execution, cancellation, native
readiness, and concurrent combinators. It depends on the shared
[coroutine foundation](roadmap-coroutines.md), whose ownership/control
contracts apply without compiler recognition of Async names. The
[effects roadmap](roadmap-effects.md) owns general language extensions.

The APIs and examples below are acceptance specifications, not implemented
features. Ordinary calls, trailing Unit lambdas, callback subsumption, scopes,
and effect instances are implemented foundations; their contracts remain in
the [reference](reference.md). Promote completed behavior there and architecture
into design as stages land; remove completed work or mark its stage DONE under
the [repository milestone rules](../AGENTS.md), without appending implementation
histories or changing stage IDs and titles.

The [API tour](#proposed-api-and-behavior) describes intended programs;
[representation](#api-representation-gate),
[registration](#registration-races), and [failure selection](#failure-selection-gate)
identify decisions that gate implementation. The
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
spin. The typed error vocabulary is an A1 entry decision.

### Executor selection

```fango
Async.runOn Executor.cooperative \_ ->
    Async.await (Async.spawn (\_ -> fetch url))

Async.runOn Executor.parallel \_ ->
    Async.await (Async.spawn (\_ -> fetch url))

Async.runOn (Executor.mixed 4) \_ ->
    Async.await (Async.spawn (\_ -> fetch url))
```

The proposed constructor names are `cooperative`, `parallel`, and `mixed`.
`parallel` names the execution policy; its goroutine-per-task implementation
does not promise an OS thread per task. `mixed n` bounds the number of cooperative
execution workers, while `parallel` has no configured worker-count bound.
`run` means cooperative execution, and `runOn` also establishes a root context.
Mixed worker count must be positive; A0 selects the common typed
configuration-error contract before implementation.
Nested `context` keeps the executor; executor switching within an active task
and nested independent runners are deferred pending explicit lifetime rules.

### API representation gate

The examples specify user behavior, not a claim that this is a legal effect
body today:

```text
effect Async
    spawn : (() ->{Async | e} a) -> Task a
    await : Task a -> a
```

Each call would choose a different `a` and possibly residual effects. Current
operation-local polymorphism does not support this shape. A0 must demonstrate
a fully typed library encoding before committing full public type signatures.
`Task a` in discussion is shorthand; whether its representation needs an
additional row index must be settled with failure/evidence propagation.

Prefer polymorphic ordinary functions around a smaller scheduling protocol,
with typed result storage and appropriately packaged Unit-producing actions.
Prove that handles for Int and String tasks can coexist in one context while
each await retains its own type. A heterogeneous queue of coroutine owners
also needs an explicit checked representation; it is not solved by renaming a
Go `any` field. If the proposed encoding requires a new general facility,
record that dependency in effects/coroutines before implementing it.

The decision must cover residual effect rows, context identity, child failure
payloads, local dictionaries, both backend representations, and capture-free
result checking. Do not claim C0–C3 automatically solve dynamic allocation or
polymorphic task completion.

Locate mutable scheduling state explicitly. A library handler cannot bypass
the ban on shared parent-local State merely because it implements Async.
Cooperative queue mutation can belong to the driver, reached through task
requests; shared native state needs an explicit synchronization/transfer contract.
Show how the context evidence used by children satisfies those rules. Any
general native value or retention facility required by the chosen encoding
becomes an A1 prerequisite, even if real IO is not delivered until A3. Name the
required C6a–C6d contracts explicitly. C6c's transferable-capture rules apply
from the first independently scheduled child, even with one cooperative driver.

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
context's obligation to drain; the A0 encoding must make that boundary explicit.

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

Before A2 ships, define concurrent primary selection and secondary ordering.
The policy must distinguish body failure, failed children, cleanup failures, and
cancellation, preserve the existing within-task precedence, and avoid depending
on a reproducible parallel interleaving. Stable context-local task sequence
numbers are a candidate ordering key; wall-clock timestamps are not a typing or
ordering proof. Specify how nested reports preserve their structure and how an
await reports failure without consuming the stored completion.

Before A7 ships, separately settle race winner and timeout deadline/completion
ties. Promptly initiating sibling cancellation and deterministically ordering
already-recorded reports are different requirements. No policy can promise the
same set of observed failures under all schedules. These are explicit entry
gates, not permission to flatten errors into strings or defer cleanup.

## Native integration and executor mechanics

### General native prerequisites

The [C6 contracts](roadmap-coroutines.md#c6-native-retention-and-transfer)
separate typed opaque values (C6a), scoped requests/retained callbacks (C6b),
shared and transferable capabilities (C6c), and concurrent invocation/runtime
safety (C6d). A0 selects any C6a/C6b prerequisites for task representation;
C6c gates independently scheduled child captures, C6b gates native readiness,
and C6d gates both concurrent executors. C5 is additionally required whenever
native completion needs suspending cleanup.

Async sidecars implement these general contracts for goroutine launch/join,
synchronized queues, timer registration, and readiness/completion signalling.
General intrinsics may enforce the contracts; task policy must not migrate into
an Async-specific compiler node. C6d's runtime audit includes List's shared chunk
frontier: capture-free immutable values alone do not prove their runtime
representations safe under concurrent use.

Current sidecars accept neither Fango functions nor general polymorphic values.
Current FangoHost may only be used during a native call and cannot be retained
for asynchronous work. Follow the [native reference](reference/native.md) until
C6b extends it. Background work needing input/output must use an explicitly
scoped request/host protocol, not capture that global object illegally.

Interpreter Core executes beside sidecars in a worker process, but that shared
heap does not make evaluator state thread-safe. Isolate per-execution evidence,
frames, counters, and context state; audit shared lazy globals, caches, output,
input ownership, and reverse-host RPC correlation. Preserve panic/exit translation
and REPL session recovery. Generated Go requires the same semantic contract.

### Readiness and blocking operations

Start with deterministic readiness simulation. A real adapter then submits
work, publishes its wait registration, and returns control without blocking a
cooperative execution driver. When no task is runnable, the driver may block
waiting for its event source; that is different from blocking while runnable
work exists.

The current Net/File sidecars perform blocking Go calls. Go's runtime can let
other goroutines run, but it cannot advance other Fango coroutines assigned
to the blocked cooperative driver. Use documented readiness operations or a
bounded blocking bridge with explicit queue capacity and admission backpressure.
Do not depend on Go netpoll internals.

Separate request capacity from execution-worker count. A slow operation must
not create an unbounded goroutine, result queue, or retained buffer. Admission
itself may need to wait and respond to cancellation. Readiness, native completion,
and cleanup registration must be tested in both immediate and delayed paths.

Specify the lifetime of a wait request separately from the lifetime of the
resource used by native work. A producer-local connection cannot simply be
yielded to a scheduler under the coroutine output rules. A registration token
must have a checked/native contract that keeps the request within the resource's
live execution and guarantees drain before release. Replacing the resource with
an integer ID is not a proof that native retention is safe. Make this ownership
case part of the A3 adapter design and its cancellation tests.

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

`Executor.parallel` allocates one goroutine per task to drive its coroutine to
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

Stage IDs are local to Async. The dependency table is authoritative; general
language work is specified once in the coroutine roadmap.

| Stage | Dependencies | Usable result |
| --- | --- | --- |
| A0: Library representation contract | C0/C4 contract drafts; no C1–C4 implementation prerequisite | Early feasibility gate for typed task/context/request design |
| A1: Deterministic cooperative tasks | A0, C4, C6c; C6a/C6b if selected by A0 | Dynamic spawn/yield/await with fake waits |
| A2: Structured contexts and failures | A1 | Root/nested lifetimes, cancellation, reusable results, synchronous cleanup |
| A3: Native readiness and IO | A2, C6b; C6a if adapter values require it | Overlapping IO with bounded native work |
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

A3 is the first practical Async release and the gate for a real concurrent
server, not merely A1's simulated scheduler. It includes cooperative structured
IO and synchronous release. Neither the other executors nor generated CPU polling
blocks that release; their absence must remain explicit in its behavior and
diagnostics. The full target includes all three executors and A8 responsiveness.

### A0: Library representation contract

Run this feasibility gate alongside C0 and the design portion of C4, before C1
implementation. It consumes proposed contracts and focused inference/Core
fixtures or narrow prototypes, not completed coroutine or allocation APIs.
Revalidate the encoding against executable C1 before the C2 Stream migration.

Resolve the API representation gate: dynamic owner association, task/result
storage, heterogeneously typed tasks, residual effects, completion exits,
cancellation representation, and general sidecar requirements. Choose full
public signatures and the typed configuration/stalled-progress error vocabulary.
Retain abstract context identity through captured evidence.

**Acceptance:** small typed examples cover Int and String tasks in one context,
helpers and stored closures, an outer handle awaited inside an inner context,
expected child failure as Result, and rejected escaping context/resource values.
Show each required compiler capability is general and owned by C0–C6d, or record
a specific additional language dependency before proceeding. Select C6a/C6b
only where the representation requires them; C6c is mandatory for child captures.
Include typed failure/evidence routing and cancellation that cannot be swallowed
as an ordinary Fail payload. A renamed erased runtime field is not a typed
heterogeneous-task representation.

**Stopping point:** implementable library contract before the shared execution
migration begins. If the encoding needs another general facility, specify its
contract and dependency here before proceeding; do not silently substitute an
Async-specific intrinsic. Examples dependent on unimplemented facilities remain
clearly marked, not advertised as working.

### A1: Deterministic cooperative tasks

Implement a Fango driver, FIFO ready queue, task records, spawn, yield, and
wait/await with scripted readiness. Dynamic allocation uses C4; do not keep a
recursive lexical scope open for every historical spawn. The initial runner is
an internal foundation for A2 rather than a public partial Async.run contract.

**Acceptance:** two tasks alternate; a parked task is absent from the ready
queue; early/duplicate notification cannot lose or duplicate work; repeated
await observes one coroutine. A task suspends within a Stream pull. Test self
wait and a stalled internal context. Completion releases execution storage.

### A2: Structured contexts and failures

Expose run/context with implicit root ownership, nested lifetime rules,
reusable capture-free results, child completion, and synchronous cancellation
cleanup. Finalize the failure selection gate. Detect cancellation at explicit
checkpoints and reject unsafe captures even in cooperative mode.

**Acceptance:** root owns direct spawns; nested contexts drain only their own
work; awaiting an outer task does not transfer it; a definition-site closure
retains its original context. Context exit includes grandchildren spawned after
body completion. Failed children cancel siblings; catching await does not erase
a failure; expected failures handled in children do not fail the context.
Cancellation before first execution runs no child body. Cleanup failure reports
remain typed and ordered under the selected policy.

**Stopping point:** a useful structured task library with fake readiness and
synchronous cleanup, without a real-IO responsiveness claim.

### A3: Native readiness and IO

Add scoped timers/network or bounded blocking bridges, synchronized registration,
admission backpressure, and native request draining. Establish C6b and any C6a
adapter storage before crossing values/callbacks they would otherwise forbid.
Integrate REPL host/input cancellation using the same ownership protocol.

**Acceptance:** two fetches overlap and a sequential Stream pipeline can suspend
inside a pull. Exercise saturated bridge capacity, completion-before-register,
duplicate/stale notifications, cancellation during native work, and cleanup
failure. Ctrl-C at supported checkpoints drains outstanding native work,
restores prompt/input ownership, and preserves session state; include queued
readiness and readLine interruption. Document adapters that cannot interrupt a
host operation promptly rather than claiming universal interruption.

**Stopping point:** real cooperative structured IO with synchronous release.

### A4: Suspending cleanup integration

Use C5 for asynchronous acquisition/release. Keep cancelling owners scheduled
until drain finishes; shield releases against repeated cancellation. Keep the
same Scope.bracket surface and domain IO/failure effects.

**Acceptance:** cancel during acquire, body, and release; deliver duplicate
readiness; fail nested releases; verify one release attempt and reverse order.
No owner or native request is freed while release waits. Nonterminating cleanup
has the documented non-completion behavior.

**Stopping point:** the lifetime foundation for race, timeout, and subscriptions.

### A5: Goroutine executor

Implement runOn with Executor.parallel over C6d's checked concurrent callbacks,
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

Implement Executor.mixed with a positive worker count and the shared ready queue,
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
