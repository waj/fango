# Roadmap: owned coroutines and the Stream foundation

This document owns the **proposed** general suspension capability, its reuse
of Iterator, and the migration of Stream onto an ordinary library surface.
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

Reuse the current machinery rather than implementing a second stack model:

| Existing component | General role | Required change |
| --- | --- | --- |
| Typed Machine frames and live-local analysis | Saved execution | Keep the representation and selective lowering |
| Yield owner carried through evidence | Exact suspension destination | Generalize away from Stream identity |
| Cursor scope and capture contracts | Owned lifetime | General coroutine type and producer capability |
| Exclusive cursor advancement | One active execution per owner | Cover advance and close, including aliases |
| Nested pull transfer stack | Calls between owned coroutines | Carry typed replies and completion values |
| Cursor evidence forwarding | Current advancement's residual handlers | Preserve captured handlers separately |
| Machine abandonment and cleanup | Disposing of unfinished execution | General close; later allow cleanup to suspend |
| Maybe packaging | Iterator's public result | Move to a Fango wrapper over a general Step |

See [machines](design/machines.md) and [capture contracts](design/ownership.md)
for implemented invariants. Source entrypoints include
[semantic Core](../internal/core/core.go), [Machine IR](../internal/machine/ir.go),
the [dispatcher](../runtime/fangort/machine.go),
[nested transfers](../runtime/fangort/traversal.go), and
[cursor owner](../runtime/fangort/iterator.go). These are starting points for
refactoring, not a second specification of their existing behavior.

## Proposed public interface

Introduce a bundled `Coroutine` module. The coroutine type is abstract and
resource-bearing. `Suspension` and `Drive` are abstract nullary control labels;
there are no public operations with which to implement arbitrary handlers for
those labels. `Step` is an ordinary public ADT.

A coroutine here is an owned resumable computation with typed requests, replies,
and a final result. Pull iteration specializes that protocol to Unit replies and
Unit completion; the public abstraction is not restricted to yielding stream
elements. A scheduler is another driver of the same protocol, not another kind
of saved continuation.

```fango
-- Proposed declarations; the abstract Coroutine representation is omitted.
type Step request result
    = Suspended request
    | Finished result
    | Closed

with
    : ((request ->{Suspension} reply)
        -> reply ->{Suspension | e} result)
    -> (Coroutine request reply result e ->{Drive | e} a)
    ->{e} a

advance
    : Coroutine request reply result e
    -> reply
    ->{Drive | e} Step request result

close
    : Coroutine request reply result e
    ->{Drive | e} ()
```

`with`, `advance`, and `close` are the initial compiler-supported operations.
The scoped suspension callback is constructed by `with`; there is no public
raw continuation or globally callable polymorphic `suspend` operation. General
resource cleanup remains compiler-supported. This boundary is a deliberate
small control API, not a promise of zero compiler intrinsics.

`request`, `reply`, and `result` are ordinary type parameters. `e` is the
residual effect row. A particular coroutine fixes its exchange types;
two coroutines may use different types without introducing two differently
parameterized labels into one effect row. Effect names record control, while
capability identity records which owner the control belongs to.

