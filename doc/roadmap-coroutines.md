# Roadmap: owned coroutines and the Stream foundation

This document owns remaining coroutine capabilities and the migration of Stream
onto an ordinary library surface. Scoped typed execution is described in the
[reference](reference/library-coroutines.md) and [design](design/coroutines.md).
[Effects](roadmap-effects.md) owns the surrounding language direction and
unrelated handler questions. [Async](roadmap-async.md) owns scheduling and task
policy. Nothing here changes the implemented [stream contract](reference/library-streams.md)
or [handler restrictions](reference/effects.md#resume-discipline) until its
implementation stage lands.

The first usable milestone is C0–C3, with Async's early A0 representation gate:
checked typed coroutines, ordinary Stream/Iterator wrappers, and a deterministic
scheduler demonstration. C4–C7 are separately gated capabilities needed by later
consumers. Stage numbers are local to this document; dependencies name stages
rather than assuming one unbroken global ordering.

Read the [interface](#proposed-public-interface) for the library surface,
[ownership](#ownership-and-lifetime-contracts) and
[routing](#effect-routing-and-nested-suspension) for the proof obligations,
[migration](#stream-and-iterator-migration) for the library transition, and
[stages](#implementation-stages) for delivery and acceptance.

## Motivation and existing machinery

An effect instance can remember which handler an operation should call. A
coroutine must additionally remember where execution stopped:

```fango
-- Producer shape; emit is an ordinary user-defined effect operation.
producer() =
    emit 10
    print "between"
    emit 20
```

A pull consumer should receive 10, do something else, then resume production
and see `between` printed before receiving 20. Calling a consumer from an
ordinary tail-resumptive handler is a push traversal; it does not preserve
this independently drivable position after the consumer's first call returns.

The implemented [typed coroutine protocol](design/coroutines.md) reuses
[Machine frames and dispatch](design/machines.md) and
[capture contracts](design/ownership.md). Later stages extend that foundation
with dynamic ownership, suspending cleanup, and native capabilities.
Source entrypoints include
[semantic Core](../internal/core/core.go), [Machine IR](../internal/machine/ir.go),
the [dispatcher](../runtime/fangort/machine.go),
[nested transfers](../runtime/fangort/traversal.go), and
[cursor owner](../runtime/fangort/iterator.go).

## Proposed public interface

The scoped interface is implemented in [Coroutine](reference/library-coroutines.md).
[Work](reference/library-work.md) and [Completion](reference/library-completion.md)
provide the supporting package and outcome APIs. Dynamic allocation remains [C4](#c4-scope-owned-dynamic-allocation).

### Typed exchange example

See the implemented [exchange example](reference/library-coroutines.md#exchange-and-lifecycle).

### Meaning of the producer callback

The [producer contract](reference/library-coroutines.md#exchange-and-lifecycle)
includes lazy application, strict arguments, and typed initial input/replies.

## Lifecycle and outcomes

The [lifecycle table](reference/library-coroutines.md#exchange-and-lifecycle)
owns terminal states and synchronous close. [C5](#c5-suspending-acquisition-and-cleanup)
extends acquisition/release to suspension.

## Ownership and lifetime contracts

### Handles and exclusive execution

See [ownership and effects](reference/library-coroutines.md#ownership-and-effects)
for aliases and exclusive advancement, including foreign suspension.

### Producer execution capability

Pause authority is separate from a driver handle; the implemented
[control proof](design/coroutines.md#protocol-and-control-proof) tracks it.

### Values crossing the boundary

The [reference](reference/library-coroutines.md#ownership-and-effects) owns
request/result lifetime and conservative input/reply retention.

### Scope escape

The [reference](reference/library-coroutines.md#ownership-and-effects) owns
lexical non-escape. [C4](#c4-scope-owned-dynamic-allocation) changes allocation
ownership without permitting detached scoped resources.

## Effect routing and nested suspension

### Captured and per-advance evidence

See [dispatch](design/coroutines.md#lowering-and-dispatch) for implemented
forwarding and definition-site evidence.

### Foreign suspension

Nested coroutine execution and the [Stream wrappers](design/coroutines.md#ordinary-pull-libraries)
are implemented. The scheduler demonstration remains
[C3](#c3-cooperative-scheduling-demonstration).

### Discharge proof gate

Owner-sensitive inference and independent Core reconstruction are described in
[the implemented control proof](design/coroutines.md#protocol-and-control-proof).
The [C0 model](../internal/feasibility/control_test.go) remains a prerequisite
regression gate, alongside production inference, codec, and backend tests.

## Stream and Iterator migration

**DONE.** Stream and Iterator use the [ordinary wrappers](design/coroutines.md#ordinary-pull-libraries)
over Coroutine. The [reference](reference/library-streams.md) owns their
signatures, ordinary Yield handling, Drive annotations, demand, and ownership.
There are no Stream-specific intrinsics or compatibility aliases.

## Compiler and backend work

Implemented scoped execution is described in [the Coroutine design](design/coroutines.md).
Later stages extend these checked nodes, contracts, and dispatch paths; they
must preserve synchronous Direct/Exit code, verified liveness, module-owned
callable families, and typed projections on both sides of private registers.
Native storage remains the separate [C6a](#c6a-typed-opaque-values) boundary.

## Implementation stages

Every stage includes its applicable tests and updates implemented
reference/design descriptions only when behavior ships. C0 is a design/checker
checkpoint; C1–C3 together are the first usable replacement foundation.

| Stage | Required predecessors | Stopping point |
| --- | --- | --- |
| C0: Control and ownership contracts | Implemented Iterator foundation | DONE: feasibility contract and focused models |
| C1: General typed execution | C0, early Async A0 contract gate | DONE: executable scoped Coroutine, Work, and Completion APIs |
| C2: Ordinary Stream and Iterator | C1 | DONE: Stream behavior with no Stream-specific intrinsics |
| C3: Cooperative scheduling demonstration | C2 | Shared foundation demonstrated without native concurrency |
| C4: Scope-owned dynamic allocation | C3, A0; scope design begins with C0 | Coroutines safely retained by a live dynamic owner |
| C5: Suspending acquisition and cleanup | C1; nested fixtures from C3/C4 | Owners remain live through suspended cleanup |
| C6a: Typed opaque values | C0/A0 representation decisions; C4 for selected task cells | Checked native storage and same-type return; required before A1 |
| C6b: Scoped native requests and retention | C4; C5 only for suspending cleanup | Bounded requests and callbacks with checked quiescence |
| C6c: Shared and transferable capabilities | C0/A0 capture contracts; C4 ownership | Safe child captures, including explicitly shared native values |
| C6d: Concurrent invocation and runtime safety | C6b, C6c; C6a when values cross opaquely | Checked concurrent callbacks and race-safe runtime representations |
| C7: General execution checkpoints | C1; Async A2 as integration consumer | Compiler-generated scheduling/cancellation points in CPU work |

C0, the design part of C4, and [Async A0](roadmap-async.md#a0-library-representation-contract)
form the joint feasibility gate. Focused source probes and test-only models
remain prerequisite checks for the implemented scoped API. The selected Async
task encoding is rechecked by the [executable C1 representation probe](../testdata/run/async_c1_representation.fango)
used by C2. C4/C6 implementations are later prerequisites of A1, not circular
prerequisites of this design gate.

### C0: Control and ownership contracts

**DONE — feasibility contract and focused proof models.** Production scoped
execution belongs to C1; dynamic allocation and native transfer remain C4/C6.

**Dependencies:** the implemented cursor/instance foundation.

Selected contracts are authoritative in [ownership](#ownership-and-lifetime-contracts),
[owner-sensitive discharge](#discharge-proof-gate), and [C4](#c4-scope-owned-dynamic-allocation).
No operation-local polymorphism, public continuation, or Async-specific intrinsic
is required. Plain nominal-row subtraction is insufficient.

Additional general prerequisites are specified in the
[execution contract topic](roadmap-execution-contracts.md): scoped effects and
checked work packages, detached typed completion/replay, private owner stop, and
shared service evidence with invocation authority. C1 implements the first three;
C6c implements shared service evidence before A1. These are general facilities,
not exemptions for Async names.

**Acceptance:** typed exchange can be represented without operation-local
polymorphism; distinct owners/types remain distinct; escaping pause, retained
short-lived replies, reentrant advancement, and falsely synchronous foreign
suspension are rejected. Malformed Core cannot bypass those checks. Explicitly
show rows and transport for the nested scheduler/pull case.

**Evidence:** [control/retention model](../internal/feasibility/control_test.go),
[typed completion/registry model](../internal/feasibility/tasks_test.go),
[scoped-effect/package model](../internal/feasibility/scoped_rows_test.go), and
[typed Machine exchange probe](../internal/feasibility/exchange_test.go).
The latter exercises the private Machine with distinct request,
reply and result types. Runtime foreign-transfer/evidence tests and Core
ownership tests cover reuse of that foundation. Models operate on explicit
capture sets and closed evidence records: they do **not** prove inference over
arbitrary source closures, module serialization, or both future backend ABIs.

**Stopping point:** selected implementable contracts and explicit compiler
prerequisites. Production counterparts are documented in the
[Coroutine design](design/coroutines.md), with real inference, malformed-Core,
codec, and differential fixtures.

### C1: General typed execution

**DONE.**

**Dependencies:** C0 and the early Async A0 contract gate.

Implemented behavior belongs in [Coroutine](reference/library-coroutines.md),
[Work](reference/library-work.md), and [Completion](reference/library-completion.md).
The [Coroutine design](design/coroutines.md) owns inference, independent
Core/Machine proofs, both backend paths, private stop, and serialization.
Stream uses that protocol through the [ordinary C2 wrappers](design/coroutines.md#ordinary-pull-libraries).

**Acceptance:** identical event traces for typed exchange, lazy start, terminal
reads, failure then Closed, nested owners, captured/per-advance evidence, and
cleanup. Staging and REPL exercise the same owner rules. Generated code keeps
ordinary direct handlers direct.

**Stopping point:** a usable scoped Coroutine API independent of Stream.

### C2: Ordinary Stream and Iterator

**DONE — ordinary wrappers on the generic Coroutine foundation.**

**Dependencies:** C1 and the executable [A0 representation check](roadmap-async.md#a0-library-representation-contract).

The [implemented architecture](design/coroutines.md#ordinary-pull-libraries) and
[reference](reference/library-streams.md) own the contract. Differential fixtures
cover zip, nested owners, borrowed values, early stop, failure, bounded demand,
ordinary Yield handlers, and an independently compiled pull library. Stream and
Iterator have no intrinsic identity checks or compatibility compiler path.

### C3: Cooperative scheduling demonstration

**Dependencies:** C2.

Write an ordinary Fango fixture/example with two lexically owned coroutines,
a FIFO ready queue, voluntary yield, and deterministic fake wait registrations.
Requests are an ordinary ADT. Return to the dispatch loop after a step rather
than recursively invoking the next continuation. Keep all queued handles inside
their owners. The demonstration is not the public Async API.

**Acceptance:** deterministic alternating trace, waiting work not requeued
until signalled, typed completion, early abandonment, and a task that suspends
inside `next`. Count retained owners/frames and show cleanup occurs once. No
native callback support or goroutine is needed.

**Stopping point:** shared control demonstrably serves Stream and scheduling.
This does not yet prove dynamic spawn, reusable task results, or parallelism.

### C4: Scope-owned dynamic allocation

**Dependencies:** C3 and A0 for implementation. Begin the scope/registry design
alongside C0, so A0 can assess dynamic spawn before the Stream migration.

Add a general live scope capable of owning multiple dynamically allocated
coroutines. Allocation registers cleanup before publishing a handle. Captures
of the new body must be valid for that scope; completed/closed work must release
execution storage without retaining every completed frame until scope exit.

**Design portion resolved at C0/A0; implementation remains open.** Selected
spelling and signature schemas (subject to C0's owner-sensitive control):

```fango
scope : (Scope e ->{Drive | e} a) ->{e} a
create
    : Scope e
    -> ((request ->{Suspension} reply) -> reply ->{Suspension | e} result)
    -> Coroutine request reply result e
```

`Scope e` is abstract and resource-bearing. `create` installs a lazy producer
and a cleanup entry atomically before publishing its handle; it runs neither
producer application. It has no outward effect, but its scoped-capability
argument makes it subject to the existing prohibition on treating resource
calls as pure for reordering/sharing. Allocation retains the producer, captures,
and definition-site evidence until completion/close; each must outlive the
**destination scope**, not merely the helper call. `e` bounds residual execution
and cleanup effects. Close/advance retain their Drive contracts. `with` remains
the single-owner convenience boundary.

Every allocation gets a distinct execution owner beneath the scope. Registry
entries erase request/reply/result types by closing over the **typed** handle
in a uniform `() ->{Drive | e} ()` close action; they never project a handle
back from an integer or `Native.Any`. Only the scope's cleanup driver invokes
these actions. The C0 owner obligation still names the captured handle, and
scope discharge accepts it only with proof of registry membership. A queue may
hold differently typed handles through this closure packaging without existential
source types. Typed task result cells are a separate C6a requirement.

For a nullary scheduling service, C4 additionally exposes a checked registration
facet that hides this scope's row parameter while retaining its owner/budget
contract. Registering a coroutine with a different residual row uses C0's
[scoped work package](roadmap-execution-contracts.md#scoped-effects-and-work-packages):
prove inclusion in the scope budget and retain typed execution/failure adapters.
The scope boundary includes those deferred obligations when inferring its
outward row. The simpler same-row cleanup closure above does not by itself prove
this hidden-row case. A package never acquires a different owner by entering
another queue, and neither the facet nor its packages may escape the live scope.

Completion/close unlinks the entry and clears producer frames, captures and
forwarding links immediately; a remaining handle has only terminal state and
its static lifetime. Remove registry links as well as frames: no append-only
list of historical cleanup closures. Scope exit closes unfinished owners in
reverse registration order; synchronous failures follow existing precedence.
Once closing starts, allocation is forbidden. A normal Async context body
finishing is **not** this transition: its library driver remains inside the
scope while children can create grandchildren, then exits after the live set
drains. C5 later permits the closing driver itself to suspend.

Explicit capability passing chooses the destination; captured context evidence
can supply it through ordinary library code. Neither `Async.context` nor a
native registry receives an exception to retention or sole execution authority.

**Acceptance:** a helper creates work owned by its caller's live scope; an
attempt to retain a helper-local resource fails. Queued work cannot escape the
scope. Repeated creation/completion does not accumulate dead execution storage.
A child may create further owned work while the context body is already done.

**Stopping point:** a safe dynamic owner facility; task failure/join policy is
still an Async library responsibility.

### C5: Suspending acquisition and cleanup

**Dependencies:** C1; use C3/C4 consumers to exercise nested ownership.

Extend existing Scope.bracket rather than adding a parallel API. Acquisition
may suspend; successful acquisition and registration of its release are one
ownership transfer. Before success, acquisition owns partial-failure cleanup.
Release may suspend while its owner remains in a closing state. Preserve LIFO
order, definition-site evidence, and typed primary/suppressed failures.

Abandonment becomes an executable drain capable of suspension. It cannot clear
an owner while a release is waiting. The driver supplies the environment needed
to complete that drain. Async supplies cancellation shielding and its wait
policy, specified in [its cancellation contract](roadmap-async.md#cancellation-and-cleanup).

**Acceptance:** pause during acquire, body, and release; fail nested releases;
abandon with an unfinished inner advance. Each acquired resource receives one
release attempt and outer cleanup runs after inner cleanup completes. A hanging
release is documented as preventing completion, not forcefully discarded.

**Stopping point:** general suspension-safe cleanup for all coroutine users.

### C6: Native retention and transfer

C6 is a family of independently accepted contracts, not one prerequisite that
every native consumer must implement in full. Consumers name C6a–C6d explicitly.
No public raw resume callback crosses any of these boundaries, and no bundled
executor name receives an implicit exemption. Native implementations remain
trusted to honor their declared contracts. The [native reference](reference/native.md)
remains authoritative until each extension lands.

#### C6a: Typed opaque values

**Dependencies:** the representation decisions from C0/A0; C4 when storage is
retained by a dynamic scope.

Implement opaque typed-value round trips and phantom wrappers with the
[STM boundary requirement](roadmap-stm.md#what-todays-rules-block). A0 decides
to require this facility before A1 for scope-owned write-once completion cells;
general `TVar a` needs it independently. Phantom-wrapper validation and opaque-value round trips have
separate acceptance obligations: scalar STM needs the former to expose
`TVar Int`, while arbitrary payload storage additionally needs the latter.
Representation-blind native storage must return a value at its original type
without inspecting evaluator or generated-Go representations.
Each selected task cell is created/read/written at exactly one payload type,
including its residual row index, with a checked scoped owner. No retrieval by
an untyped task ID, index-changing coercion, or public cast is allowed. A task
queue holds homogeneous Unit jobs that close over these typed cells; it does
not contain untyped payloads. Native storage never invokes a stored callable.

Storage preserves the value's capture and lifetime obligations. An opaque box
does not make a borrowed resource or stateful closure transferable, and it does
not authorize concurrent access. Validate declarations and preserve contracts
through inference, Core, wrappers, module interfaces, and both backend ABIs.

**Acceptance:** phantom wrappers preserve their type index and cannot be forged
or confused across native calls. Distinct Int/String entries and values containing
records or recursive ADTs round-trip at their original types; mismatched retrieval and
longer-lived retention of a borrowed value are rejected. Both backends agree.

**Stopping point:** checked typed native storage, independently of asynchronous
callbacks or concurrent execution.

#### C6b: Scoped native requests and retention

**Dependencies:** C4 for retained scoped work; C5 before exposing callbacks whose
completion requires suspending cleanup. Use C6a only if requests retain opaque
Fango values.

Define general contracts for immediate and retained Fango callbacks and native
requests. State whether invocation is once or repeatable, which execution owner
may invoke a callback, what scope bounds retention, how completion/exits are
represented, and what native quiescence means before that scope can close.
Registration must cover both immediate completion and later notification.
Cancellation revokes normal delivery but does not free resources still used by
native work. Validate and export the obligations as for C6a.

A background Go operation may publish readiness to a synchronized queue while
Fango callbacks execute only on their authorized driver. This subset does not
permit concurrent Fango callback invocation. Provide an explicitly scoped
request/host protocol: current FangoHost rules forbid retaining that global
object for background work. Synchronous release may cancel/join or drain a
request; suspension during release additionally requires C5.

**Acceptance:** immediate/delayed completion, completion during registration,
duplicate or stale notification, cancellation, partial acquisition failure,
bounded admission, and drain before resource release. Count live requests and
registrations. Language exits return as completion, never by unwinding a native
caller's unrelated execution.

**Stopping point:** the trusted boundary required by cooperative native IO,
without a claim that the Fango evaluator or callbacks are thread-safe.

#### C6c: Shared and transferable capabilities

**Dependencies:** C0/A0 capture contracts and C4 ownership; C6a only for opaque
polymorphic payloads.

Prove transfer of explicit captures and effect evidence. Reject parent-local
mutable handler state and borrowed advancement capabilities; permit child-owned
resources and explicitly supported shared native values. Distinguish moving the
sole authority over an execution from sharing a value whose native operations
provide their own synchronization and lifetime protocol.

The same capture rule applies to cooperative and concurrent executors. In
particular, an STM variable shared by cooperative children needs this contract
before its first release; a later executor cannot retroactively justify earlier
sharing. A shared handle's owner stays live until every authorized child and
native request has drained. The declaration must not grant arbitrary sharing
to every resource wrapper with the same representation.

A1 requires three specific capabilities: C4's synchronized allocation service,
C6a's write-once completion cell (one publisher, multiple readers), and C0's
[split service evidence](roadmap-execution-contracts.md). Implement and validate
that opt-in evidence/adapter contract here; its declaration spelling remains an
implementation review decision. The [service model](../internal/feasibility/tasks_test.go)
passes execution authority explicitly and tests captured outer context from a
different producer. This is a required general extension, not something ordinary
evidence does today.

**Acceptance:** safe child-local resources and declared shared values work;
explicit and hidden parent-state/cursor captures fail through closures, ADTs,
dictionaries, and evidence. A scalar shared-native-cell fixture exercises two
dynamically owned coroutines cooperatively without depending on Async or STM
implementation. STM later repeats the sharing proof through its transaction API.

**Stopping point:** checked child capture and shared-capability contracts.
Concurrent execution additionally requires C6d's runtime audit.

#### C6d: Concurrent invocation and runtime safety

**Dependencies:** C6b and C6c; C6a when values cross opaquely. C5 is required for
callbacks whose completion involves suspending cleanup.

Extend callback contracts to concurrent invocation, launch/join, publication,
and synchronized access. A callback's permitted invocation cardinality and its
execution owner remain explicit; concurrency never permits two workers to
advance one coroutine. Both backends must satisfy the contract despite their
different value layouts.

Audit the complete runtime before either concurrent executor is enabled.
Source-level immutability is insufficient when an implementation mutates shared
storage. In particular, [List's chunk frontier](../runtime/fangort/list.go)
currently uses a non-atomic claim justified by single-threaded evaluation.
Replace it with a proved thread-safe claim/publication path or a representation
that avoids shared mutation. Test two children extending the same list, not
just independently allocated lists. Preserve persistent values and their
documented complexity.

Also audit generated closure captures, effect evidence, state cells, evaluator
execution state, lazy globals, descriptor caches, failure snapshots, cleanup
stacks, native hosts, file/socket adapters, output coordination, and interpreter
reverse-host RPC correlation. A task may migrate workers without selecting
handlers, contexts, or input owners through worker-local ambient state.

**Acceptance:** targeted Go race detection covers runtime/native coordination
and emitted concurrent fixtures, including shared immutable values with mutable
internal representations. Callbacks preserve sole execution ownership, unsafe
captures fail statically, and completion/cancellation races cannot revive an
owner or release resources before native quiescence. Exercise interpreter and
generated execution, not only standalone Go runtime helpers.

**Stopping point:** a general trusted concurrent execution boundary, consumed
independently by goroutine-per-task and worker-pool executors.

### C7: General execution checkpoints

**Dependencies:** C1, with Async A2 as the structured cancellation consumer.
This is a later responsiveness milestone, not a prerequisite for explicit-yield
cooperative IO.

Introduce compiler-generated scheduling/cancellation checkpoints for long CPU
work. Cover generated loops, self-tail loops, general Machine dispatch, and
Direct/Exit callees reachable from a task. Wrapping an uninterruptible callback
in ImmediateMachine does not provide a checkpoint inside it. A poll that can
yield must preserve the live execution as explicit frames; merely observing a
flag cannot implement cooperative scheduling.

Before implementation, specify poll placement/frequency, cancellation routing,
transport and callable-family changes, and the execution capability that enables
polling. It must be general execution support rather than recognition of Async
names. Preserve the ordinary Direct/Exit fast paths outside scheduled execution,
definition-site evidence, module-owned ABIs, staging budgets, and existing
evaluator interruption checks. Explain how indirect and separately compiled
calls receive the execution context without ambient worker identity.

Cancellation must enter the same owned drain protocol as explicit checkpoints;
it cannot skip cleanup, interrupt a shielded release repeatedly, or surface as a
catchable ordinary failure. Opaque native work keeps its C6b cancellation/drain
contract; compiler polling does not make every host call interruptible.

**Acceptance:** CPU-only loops and Direct helpers reached through stored or
cross-module calls let another ready task progress and observe cancellation in
both backends. Nested resources release once, handler identities survive a
scheduling switch, and no task is resumed concurrently. Test progress using
controlled work/poll counts and synchronization rather than fragile elapsed-time
thresholds. Measure overhead and cancellation latency separately on an idle
machine, including synchronous code outside Async.

**Stopping point:** generated Fango CPU work reaches documented safe points.
Async's A8 integration owns executor behavior; no guarantee is made that arbitrary
native calls or nonterminating cleanup complete within a deadline.

## Acceptance and verification

Convert each row into focused positive/negative fixtures at its owning stage:

| Area | Required scenarios |
| --- | --- |
| Exchange | Different request/reply/result types; no pause; multiple pauses; strict argument evaluation; lazy producer factory |
| Ownership | Aliases; indirect calls; recursive helpers; distinct nested allocations; escape through ADTs, closures, dictionaries, outer stores |
| Reply retention | Capture-free replies; permitted enclosing resources; rejected shorter-lived resources; module-exported contracts |
| Outcomes | Finished once; stable Closed; close before start; repeated close; caught failure then Closed |
| Cleanup | Early stop; nested failure; definition-site handlers; one attempt; foreign suspension; later suspending release |
| Evidence | Different interpretations on successive advances; captured identity; inner same-effect handlers; foreign owner propagation |
| Stream | take zero; left-first zip; many-input/many-output stages; reopening; borrowed outputs; nested pull conflicts |
| Integration | Stored callbacks, ADTs, dictionaries, currying, equation groups, separate modules, staging limits, REPL rollback |
| Independent checks | Malformed owner, access, reply type, row, liveness, cleanup depth, and serialized contracts rejected |
| Storage | No unbounded dispatcher recursion; completed frames/links cleared; dynamic registry releases dead execution storage |
| Native contracts | Typed same-index storage; bounded retained requests; drain before release; transfer checks in cooperative execution |
| Concurrent runtime | Shared List extension; publication and host/evaluator audit; single execution owner; C6d race fixtures in both backends |
| CPU checkpoints | Generated loops and Direct callees; progress and cancellation; preserved cleanup/evidence; synchronous-path overhead |

Retain [repository gates](../AGENTS.md) and
[verification commands](design/verification.md#verification-commands): Core and
Machine lint, interpreter/compiler differential and functional suites, go vet,
formatting, and deterministic emission. Run grammar tokenization for migrated
fixtures; any later syntax addition must update exact lexer-matching grammar
rules and representative cases. No new syntax is required by C0–C3.

Do not run timing benchmarks during ordinary implementation or documentation
work. Build-check them with `go vet ./benchmarks` for implementation changes.
Measure frame reuse, allocation, evidence forwarding, compilation cost, and
steady-state advancement only in a separate idle-host performance stage.

For a documentation-only change, validate links/anchors, stage dependencies,
proposed labels, and API consistency. The roadmap does not claim the examples
are runnable before their stages land. Do not add completed plan files or an
implementation diary when promoting finished stages into design/reference.
