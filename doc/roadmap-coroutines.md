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
scheduler demonstration. C4 adds implemented dynamic ownership; C6a–C6c add
checked storage, requests, and sharing. C5, C6d, and C7 remain separately gated
capabilities needed by later consumers. Stage numbers are local
to this document; dependencies name stages rather than assuming one unbroken
global ordering.

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

## Performance prerequisite

**Satisfied for continuing coroutine/Async development.** The project accepts
the measured remaining costs against pre-Async revision
`0043640bcf46c6418c5ca4ee1fb84374eee5ce96`: zip takes about 3% longer, and repeated
short-lived traversals take about 11% longer with about 4% more allocated bytes.
The other seven primary cases meet the original full-parity requirements in
both measurement rounds. This is an explicit acceptance of these remaining
costs, not a full-parity result or a portable regression allowance.

Keep the strict [comparison](design/verification.md#performance-evidence) and
use the accepted implementation as an additional regression reference during
later stages. The [implemented optimizations](design/machines.md#dispatch-and-frame-lifetime)
recover steady-state execution through general frame reuse and primitive-call
forwarding; the independent Pull library benefits too. Library names must not
become compiler primitives. Each scheduling stage still needs its own semantic,
retention, cleanup, and performance evidence.

Remaining performance work is nonblocking for the next stages:

- Separate owner/setup costs from per-element work in zip and repeated short
  traversals; obtain usable profiles before attributing the remaining time.
- Reduce remaining frame allocation in list traversal and stateful handlers
  where a proof preserves state, cleanup, captured locals, and checkpoints.
- Measure boxing, closure, and evidence-row allocation after frame removal;
  constant frame counts do not mean allocation-free execution.
- Investigate the roughly 2% dispatcher overhead in the handwritten Go runtime
  control while retaining deferred execution and the existing step payload size.

## Proposed public interface

The scoped interface is implemented in [Coroutine](reference/library-coroutines.md).
[Work](reference/library-work.md) and [Completion](reference/library-completion.md)
provide the supporting package and outcome APIs, including
[dynamic allocation](reference/library-coroutines.md#dynamic-ownership).

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
non-escape for lexical and dynamic ownership. Dynamic allocation does not
permit detached scoped resources.

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
Native storage follows the implemented [C6a](#c6a-typed-opaque-values) boundary.

## Implementation stages

Every stage includes its applicable tests and updates implemented
reference/design descriptions only when behavior ships. C0 is a design/checker
checkpoint; C1–C3 together are the first usable replacement foundation.

| Stage | Required predecessors | Stopping point |
| --- | --- | --- |
| C0: Control and ownership contracts | Implemented Iterator foundation | DONE: feasibility contract and focused models |
| C1: General typed execution | C0, early Async A0 contract gate | DONE: executable scoped Coroutine, Work, and Completion APIs |
| C2: Ordinary Stream and Iterator | C1 | DONE: Stream behavior with no Stream-specific intrinsics |
| C3: Cooperative scheduling demonstration | C2 | DONE: shared foundation demonstrated without native concurrency |
| C4: Scope-owned dynamic allocation | C3, A0; scope design begins with C0 | DONE: live registry ownership and checked registration |
| C5: Suspending acquisition and cleanup | C1; nested fixtures from C3/C4 | Owners remain live through suspended cleanup |
| C6a: Typed opaque values | C0/A0 representation decisions; C4 for selected task cells | DONE: checked native storage and scope-owned write-once cells |
| C6b: Scoped native requests and retention | C4; C5 only for suspending cleanup | DONE: bounded requests and callbacks with checked quiescence |
| C6c: Shared and transferable capabilities | C0/A0 capture contracts; C4 ownership | DONE: checked shared values and implicit service invocation authority |
| C6d: Concurrent invocation and runtime safety | C6b, C6c; C6a when values cross opaquely | Checked concurrent callbacks and race-safe runtime representations |
| C7: General execution checkpoints | C1; Async A2 as integration consumer | Compiler-generated scheduling/cancellation points in CPU work |

C0, the design part of C4, and [Async A0](roadmap-async.md#a0-library-representation-contract)
form the joint feasibility gate. Focused source probes and test-only models
remain prerequisite checks for the implemented scoped API. The selected Async
task encoding is rechecked by the [executable C1 representation probe](../testdata/run/async_c1_representation.fango)
used by C2. C4/C6 are implementation prerequisites of A1, not circular
prerequisites of this design gate.

### C0: Control and ownership contracts

**DONE — feasibility contract and focused proof models.** Production scoped
execution belongs to C1, dynamic allocation to C4, and native transfer remains C6.

**Dependencies:** the implemented cursor/instance foundation.

Selected contracts are authoritative in [ownership](#ownership-and-lifetime-contracts),
[owner-sensitive discharge](#discharge-proof-gate), and [C4](#c4-scope-owned-dynamic-allocation).
No operation-local polymorphism, public continuation, or Async-specific intrinsic
is required. Plain nominal-row subtraction is insufficient.

Additional general prerequisites are specified in the
[execution contract topic](roadmap-execution-contracts.md): scoped effects and
checked work packages, detached typed completion/replay, private owner stop, and
shared service evidence with invocation authority. C1 implements the first three;
C6c implements shared service evidence for A1. These are general facilities,
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

**DONE — ordinary lexical scheduling over the shared Coroutine protocol.**

**Dependencies:** C2, including its executable A0 representation check.

The [implemented demonstration](design/coroutines.md#cooperative-dispatch-demonstration)
owns the queue, fake-wait, typed completion, nested `next`, and cleanup contract.
Its differential fixture and instrumented storage gate exercise both backends.
Dynamic spawn, reusable task results, and parallelism remain later work; this is
not the public Async API.

### C4: Scope-owned dynamic allocation

**DONE — dynamic coroutine scopes and checked hidden-row registration.**

**Dependencies:** C3 and A0; their scheduler and representation gates remain
prerequisites for the implementation.

[Dynamic ownership](reference/library-coroutines.md#dynamic-ownership) owns
`scope`, `create`, lifetime and cleanup behavior.
[Work registration](reference/library-work.md#dynamic-registration) owns the
checked registration facet and deferred budget contract. The
[registry design and verification](design/coroutines.md#dynamic-scope-registry)
cover both backends, distinct execution owners, immediate entry removal,
caller-owned helper allocation, rejected local captures and escaped queues,
and child allocation after the context-body helper has returned.

**Stopping point:** a safe dynamic owner facility. Task failure/join policy
and concurrent execution remain separate Async and C6 obligations;
C6a/C6c supply typed cells and shared service authority.

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

**DONE — checked phantom indices and same-type opaque storage.**

[Native storage](reference/native.md#indexed-native-storage) and
[scope-owned write-once cells](reference/library-cells.md) own the public
contracts. [Shared-capability design](design/shared-capabilities.md) owns token
representation, capture preservation, independent Core reconstruction, and
both backend ABIs. C0/A0 representation and C4 ownership prerequisites are
covered by the executable representation, ownership, and registry suites.

#### C6b: Scoped native requests and retention

**DONE — bounded scoped requests and driver-owned callback completion.**

[NativeRequest](reference/library-native-requests.md) owns registration,
cardinality, driver authority, cancellation, and synchronous quiescence.
[The design contract](design/native-requests.md) owns checked retention edges,
module/Core proofs, and the shared backend runtime. Acceptance covers immediate
and delayed completion, completion during registration, duplicate/stale signals,
partial acquisition failure, bounded admission, live counts, and drain before
resource release, including dynamically owned children.

C4 ownership is exercised by child abandonment fixtures. C6a remains the
boundary for opaque result storage. Callbacks and registration actions cannot
suspend; cleanup synchronously drains native work, so this contract does not
require C5. Suspending release remains C5 and concurrent Fango callbacks remain
C6d.

#### C6c: Shared and transferable capabilities

**DONE — nominal shared values and split service invocation authority.**

[Shared resources](reference/native.md#shared-native-resources),
[service contexts](reference/library-services.md), and
[the implementation invariants](design/shared-capabilities.md) own these
contracts. The acceptance suite exercises dynamically owned cooperative
children, child draining before shared-resource release, typed write-once
cells, retained service contexts used by different producers, nested pull
forwarding, rejected authority/capture transfers, and module/Core codecs in
both backends. C0/A0 and C4 remain prerequisite regression gates.

This subset has no retained native request or callback and therefore requires
no C6b extension. [Native requests](reference/library-native-requests.md) provide
the separate C6b contract; concurrent execution additionally requires C6d.

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