These are signature schemas with the [C0 control contract](#discharge-proof-gate),
not sufficient plain nominal-row signatures on today's compiler. Inference must
also export owner-indexed control obligations; `e` includes surviving foreign
control, even when its printed label is also `Suspension` or `Drive`. No new
source instance-name syntax or compiler exception for an Async name is selected.

### Typed exchange example

```fango
-- Proposed example; Coroutine is imported qualified.
Coroutine.with
    (\pause initial ->
        answer = pause ("hello " ++ initial)
        answer ++ "!")
    (\work ->
        first = Coroutine.advance work "Ada"
        second = Coroutine.advance work "received"
        (first, second))
```

The results are `Suspended "hello Ada"` and `Finished "received!"`.
The first String starts the body; the second answers its suspended request.
There is no separate start/resume typestate protocol. Further advancement
returns `Closed` and never restarts the producer.

For Stream, reply and result are both Unit. For a dialogue, a request can be a
question ADT and the reply a validated answer ADT. For a scheduler, a request
can distinguish voluntary yield from waiting on a registration. A wakeup-only
scheduler may choose Unit replies and retrieve typed results separately.

### Meaning of the producer callback

The first arrow supplies `pause` and is pure; the final arrow runs the body.
The boundary must not execute either producer application before the first
advancement. Ordinary argument evaluation remains strict: constructing and
passing an action is not a promise to defer effects already evaluated while
building its arguments.

`pause request` sends a request to this coroutine's driver and, when that
driver advances it again, returns the supplied reply. The same `pause` callback
may be called at multiple sequential suspension sites. It is not a resume
callback: invoking it does not replay a previously suspended continuation.
The current continuation remains owned internally by the coroutine.

No general non-tail resume is needed for an ordinary handler to use it:

```fango
-- Proposed handler inside a producer supplied with pause.
handle emitValues() of
    emit value -> resume (pause value)
```

The operation clause waits inside `pause` and then performs its one tail
resume. The [general resume roadmap](roadmap-effects.md#resume-discipline)
owns broader handler composition.

## Lifecycle and outcomes

The following states describe private execution, not new source constructors.
Every transition preserves a single authority to advance or close the owner.

| State and operation | Action | Outcome/state |
| --- | --- | --- |
| Open with a driver | Register owner cleanup, invoke driver | Unstarted producer |
| Advance unstarted with input | Supply pause and initial input; run producer | Running |
| Advance suspended with reply | Define suspended call's result; continue | Running |
| Running reaches its own pause | Retain execution and cleanup; return request | Suspended; `Suspended request` |
| Running returns normally | Complete cleanup before publishing value | Terminal; `Finished result` once |
| Running exits to an outer handler | Unwind producer regions and propagate exit | Terminal before outer handling |
| Advance terminal | Evaluate ordinary call arguments, execute no producer code | `Closed` |
| Close unstarted | Discard producer without invoking it | Terminal |
| Close suspended | Abandon saved execution and drain its cleanup | Terminal; Unit or cleanup failure |
| Close terminal | No new release attempt | Unit |
| Owner scope exits | Close unfinished production before leaving | Driver answer or combined exit |

An explicit close that fails still leaves the owner closed; automatic scope
cleanup must not retry releases. If driver execution already failed, its
failure stays primary and close failures are suppressed using the existing
[cleanup precedence](reference/resources.md#cleanup-failures). Successful
production whose cleanup fails emits no `Finished` value. Failure caught
around one advance leaves later advances observing `Closed`.

The first release-capable implementation uses synchronous acquisition/release.
C5 extends the same lifecycle with a private closing state that may suspend;
there is no new `AsyncScope` API. Neither version promises completion of a
nonterminating body or release.

## Ownership and lifetime contracts

### Handles and exclusive execution

A handle may be aliased inside its owning scope. Advancing one alias advances
the same current coroutine; it does not create a copy of execution.
Sequential use is legal:

```text
alias = work
advance(work, firstInput)
advance(alias, secondInput)
```

The following must be rejected when the producer can reach the second advance:

```text
advance(work, input)
    producer calls a helper that advances alias-of-work
```

Close conflicts with active advancement for the same reason. A runtime `busy`
flag is a defensive invariant check, not the source-language proof. Generalize
existing exclusive-access contracts through aliases, ADTs, named helpers,
stored callbacks, indirect calls, and recursive summaries. Diagnostics should
name the owner and the conflicting accesses; exact wording is selected with
the implementation fixtures.

If an advance suspends to a different owner, that advance is still unfinished.
Its exclusive access remains held until it returns, exits, or is abandoned by
its enclosing owner. A scheduler must not interpret a foreign suspension as
permission to advance every nested handle independently.

### Producer execution capability

`pause` retains a producer execution capability as well as its destination.
It may be invoked by the producer or by synchronous descendants, including a
nested coroutine reached through that producer's active advancement. It may
not be called independently by the driver or another task.

Reject exposing `pause` in a request, a completion value, a returned closure,
an ADT, or a longer-lived store. A bare resource marker on the outer coroutine
handle is not enough: the driver can hold that handle, but cannot acquire the
producer's authority to suspend. Keep those capabilities distinct in the flow
proof even if their runtime representation shares an owner token.

### Values crossing the boundary

Requests and final results must not retain producer-local resources whose
lifetime could end on resumption or completion. Existing Stream rules for
yielded resources are the baseline:

```text
producer opens a local file
producer pauses with that file       → reject
producer pauses with bytes read     → accept
```

Resources from a scope enclosing the coroutine may be carried while their
existing owner remains live. Closing the coroutine does not give a consumer
permission to keep a resource beyond its original scope.

Inputs and replies may remain in locals across future suspensions. Therefore
an input's captured capabilities must outlive the receiving coroutine, not
merely this call to advance:

```text
open coroutine in outer scope
open temporary file in inner scope
advance outer coroutine with temporary file → reject if retained there
```

An immutable String reply has no such resource capture. Capture obligations
must remain visible through generic types and module boundaries; erasing to a
runtime register cannot erase the source lifetime constraint. The selected
conservative contract treats **every** input/reply as retained until the receiving
owner closes, including initial input and terminal advances. Captures must
outlive that owner even if a body ignores the value. C1 has no non-retaining
exception. Wrappers export this obligation and check it after substitution.

### Scope escape

Returning a coroutine handle, hiding it in a result, or storing it in an
outer handler is rejected. Returning unrelated values remains legal. Closing
a handle does not retroactively erase its static scope identity or turn a
scoped value into an unrestricted one.

C4 changes where an owner may allocate a coroutine. It does not remove these
rules or permit detachment from a live scope.

## Effect routing and nested suspension

### Captured and per-advance evidence

Preserve both existing paths:

- A handler captured when a closure was constructed keeps that activation.
- A residual effect not captured there receives the current advance's
  interpretation. Two pulls under different handlers can therefore interpret
  the same uncaptured operation differently.

Do not bind every residual operation at coroutine creation, and do not use a
worker-global current-handler slot. Restore forwarding references between
completed advances and before abandonment; retain them across an unfinished
foreign suspension. Cleanup uses definition-site evidence. Runtime tags and
registration generations coordinate execution, not source lifetime proofs.

### Foreign suspension

The important composition test is a stream producer calling an operation that
suspends to an enclosing scheduler. The sequence is:

1. A task calls `next` on its stream cursor.
2. The producer calls `fetch`; its interpretation requests a network wait.
3. The scheduler receives that wait, with the task and unfinished pull saved.
4. Readiness resumes the task inside `fetch`.
5. The producer yields an element to its own cursor owner.
6. `next` finally returns that element to the task.

Only step 5 completes the cursor advance. Steps 3–4 must preserve the cursor's
exclusive access and every intervening resource scope. Early abandonment of
the outer task drains nested production before its callers.

The same test can use fake readiness and an ordinary `Wait` effect; network
support is not a prerequisite for proving the composition.

### Discharge proof gate

A nullary marker cannot by itself identify an owner. A boundary must discharge
only its locally owned suspension/advancement and preserve foreign control in
its inferred residual contract. In particular, putting an outer `pause` in an
inner producer must not make an externally suspending call appear synchronous
because both callbacks mention `Suspension`.

The selected extension adds finite owner-indexed obligations to arrow/capture
contracts: `suspend(owner)` and `drive(owner)`, with symbolic owner parameters
substituted at calls and fresh identities at allocation. These are proof
notation, not source syntax. The existing nominal row and `Control.Transport`
alone cannot express subtraction of one owner. Join obligations across aliases,
branches and recursive summaries; an unknown owner remains outward control.
Reconstruct them from executable Core, serialize them across modules, and check
them independently after transformations. A claimed Direct annotation cannot
erase an obligation. Allocation-site folding must never prove uncertain owners
equal; conservative rejection is preferable to discharging a foreign owner.

An advance consumes only `suspend` addressed to its producer, retaining its
exclusive access through foreign suspension. A lexical boundary consumes only
the drives it owns; a dynamic scope consumes drives of its registered owners.
Project the remaining control set to nominal labels **after** owner subtraction.
Control-label projection is idempotent, so a foreign `Suspension` may survive a
boundary that also introduces local `Suspension`; it must not be excluded by a
nominal row-tail lacks constraint. Ordinary non-control row rules stay intact.
This is a general inference/contract extension required by C1, not implemented
by the feasibility model.

| Expression in the scheduler/pull example | Remaining control (proof notation) | Printed row, omitting unrelated effects | Transport |
| --- | --- | --- | --- |
| Inner producer pauses locally and through captured outer callback | suspend(pull), suspend(task) | Suspension | Machine |
| Advance inner cursor | drive(pull), suspend(task) | Drive, Suspension | Machine |
| Inner cursor boundary | suspend(task) | Suspension | Machine |
| Advance outer task | drive(task) | Drive | Machine inside its driver |
| Outer owner boundary with synchronous driver | none | empty | Direct; Exit if other residual effects can abort |

Different request/reply/result types belong to owner protocols, never to the
nullary labels. A nested boundary around a foreign handle preserves its Drive
obligation. The outer pause is legal only on the synchronous descendant path of
its active producer; calling it from the driver or publishing it in any value
is rejected. Reentrant advance or close is rejected even through an alias.
The [control model](../internal/feasibility/control_test.go) checks these cases,
including missing owner, mismatched protocol and falsely synchronous summaries.
Existing [Core ownership tests](../internal/core/iterator_ownership_test.go) prove
reconstruction for today's Iterator; C1 must extend that real linter to the new
operations rather than trusting the test-only model.

## Stream and Iterator migration

Source compatibility is not a delivery constraint for this compiler. Keep
Iterator as the deliberately chosen pull-consumer abstraction: its ordinary
`next` wrapper hides Unit replies and Step packaging from Stream consumers.
Do not keep old compiler paths or introduce aliases solely to preserve prior
contracts. Migrate bundled sources, annotations, diagnostics, and reference
examples together when the new boundary lands.

Keep `Stream a e` as an ordinary reusable description of a producer. Make
`Stream.Yield` an ordinary user-handleable effect. Open the generic coroutine
and install its interpretation inside the producer:

```fango
-- Proposed wrapper skeleton; makeIterator packages next's ordinary closure.
withProducer producer consumer =
    Coroutine.with
        (\pause _ ->
            handle producer() of
                Stream.yield value -> resume (pause value))
        (\work -> consumer (makeIterator work))
```

Iterator's private wrapper uses a coroutine with Unit reply and result.
`next` advances with Unit and maps `Suspended value` to `Just value`; both
`Finished ()` and `Closed` become `Nothing`. Failure propagates before result
packaging. The compiler need not know Maybe's constructors for this wrapper.

The consumer surface becomes:

```fango
-- Proposed signatures; import Coroutine.Drive explicitly.
withCursor
    : Stream a e
    -> (Iterator a e ->{Drive | e} result)
    ->{e} result

next : Iterator a e ->{Drive | e} Maybe a
```

Remove `Iterator.Traversal` and migrate annotations deliberately. Imported
effects cannot currently be re-exported, and no effect alias feature is added
merely to preserve that spelling. No compatibility intrinsic remains for it.

Keep `generate`, `yield`, `fromList`, `map`, `filter`, `take`, `fold`, `forEach`,
`toList`, and `zip` with their current meaning. Preserve first-pull execution,
reopening effects, `take n` doing no upstream production for nonpositive `n`,
left-first zip with its possible unmatched left element, bounded stage
buffering, and stable exhaustion. Yielded capture checks remain in force even
though the library handler now translates the operation into a generic pause.

After parity tests pass, remove special recognition of `Stream.withProducer`,
`Stream.Yield`, `Stream.yield`, `Iterator.Iterator`, `Iterator.next`, and
`Iterator.Traversal`. Compiler-owned general types/operations take their place;
renaming the Stream module must not affect proof or lowering behavior.

## Compiler and backend work

| Subsystem | Required responsibility |
| --- | --- |
| Resolution and inference | Validate general intrinsic shapes, preserve reply/result types, infer owner-sensitive residual control |
| Capture analysis | Separate driver and producer capabilities; track retained replies and exclusive advance/close |
| Semantic Core | Express general owner creation, advance, close, and typed suspension without Stream names |
| Core lint | Reconstruct owner, capture, row, access, and intrinsic contracts independently |
| Machine lowering/lint | Generalize cursor transitions, reply bindings, result packaging, liveness, and cleanup joins |
| Evaluator | Preserve typed protocol and evidence using its own value representation |
| Go backend/runtime | Emit typed frames/callback families around the private dispatcher and checked result projections |
| Modules and codecs | Serialize new nodes/contracts, invalidate incompatible cached artifacts, retain deterministic ABI families |
| Staging and REPL | Use the same proofs and execution boundaries; preserve budgets, forbidden-native rules, and rollback |

Factories remain lazy. Non-tail calls push explicit frames; tail transfers
replace them. Saved locals come from verified liveness, not a blanket capture
of all state. Clear finished frames and forwarding links. There is no host
stack copying, goroutine per suspension, or recursive dispatcher handoff.

A runtime erased register is acceptable only where source/Core/Machine proofs
establish its concrete type on both edges. It is not permission to expose
unchecked casts or polymorphic native storage. C6a owns that separate boundary.

Keep ordinary synchronous functions and handlers on Direct/Exit paths. The
coroutine owner, not every enclosing function, determines where latent
Machine transport is driven. Cross-module callable families remain chosen by
the defining module rather than specialized for every consumer.

## Implementation stages

Every stage includes its applicable tests and updates implemented
reference/design descriptions only when behavior ships. C0 is a design/checker
checkpoint; C1–C3 together are the first usable replacement foundation.

| Stage | Required predecessors | Stopping point |
| --- | --- | --- |
| C0: Control and ownership contracts | Implemented Iterator foundation | DONE: feasibility contract and focused models; review before C1 |
| C1: General typed execution | C0, early Async A0 contract gate | Executable scoped Coroutine API |
| C2: Ordinary Stream and Iterator | C1 | Stream behavior with no Stream-specific intrinsics |
| C3: Cooperative scheduling demonstration | C2 | Shared foundation demonstrated without native concurrency |
| C4: Scope-owned dynamic allocation | C3, A0; scope design begins with C0 | Coroutines safely retained by a live dynamic owner |
| C5: Suspending acquisition and cleanup | C1; nested fixtures from C3/C4 | Owners remain live through suspended cleanup |
| C6a: Typed opaque values | C0/A0 representation decisions; C4 for selected task cells | Checked native storage and same-type return; required before A1 |
| C6b: Scoped native requests and retention | C4; C5 only for suspending cleanup | Bounded requests and callbacks with checked quiescence |
| C6c: Shared and transferable capabilities | C0/A0 capture contracts; C4 ownership | Safe child captures, including explicitly shared native values |
| C6d: Concurrent invocation and runtime safety | C6b, C6c; C6a when values cross opaquely | Checked concurrent callbacks and race-safe runtime representations |
| C7: General execution checkpoints | C1; Async A2 as integration consumer | Compiler-generated scheduling/cancellation points in CPU work |

C0, the design part of C4, and [Async A0](roadmap-async.md#a0-library-representation-contract)
form the joint feasibility gate. The contract is selected with focused source
probes and test-only models; no Coroutine or Async API is implemented. **Stop
for review before C1.** C1 must validate the actual inference/Core/backend
extensions, and the selected task encoding must be rechecked before removing
the old Stream route in C2. C4/C6 implementations are later prerequisites of A1,
not circular prerequisites of this design gate.

### C0: Control and ownership contracts

**DONE — feasibility contract and focused proof models.** Production checking
and execution of the proposed contracts remain C1/C4/C6 work. This status does
not advertise the proposed APIs as implemented language behavior.

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
The latter exercises the existing private Machine with distinct request,
reply and result types; today's cursor transfer still replies Unit and drops
the final value. Existing runtime foreign-transfer/evidence tests and Core
ownership tests cover reuse of that foundation. Models operate on explicit
capture sets and closed evidence records: they do **not** prove inference over
arbitrary source closures, module serialization, or both future backend ABIs.

**Stopping point:** selected implementable contracts and explicit compiler
prerequisites, ready for review; C1 has not begun. The source spelling is not yet
fully checkable. Revalidate the model's positive/negative cases as real inference,
malformed-Core and differential fixtures during implementation.

### C1: General typed execution

**Dependencies:** C0 and the early Async A0 contract gate.

Implement the general prerequisites selected in C0 (owner-sensitive control,
scoped effects/work packages, typed completion/replay and private owner stop),
then general Core/Machine owner operations, typed replies/results,
terminal states, synchronous close, forwarding, and both backend paths. Add
module serialization and stale-artifact handling with the new representation.
Keep the old Stream path temporarily so failures can be isolated.

**Acceptance:** identical event traces for typed exchange, lazy start, terminal
reads, failure then Closed, nested owners, captured/per-advance evidence, and
cleanup. Staging and REPL exercise the same owner rules. Generated code keeps
ordinary direct handlers direct.

**Stopping point:** a usable scoped Coroutine API independent of Stream.

### C2: Ordinary Stream and Iterator

**Dependencies:** C1; validate A0's representation against the executable API.

Implement wrappers, migrate Drive annotations, allow ordinary handlers for
Stream.Yield, and remove Stream-specific compiler recognition and IR. Update
stdlib, examples, test fixtures, reference signatures, and diagnostics together.

**Acceptance:** existing stream behavior passes through the new route, including
zip, nested owners, borrowed values, early stop, failure, and bounded lookahead.
A user module implements another pull abstraction with no compiler registration.
All old intrinsic identity checks are gone; no compatibility-only compiler path
remains.

**Stopping point:** existing Stream functionality on the general foundation.

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
