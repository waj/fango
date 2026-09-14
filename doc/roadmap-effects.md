# Roadmap: direct effects, streams, and structured tasks

This document owns proposed APIs and the work needed to make them usable.
The implemented contract remains in [design](design.md#functions-and-effects)
and [reference](reference.md#effects-and-handlers); the main
[roadmap](roadmap.md#effects-state-and-resource-scopes) summarizes priorities.
All examples below are **proposed**, with their delivery milestones identified.
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

**Proposed example — milestone 1 for callback parity and call syntax; milestone
4 for the suspending interpretation.** Ordinary declarations, direct calls,
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

**Proposed example — milestone 2, with milestone 1 trailing syntax:**

```fango
File.withFile path \file ->
    process file

withConnection address use =
    Scope.bracket (\_ -> openConnection address) closeConnection use
```

The familiar file scope already exists with a parenthesized callback.
`withConnection` illustrates a user library wrapper: a generally declared
opaque resource and its wrapper receive the same capture checking as `File`,
without registering either name in the compiler. Acquisition/release in this
example are synchronous; their suspending forms arrive in milestone 5.

### Stream descriptions and pipelines

**Proposed example — milestone 3, using milestone 1 syntax:**

```fango
numbers = Stream.generate \_ ->
    Stream.yield 10
    Stream.yield 20

total =
    numbers
        |> Stream.map double
        |> Stream.fold (\value sum -> sum + value) 0
```

`Stream` is the public producer abstraction, absorbing the experimental
`Generator` construction API. `Yield` is its owned suspension effect, exposed
with the `Stream.yield` operation. Ordinary handlers cannot intercept it;
traversal supplies its owner.

**Proposed Fango signatures — milestone 3; suspending callbacks usable at
milestone 4.** These are source type signatures, not lifetime notation:

```fango
generate : (() ->{Yield a | e} ()) -> Stream a e
yield : a ->{Yield a} ()

map : (a ->{e} b) -> Stream a e -> Stream b e
filter : (a ->{e} Bool) -> Stream a e -> Stream a e
take : Int -> Stream a e -> Stream a e
fold : (a -> b ->{e} b) -> b -> Stream a e ->{e} b
forEach : (a ->{e} ()) -> Stream a e ->{e} ()
```

The shared `e` is the permitted combined row. Subsumption admits a callback
or covariant stream value with fewer effects; arguments need not have identical
rows. Construction and transformation are pure. Traversal performs the latent
producer and callback effects. Strict argument expressions still evaluate
normally before calling a constructor or combinator.

A stream describes production; a cursor is one active traversal. Reopening a
reusable description repeats its effects and need not reproduce identical
values. Captured resources and effect evidence restrict where that description
can be used. Reusability does not extend a captured capability's lifetime.

Ordinary stages are sequential and demand-driven. `take 0` never starts its
upstream producer, and ending traversal releases everything it acquired.
`take` produces another stream; materialization is a separate library operation,
such as `Stream.toList`, with storage proportional to the collected output.
Pure and suspending callbacks use the same `map`, `filter`, and `fold`.

**Proposed file pipeline — milestone 3:**

```fango
emitLines file =
    case File.readLine file of
        Nothing -> ()
        Just line ->
            Stream.yield line.text
            emitLines file

lines path = Stream.generate \_ ->
    File.withFile path \file ->
        emitLines file

printPrefix path =
    lines path
        |> Stream.filter nonempty
        |> Stream.take 20
        |> Stream.forEach print
```

Opening the description starts no file IO; traversal opens the file and closes
it once on exhaustion, failure, or early stop. File IO here is synchronous.
Machine lowering does not make a blocking native call nonblocking.

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

### Custom consumers and stages

**Proposed example — milestone 3:**

```fango
Stream.withCursor source \cursor ->
    first = Iterator.next cursor
    second = Iterator.next cursor
    combine first second
```

`Iterator.next` returns `Maybe a`, exclusively borrows its cursor for the whole
advancement, and carries both traversal-state and producer effects. Exhaustion
is stable: every later read returns `Nothing`. Cursors can pass through checked
helpers and be used sequentially. Escape, retention beyond the owner, reentrant
advancement, and concurrent advancement are statically rejected. An outstanding
advancement retains its exclusive borrow across suspension. Distinct cursors
can advance independently inside nested scopes.

**Proposed annotated consumer — milestone 3.** `Iterator a e` and `Traversal`
are proposed source types/effects; the hidden borrow identity is inferred.
`Traversal` represents cursor-state access, discharged by `withCursor`, and is
separate from the producer's residual row. Helper contracts also record which
cursor is borrowed; an effect label alone cannot establish exclusivity.

```fango
sumCursor : Iterator Int e ->{Traversal | e} Int
sumCursor cursor =
    case Iterator.next cursor of
        Nothing -> 0
        Just value -> value + sumCursor cursor

sumStream : Stream Int e ->{e} Int
sumStream source = Stream.withCursor source sumCursor
```

The helper retains no cursor and returns an immutable result. Implementations
may use a tail-recursive accumulator to avoid pending additions. There is no
terminal-name recognition: an independently authored consumer has the same
rights as `Stream.fold`.

**Proposed filtering producer and sequential zip — milestone 3:**

```fango
keepMatching predicate cursor =
    case Iterator.next cursor of
        Nothing -> ()
        Just value ->
            if predicate value then Stream.yield value else ()
            keepMatching predicate cursor

filterWith predicate source = Stream.generate \_ ->
    Stream.withCursor source \cursor ->
        keepMatching predicate cursor

emitPairs left right =
    case Iterator.next left of
        Nothing -> ()
        Just a ->
            case Iterator.next right of
                Nothing -> ()
                Just b ->
                    Stream.yield (a, b)
                    emitPairs left right

zip left right = Stream.generate \_ ->
    Stream.withCursor left \leftCursor ->
        Stream.withCursor right \rightCursor ->
            emitPairs leftCursor rightCursor
```

Sequential `zip` reads left first. If right ends, one unmatched left value may
already have been produced; it cannot be undone. Storage is bounded apart from
the producers' own state. Scope exit closes unfinished production before
returning or propagating failure, including when downstream stops between
yields. Lookahead consumers keep a bounded value buffer, and many-input or
many-output stages use these same facilities.

**Schematic lifetime contracts — milestone 2 foundations, milestone 3 cursors;
not Fango syntax:**

```text
withCursor(source, use): introduce fresh s
  cursor: Iterator<s, a, e>, owned by this traversal
  use: borrow cursor within s; result and outer stores must not retain s
next(&exclusive cursor<s>): {Traversal(s), e} Maybe a
  exclusive borrow lasts until completion, including suspension
helper(cursor<s>): export retention, result-capture, and access requirements
```

Every scope entry has a fresh identity, including recursive entries. Inferred
contracts propagate through helper calls, closures, ADTs, and effect evidence,
and module interfaces export them. An output element with captures retains its
own lifetime restrictions; wrapping it in `Maybe` does not erase them. Ordinary
examples infer identities and borrow boundaries. The source spelling for
explicit resource declarations and advanced capture contracts is a milestone 2
design task, not a claim that the schematic notation is accepted today.

### Structured async

**Proposed example — milestone 4:**

```fango
Async.run \_ ->
    Async.nursery \scope ->
        first = Async.spawn scope (\_ -> fetch firstUrl)
        second = Async.spawn scope (\_ -> fetch secondUrl)
        a = Async.await first
        b = Async.await second
        combine a b
```

Calling a suspending function is an ordinary call. `spawn` starts a child owned
by the nursery. `await` observes stored completion; repeated awaits do not rerun
work. Initial reusable results must be immutable and capture-free, and handles
remain nursery-scoped. Result reuse does not permit sharing mutable capabilities.

Successful nursery exit waits for all children. Failure or cancellation cancels
remaining children and drains their cleanup before exit. Unobserved child
failures still fail the nursery: catching a failure from `await` does not erase
a failed child's completion. Handle expected failures inside the child and
return a `Result`. Timeout and race cancel and drain losing work explicitly.

`Async.run` selects the cooperative executor. Parent-local mutable state and
borrowed cursors cannot be captured by children in the initial API, even on that
executor; children may create their own local state and resources.

**Proposed executor selection — milestone 7:**

```fango
Async.runOn (Executor.parallel 4) \_ ->
    Async.nursery \scope ->
        task = Async.spawn scope (\_ -> fetch url)
        Async.await task
```

Parallel execution requires checked transferable captures, including captured
effect evidence. An effect row alone is not a transfer proof. The executor uses
bounded workers and preserves the same nursery, resource, and cancellation
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

The implemented baseline includes Direct/Exit effects, synchronous `Scope`,
`Fail`, scoped `File`, and a limited experimental `Generator`/`Iterator` path.
It does not yet provide the general borrowing, Stream, or task APIs above.
Preserve its useful mechanisms while replacing terminal intrinsics and
canonical resource-name lists with general rules:

- **Effect subsumption:** widen fewer-effect callbacks and covariant stream
  values to a permitted combined row. Prove variance, annotation checking,
  generalization, and inference-order behavior; never erase a real effect.
- **Capture and borrowing contracts:** infer and export argument retention,
  result captures, exclusive access, and captured evidence through helpers,
  closures, ADTs, dictionaries, and module boundaries. General resource
  declarations grant opaque library resources scoped-capability treatment.
- **Escape prevention:** check values stored through outer handlers, not only
  scope return values. The existing outer-handler storage gap must close before
  promising sound general resource wrappers.
- **Compositional suspension:** Machine calls work through higher-order and
  stored callbacks, nested scopes, result constructors, and module boundaries.
  Unsupported suspension handlers receive source diagnostics, not Core errors.
- **Task boundaries:** validate captures and separate child completion from an
  exit targeted at a parent handler. A child executor never unwinds a parent's
  stack directly; it reports completion for parent-side routing after drain.
- **Surface convenience:** trailing final lambdas retain `\_ ->` for Unit
  callbacks. `Basics` supplies `(|>)` (`infixl 0`) and `(<|)` (`infixr 0`),
  exported by `Prelude`, as ordinary functions. No `async` keyword or `do`
  block is required. This document owns that syntax work for tests as well.

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

### 1. Compositional effects and call syntax

- **API and dependencies:** named pure/effectful callbacks work alike in
  ordinary helpers; deliver the tour's trailing lambdas and pipes on the
  existing Direct/Exit foundation. Suspended interpretations wait for 4.
- **Implementation and soundness:** inclusion-based callback checking and
  covariant row widening, sound variance and annotation checks, order-independent
  inference; parser sugar lowers to ordinary application/lambda Core. A final
  lambda extends rightward and ends at its layout boundary or list comma.
- **Generated code:** preserve effect timing, evidence and Direct/Exit adapters;
  no scheduler is introduced by call syntax or row widening.
- **Acceptance/stopping point:** mixed named and inline callbacks compose in
  `Scope` and test helpers, across modules and argument orderings. The test
  framework can adopt `test "name" \_ ->` without waiting for streams.

### 2. General scoped capabilities

- **API and dependencies:** after 1, declare library resources and implement
  `withConnection` over `Scope.bracket`; checked helpers can borrow resources.
- **Implementation and soundness:** replace canonical-name treatment with
  general resource declarations and inferred/exported capture/access contracts.
  Check outer-handler stores, closure/ADT escape, and indirect helper retention.
  Define diagnostics naming the owner, escaping value, and conflicting access.
- **Generated code:** erase static identities after independent Core checking;
  preserve direct synchronous scopes and exactly-once release authority.
- **Acceptance/stopping point:** an independent wrapper needs no compiler
  registration, valid scalar/capture-free results pass, and indirect escape
  fails before execution. Synchronous resources are useful on their own.

### 3. Streams and custom traversal

- **API and dependencies:** after 1–2, deliver `Stream.generate`, transformations,
  library consumers, `withCursor`, and `Iterator.next`. The tour's file/filter/
  take, annotated consumer, lookahead parsing, and sequential zip must work.
- **Implementation and soundness:** complete Machine representation families for
  higher-order/stored callbacks, nested cursor scopes, result constructors, and
  module calls. Replace recognized terminal consumers with ordinary Fango.
  Prove exclusive borrows across suspension and stable exhaustion; reject
  handling `Yield` without an owner with a source diagnostic.
- **Generated code:** selective typed producer frames and owned advancement;
  synchronous pipelines require no scheduler, goroutine, or channel machinery.
- **Acceptance/stopping point:** custom many-input/many-output stages use bounded
  storage; `take 0` starts nothing and early termination closes nested files
  once, including failures. Settle cleanup-failure observation below before
  promising stable early-stop failure reporting. Streams work without tasks.

### 4. Cooperative structured async

- **API and dependencies:** after 2–3, deliver `Async.run`, `nursery`, `spawn`,
  reusable `await`, and the network interpretation/pipeline in the tour.
- **Implementation and soundness:** sole advancement authority transfers from
  running frame to wait registration to ready queue and back, then to completion
  or cancellation/drain. Duplicate readiness and cancellation races cannot
  create a second owner. Store immutable capture-free task results, keep handles
  in their nursery, reject unsafe captures, and route child exits via completion.
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
  unobserved failure cancels siblings and drains cleanup. Ctrl-C cancels active
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
  bounded parallel execution explicitly; `Async.run` remains cooperative.
- **Implementation and soundness:** check transfer of values and captured effect
  evidence across executor boundaries. Reject parent-local mutable state and
  borrowed cursors. Permit child-owned local state/resources. Enforce single-owner
  machine advancement while readiness, cancellation, and worker completion race.
- **Generated code:** bounded worker scheduling, no worker/thread per task;
  compatible module-owned Machine ABIs and the same cleanup protocol.
- **Acceptance/stopping point:** safe CPU tasks run concurrently; unsafe captures
  fail statically. Run equivalent nursery/resource/cancellation fixtures on both
  executors, without promising an identical concurrent effect interleaving.

### 8. Measured optimization

- **API and dependencies:** after 3–7, retain the same tour API and semantics.
- **Implementation and soundness:** measure frame reuse, synchronous-completion
  paths, callback overhead, and selective stage fusion. Transformations must
  preserve demand, effect order, cleanup, captures, and sole advancement authority.
  Coordinate worker-call work with [the calls roadmap](roadmap-calls.md).
- **Generated code:** reduce measured allocations/dispatch rather than claiming
  automatic fusion; keep Direct/Exit paths and deterministic module ABIs.
- **Acceptance/stopping point:** explain generated-code changes and allocation/
  latency results on an idle host; keep an optimization only with demonstrated
  benefit and all semantic gates passing. No optimization gates earlier API use.

## Failure reporting prerequisite

The current exit representation records suppressed cleanup failures but exposes
no source observation API. Preserve this obligation: before stable reporting in
3–6, select a public observation contract in which primary failures remain typed
and secondary cleanup failures are inspectable, including heterogeneous errors.
A file close failure after a failed write is the concrete acceptance case.

Distinguish ordinary errors, early stop, and cancellation. Decide when failed
cleanup supersedes a non-error stop and how that is reported; do not silently
turn `take` completion into an ordinary error or discard the close failure.
A successful body followed by failed release reports release failure; a failed
body remains primary while nested release failures accumulate inner to outer.
For concurrent children, define primary-failure selection and deterministic
secondary ordering without assuming scheduler order is deterministic. These
observation/selection details remain open prerequisites, not grounds to defer
cleanup or silently flatten typed errors into strings.

## Deferred topics

These do not block the committed sequence unless a concrete API requires them:

- **General operation-local polymorphism:** parameterized effects are different
  from `fetch : Key a -> a` instantiated per operation. Choose a checked ABI for
  request packages or module-owned specialization, including local dictionaries,
  handler skolems, existential payload scope, answer types, and indirect calls.
  Go's generic-field/method restrictions cannot be bypassed with unchecked `any`.
- **Complete builtin IO interception:** wait until actual native declarations and
  class evidence fit that ABI; fixed-signature domain interpretations work now.
- **Named effect instances:** public instance selection and duplicate row labels
  need resolution rules beyond the already distinct activation identities.
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
- Nursery failure, timeout, duplicate readiness, and cancellation races preserve
  sole advancement authority and drain tasks/resources. Count live registrations,
  frames, handles, and native requests; goroutine counts alone are insufficient.
- Parallel execution rejects unsafe captures. Generated synchronous pipelines
  contain no required scheduler, goroutine, or channel machinery.

For this documentation replacement, check internal links, API spelling,
milestone dependencies, proposed/implemented labels, and single ownership of
syntax proposals. Proposed snippets are acceptance specifications, not fixtures
claimed to compile with today's compiler.

For subsequent implementation, preserve independent semantic and lowered Core
lint, with malformed-Core negative tests and source provenance. Keep compiler/
interpreter differential tests through the real CLI, functional examples, REPL
and diagnostic tests, intentional lexer/parser/checker/Core goldens, deterministic
and gofmt-idempotent generated modules, native invalidation, and `go vet`.
Exercise early curried effects, handler equation groups, nested handlers,
staging/splices/derivers, step limits, and checkpoint rollback.

Syntax changes update `editors/vscode/syntaxes/fango.tmLanguage.json` in the same
change; update language configuration when comments/brackets change. Verify
stdlib, testdata, and representative new syntax with `vscode-textmate`, keeping
regexes exact to lexer rules. No keyword is reserved solely for highlighting an
ordinary library API.

Build-check benchmarks with `go vet ./benchmarks` during ordinary development;
do not run timing gates. Keep `make test`, `make ci`, Core lint, differential and
functional suites, and existing benchmark thresholds intact. Run `make test-perf`
only for relevant performance work on an otherwise idle machine. Separate setup,
hot operation cost, callback/evidence adapters, application allocation, live
frame depth/storage, cancellation/drain latency, native worker capacity, compile
latency, and generated code size. Use allocation profiles and Go escape reports
to explain changes rather than treating a source lifetime proof as an allocation
promise.
