# Roadmap: direct effects, streams, and structured tasks

This document owns proposed APIs and the work needed to make them usable.
The implemented contract remains in [design](design/effects.md)
and [reference](reference/effects.md#effects-and-handlers); the main
[roadmap](roadmap.md#effects-state-and-resource-scopes) summarizes priorities.
The APIs below are **proposed**, with their delivery milestones identified.
Callback subsumption, trailing lambdas, and pipes are implemented foundations;
see the [reference](reference/effects.md).
Fango blocks use intended Fango syntax and omit routine imports; application
functions such as `fetch` and `consume` stand for domain code. Lifetime
contracts are explicitly schematic, not new source syntax.

The commitments are direct-style effects, one stream API for pure, effectful,
and suspending work, scoped borrowing, ordinary library combinators, and
structured tasks with reusable results. Deliver cooperative execution first,
then suspending cleanup, bounded concurrency and event adapters, and a bounded
parallel executor. Experimental APIs may be replaced without compatibility
wrappers. Promote durable results into design/reference when implemented and
remove completed roadmap work; Git history is the archive.

## API tour

### Effects and real IO

**Proposed example — milestone 4 for the suspending interpretation.** Ordinary declarations, direct calls,
tail-resumptive handlers, and `Fail` retain their existing meaning:

```fango
effect Catalog
    lookup : String -> String

loadName : String ->{Catalog} String
loadName key = lookup key

inTest action =
    handle action() of
        lookup key -> resume ("fixture:" ++ key)

fetch : String ->{IO, Async, Fail Error} String
fetch url = Network.get url

inProduction action =
    handle action() of
        lookup key ->
            value = fetch key
            resume value
```

`Network.get` is a proposed milestone 4 adapter. A real interpretation can
perform IO or call a suspending library function before its tail `resume`.
The operation's apparent result type does not determine its execution mode:
the supplied interpretation does. `Async` describes execution, `IO` external
interaction, and `Fail Error` typed failure; none erases the others.

### Async stream pipelines

The synchronous Stream API, scoped cursors, custom stages, and file pipelines
are implemented; see [Streams and cursors](reference/library-streams.md).
The next extension admits suspending callbacks through those same combinators.

**Proposed async network pipeline — milestone 4:**

```fango
Async.run \_ ->
    requests
        |> Stream.map fetch
        |> Stream.filter wanted
        |> Stream.forEach consume
```

This still fetches sequentially. The network adapter supplies readiness or a
bounded blocking-native bridge; neither `map` nor the call to `fetch` needs an
async-specific spelling. Residual IO and failure remain visible to the caller.

### Structured async

**Proposed example — milestone 4:**

```fango
Async.run \_ ->
    first = Async.spawn (\_ -> fetch firstUrl)
    second = Async.spawn (\_ -> fetch secondUrl)
    a = Async.await first
    b = Async.await second
    combine a b
```

Calling a suspending function is an ordinary call. `spawn` starts a child owned
by an implicit task context and takes only its action. `Async.run` selects the
cooperative executor and establishes a root context, so spawning needs no
additional scope block. `await` observes stored completion; repeated awaits do
not rerun work. Initial reusable results must be immutable and capture-free, and handles
remain scoped to their owning context. Result reuse does not permit sharing
mutable capabilities.

**Proposed nested context — milestone 4:**

```fango
Async.run \_ ->
    first = Async.spawn (\_ -> fetch firstUrl)

    b = Async.context \_ ->
        second = Async.spawn (\_ -> fetch secondUrl)
        Async.await second

    a = Async.await first
    combine a b
```

`Async.context` requires async execution and establishes a nested task lifetime
and cancellation boundary on the same executor. Its callback receives Unit.
Here `second` must finish before the inner context exits; `first` belongs to
the root and must finish before `run` exits. Awaiting an outer task inside an
inner context is allowed and does not change its owner.

Context ownership flows through scoped effect evidence. An ordinary helper
receives that evidence from its caller; a closure that captured a definition-site
context keeps that owner even when invoked inside a different context. Ownership
does not come from a mutable ambient current-context slot. Task handles and
closures retaining a context cannot outlive it, including through returned ADTs
or stores into outer handlers. Child actions inherit their owning context unless
they establish a nested one; exit waits for tasks spawned by children as well.
Spawning without an async runner remains an unhandled effect, and detached tasks
remain outside the proposal.

Successful context exit waits for all owned tasks. Failure or cancellation cancels
remaining children and drains their cleanup before exit. Unobserved child
failures still fail the context: catching a failure from `await` does not erase
a failed child's completion. Handle expected failures inside the child and
return a `Result`. Timeout and race cancel and drain losing work explicitly.

Parent-local mutable state and borrowed cursors cannot be captured by children
in the initial API, even on the cooperative executor; children may create their
own local state and resources.

**Proposed executor selection — milestone 7:**

```fango
Async.runOn (Executor.parallel 4) \_ ->
    task = Async.spawn (\_ -> fetch url)
    Async.await task
```

`Async.runOn` also establishes a root context. Parallel execution requires
checked transferable captures, including captured effect evidence. An effect
row alone is not a transfer proof. The executor uses
bounded workers and preserves the same context, resource, and cancellation
semantics as cooperative execution.

### Concurrent streams and external events

**Proposed example — milestone 6:**

```fango
requests
    |> Stream.mapConcurrent 8 fetch
    |> Stream.forEach consume
```

`mapConcurrent` preserves input order and bounds both active work and retained
results by its positive capacity. If an early request is slow, completed later
results occupy that bound and stop further admission. A separately named
`Stream.mapConcurrentUnordered` delivers in readiness order. Early downstream
termination cancels and drains active work before closing upstream traversal.

**Proposed subscription API shape — milestone 6:**

```fango
Events.withSubscription source 32 Events.DropOldest \events ->
    events
        |> Stream.take 10
        |> Stream.forEach consume
```

The explicit scope bounds subscription lifetime. Subscription starts on
traversal, not description construction, and ends on traversal cleanup, always
before subscription scope exit. Each reopening makes a new subscription.
Capacities must be positive; policies are `Events.Fail`, `Events.DropOldest`,
and `Events.DropNewest`. Overflow failure is typed and cancels/drains traversal.
An adapter may offer backpressure only if the source supports it. External
callbacks enter a bounded adapter queue, never a public resumption callback.
General multicast, replay, detached subscriptions, and public channels/select
are outside the committed scope.

## Language foundations and compiler boundary

The implemented effect, Stream, cursor, and representation contracts are in
the [design](design/core.md). The task APIs above remain
unimplemented and must preserve those boundaries.

- **Effect subsumption:** extend the implemented callback inclusion and nominal
  variance rules to task APIs; never erase a real effect.
- **Exclusive borrowing:** preserve sole advancement authority across task
  suspension, cancellation, and transfers between executors.
- **Task boundaries:** validate captures and separate child completion from an
  exit targeted at a parent handler. A child executor never unwinds a parent's
  stack directly; it reports completion for parent-side routing after drain.
- **Surface convenience:** trailing final lambdas retain `\_ ->` for Unit
  callbacks. `Basics` supplies `(|>)` (`infixl 0`) and `(<|)` (`infixr 0`),
  exported by `Prelude`, as ordinary functions. No `async` keyword or `do`
  block is required. No additional call syntax is proposed here.

| Compiler/runtime | Ordinary Fango library |
| --- | --- |
| Effect dispatch and exits | Domain effects and interpretations |
| Scope ownership, cleanup, and capture checking | Resource wrappers |
| Yield and owned cursor advancement | Stream stages and consumers |
| Task ownership, waiting, cancellation, executor integration | Concurrent combinators, race, timeout |
| Trusted native resource/readiness boundaries | File, network, and event-facing APIs |

Raw continuation and readiness callbacks stay private. The compiler proves
resume discipline, non-escape, and exclusive advancement before erasure; runtime
consumed flags or panics cannot substitute for these proofs. Runtime tags,
program counters, and registration generations coordinate legal execution.
Native implementations remain trusted without receiving a public raw resume.

Preserve Direct and Exit fast paths and select state machines only for actual
Machine computations. Preserve definition-site evidence, deep handler behavior,
per-curried-arrow effect timing, normal-only return transformations, deterministic
module-owned ABI families, and independent Core verification. Expanded source,
staging, batch builds, and the REPL enter the same applicable proof boundaries.
Machine lowering uses typed live locals and explicit loop transitions, never
host-stack copying, runtime internals, or a goroutine-based continuation engine.

No thread or channel is required per task or pipeline stage. Suspension can
still require frames, allocation, and dispatch; unbounded suspended recursion
requires storage. Library composition does not automatically fuse stages.
Fusion is a measured later optimization, and source non-escape does not guarantee
Go stack allocation.

## Delivery milestones

Each stopping point is usable without the remaining sequence. All inherit the
API tour's semantics and the verification gates below.

### 4. Cooperative structured async

- **API and dependencies:** build on the implemented stream/cursor foundation to deliver `Async.run` with an implicit root
  context, nested `Async.context`, action-only `spawn`, reusable `await`, and the
  network interpretation/pipeline in the tour.
- **Implementation and soundness:** sole advancement authority transfers from
  running frame to wait registration to ready queue and back, then to completion
  or cancellation/drain. Duplicate readiness and cancellation races cannot
  create a second owner. Store immutable capture-free task results, keep handles
  in their owning context, reject unsafe captures, and route child exits via
  completion. Preserve context identity through effect evidence and captured
  closures, and drain all owned tasks, including those spawned by children.
- **Native and cancellation protocol:** use deterministic readiness simulation
  first. Blocking-native bridges have bounded workers, explicit capacity and
  admission backpressure. Cancellation drains outstanding native requests before
  releasing resources they may access. Poll at safe points, including tight
  generated loops. Register release atomically with acquisition-success ownership
  transfer; acquisition owns partial-failure cleanup. Reject suspending acquire/
  release until 5. Shield synchronous release from repeated cancellation.
- **Generated code:** scheduler dispatch only for suspending work; no mandatory
  goroutine per child/wait and no reliance on Go netpoll internals.
- **Acceptance/stopping point:** two fetches overlap; repeated await executes once;
  root and nested contexts enforce ownership and drain before exit; unobserved
  failure cancels siblings and drains cleanup. Ctrl-C cancels active
  REPL work and native requests, restores input ownership, and preserves session
  state. Test queued-readiness and `readLine` interruption. This is a useful
  cooperative task system with synchronous cleanup.

### 5. Suspending cleanup

**Proposed example — milestone 5:**

```fango
Scope.bracket (\_ -> connect address) closeAsync \connection ->
    exchange connection
```

Here acquisition and release may both suspend; no separate async scope API is
introduced. These domain functions retain their IO and failure effects.

- **API and dependencies:** after 4, extend the same `Scope.bracket` API to
  suspending acquire/release; a connection's close may now wait for completion.
- **Implementation and soundness:** ownership registration remains inseparable
  from acquisition success. Keep the cancelling owner alive while release waits,
  shield release from repeated cancellation, and drain nested releases in reverse
  acquisition order before reporting completion. Preserve definition-site evidence
  and typed primary/inspectable secondary failures.
- **Generated code:** cleanup frames participate in the same Machine and readiness
  ownership protocol; never clear an owner while its release is waiting.
- **Acceptance/stopping point:** cancel during acquire, body, and release, with
  duplicate readiness and failing nested release; prove one release attempt per
  acquired resource. Suspending cleanup is committed, but completion cannot be
  promised if cleanup itself does not terminate.

### 6. Bounded concurrent streams and events

- **API and dependencies:** after 4–5, deliver ordered `mapConcurrent`,
  `mapConcurrentUnordered`, `Async.race`, `Async.timeout`, and scoped subscriptions.
  Race/timeout return only after cancelling and draining losers.
- **Implementation and soundness:** ordinary Fango combinators over structured
  tasks; bound in-flight work and retained results, including head-of-line stalls.
  Native event bridges own bounded registrations/queues with explicit overflow.
  Specify race winner/timeout completion and typed overflow reporting using the
  failure model below; nonpositive capacities receive a documented rejection.
- **Generated code:** reuse task and scope primitives; no compiler recognition of
  combinator names, unbounded result queues, or thread per pipeline stage.
- **Acceptance/stopping point:** test slow-first ordered mapping, readiness-order
  delivery, downstream early stop, event overflow under each policy, and
  event/close races. Subscriptions and losing tasks leave no live registrations.

### 7. Parallel executor

- **API and dependencies:** after 4–6, `Async.runOn (Executor.parallel 4)` selects
  bounded parallel execution explicitly and establishes a root context;
  `Async.run` remains cooperative.
- **Implementation and soundness:** check transfer of values and captured effect
  evidence across executor boundaries. Reject parent-local mutable state and
  borrowed cursors. Permit child-owned local state/resources. Enforce single-owner
  machine advancement while readiness, cancellation, and worker completion race.
- **Generated code:** bounded worker scheduling, no worker/thread per task;
  compatible module-owned Machine ABIs and the same cleanup protocol.
- **Acceptance/stopping point:** safe CPU tasks run concurrently; unsafe captures
  fail statically. Run equivalent context/resource/cancellation fixtures on both
  executors, without promising an identical concurrent effect interleaving.

### 8. Measured optimization

- **API and dependencies:** after the stream foundation and milestones 4–7, retain the same tour API and semantics.
- **Implementation and soundness:** measure frame reuse, synchronous-completion
  paths, callback overhead, and selective stage fusion. Transformations must
  preserve demand, effect order, cleanup, captures, and sole advancement authority.
  Coordinate worker-call work with [the calls roadmap](roadmap-calls.md).
- **Generated code:** reduce measured allocations/dispatch rather than claiming
  automatic fusion; keep Direct/Exit paths and deterministic module ABIs.
- **Acceptance/stopping point:** explain generated-code changes and allocation/
  latency results on an idle host; keep an optimization only with demonstrated
  benefit and all semantic gates passing. No optimization gates earlier API use.

### 9. Explicit capture and borrowing annotations

- **API and dependencies:** after the inferred contracts and access checks in
  the preceding milestones, add source syntax for written capture, retention,
  and borrowing contracts. Ordinary helpers continue to infer them.
- **Implementation and soundness:** check annotations against inferred bodies;
  preserve contracts across exported interfaces, callback adaptation, and
  staging. An annotation cannot weaken a proven lifetime or access obligation.
- **Acceptance/stopping point:** library authors can document and constrain a
  helper's contract, with diagnostics identifying mismatches and conflicting
  accesses. Settle the spelling and annotation placement during this milestone;
  the schematic notation in this roadmap is not a syntax commitment.

## Failure reporting prerequisite

Concurrent failure selection remains open. Define primary selection and deterministic
secondary ordering without assuming deterministic scheduler order. Distinguish
ordinary failure, early stop, and cancellation. Preserve the implemented
[cleanup precedence](reference/resources.md#cleanup-failures) and
[typed snapshots](reference/library-effects.md#fail-and-failure); do not flatten
failures into strings or defer cleanup while choosing concurrent policy.

## Deferred topics

These do not block the committed sequence unless a concrete API requires them:

- **General operation-local polymorphism:** parameterized effects are different
  from `fetch : Key a -> a` instantiated per operation. Choose a checked ABI for
  request packages or module-owned specialization, including local dictionaries,
  handler skolems, existential payload scope, answer types, and indirect calls.
  Go's generic-field/method restrictions cannot be bypassed with unchecked `any`.
- **Complete builtin IO interception:** wait until actual native declarations and
  class evidence fit that ABI; fixed-signature domain interpretations work now.
- **Named effect instances:** now proposed in
  [handler instances](roadmap-instances.md), which owns instance selection and
  the row-label rule that duplicate labels require.
- **Non-tail resumption and escaping computation owners:** neither is needed for
  scoped streams/tasks. Require a concrete consumer and a checked ownership
  contract before scheduling them. Raw escaping resume and multi-shot cloning
  remain outside the chosen model.
- **Shared mutable state:** requires an explicit sharing protocol; cooperative
  `get; await; put` is not atomic. Detached tasks/subscriptions, general multicast,
  replay, and public channels/select remain outside scope.
- **General fallible sidecars:** extend the current bundled File boundary only
  with resolved error identities or a declared marker and a chosen error
  vocabulary. Trusted adapters required above do not imply arbitrary foreign
  suspension or a universal FFI. Rich opaque native values remain in the
  [main roadmap](roadmap.md#opaque-native-types).

Handlers do not roll back arbitrary external writes. Search can use explicit
worklists and fresh computations without continuation cloning. Neither release
attempts nor ownership proofs guarantee successful external close or termination.

## Acceptance and verification

The API tour is the implementation acceptance-suite specification:

- Pure, IO, failing, and suspending callbacks compose through identical helpers,
  including named callbacks, stored functions, ADTs, dictionaries, and modules.
- Independent consumers and resource wrappers require no compiler registration.
- Early termination closes files exactly once through nested scopes and failing
  cleanup. Borrowed values cannot escape through closures, ADTs, outer effect
  handlers, or child tasks; reentrant/concurrent cursor advancement is rejected.
- Zip, bounded lookahead, and many-input/many-output stages work; repeated await
  observes one execution. Concurrent mapping meets ordering and storage bounds.
- Root contexts own direct spawns without an explicit context block. Nested
  contexts drain their own tasks before exit, and outer tasks can be awaited
  inside them without changing ownership. Context exit also waits for tasks
  spawned by children after the context body's result is available.
- Ordinary helper calls use caller-supplied context evidence; closures capturing
  an outer context keep that owner when invoked inside an inner context. Reject
  task handles and context-retaining closures escaping their owner directly,
  through ADTs, or through outer handlers. Spawning without an async runner
  reports an unhandled effect.
- Context failure, timeout, duplicate readiness, and cancellation races preserve
  sole advancement authority and drain tasks/resources. Count live registrations,
  frames, handles, and native requests; goroutine counts alone are insufficient.
- Parallel execution rejects unsafe captures. Generated synchronous pipelines
  contain no required scheduler, goroutine, or channel machinery.

For roadmap changes, check internal links, API spelling,
milestone dependencies, proposed/implemented labels, and single ownership of
syntax proposals. Proposed snippets are acceptance specifications, not fixtures
claimed to compile with today's compiler.

For implementation, retain the [repository gates](../AGENTS.md) and
[verification contracts](design/verification.md), including independent semantic
and Machine lint with malformed-IR tests. Exercise early curried effects, equation
groups, nested handlers, staging, step limits, and rollback. Syntax changes require
exact TextMate updates and representative tokenization; library API names alone
do not become keywords.

For performance work on an idle host, distinguish setup, hot operations, callback/
evidence adapters, allocation, live frame storage, cancellation/drain latency,
native capacity, compile latency, and output size. Explain results with allocation
profiles and escape reports; source lifetime proofs do not promise stack allocation.
