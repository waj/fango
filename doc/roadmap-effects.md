# Roadmap: effects, state, and resource scopes

This document owns the proposed effects work. It is an implementation roadmap,
not a description of features already available. The current contract remains
in [design.md](design.md#functions-and-effects) and
[reference.md](reference.md#effects-and-handlers). The main
[roadmap](roadmap.md#effects-state-and-resource-scopes) links here.

Ship the milestones independently. State, failure, and synchronous resource
scopes must be useful without implementing generators, a scheduler, or general
continuations. When a milestone is implemented, promote its durable semantics
and invariants into design/reference and remove the completed work here. Do not
turn this file into an implementation diary.

Fango snippets illustrate intended programs. Unless explicitly described as an
existing reproducer, they may use proposed APIs. Control/ownership annotations
are marked as provisional. Go and Core snippets
are representation sketches, not exact generated identifiers or code to paste
into the implementation. Examples omit routine module imports where appropriate.

## 1. Two non-negotiable requirements

### P: performance

The original requirement is no goroutine-per-effect and no full continuation
capture by stack copying. Handler invocation must lower to ordinary function
calls or a loop-based state machine. Allocation-light, one-shot, preferably
tail-resumptive execution takes priority over multi-shot expressiveness.

Consequences for this roadmap:

- Do not add a goroutine/channel general-handler engine as a fallback. Moving
  the goroutine boundary from each operation to each handled body still does
  not provide the requested call/state-machine execution model.
- Ordinary resumptive handlers need no continuation object. A proven tail
  `resume value` becomes an operation callback's return of `value`.
- Aborting effects propagate tagged exits without preserving abandoned work.
- Actual suspension uses compiler-generated frames, live locals, and a loop.
  Never copy or inspect Go stack frames, use `unsafe` stack tricks, or rely on
  Go runtime internals. Ordinary Go stack growth is not continuation capture.
- Go does not supply general tail-call optimization. Explicitly lower any
  continuation transitions that require bounded stack to iteration.
- No promise of universal zero allocation: captured evidence can escape to
  Go's heap, collections allocate application data, and unbounded suspended
  recursion needs storage. Separate handler setup, per-operation overhead,
  live-frame storage, and application allocations in measurements.

### S: compile-time soundness

The original requirement is that every imposed resume restriction—exactly
once, tail position, non-escape, lifetime—be enforced by the compiler. A
runtime panic, consumed flag, liveness check, or error returned for an illegal
resume is not an implementation of this requirement.

Consequences:

- Reject unsupported programs before either backend executes them.
- Keep `resume` a compiler control construct, not an ordinary copyable function.
- Validate source programs and independently validate lowered control flow.
- Keep scope/capture checking ahead of hidden mutable state or scoped
  resources. Resume checking alone cannot prove that a resource does not escape.
- Introduce ownership checking before exposing iterators or task handles that
  own suspended computation. Garbage collection is not deterministic cleanup.
- Runtime outcome tags, program counters, scheduler readiness, and I/O errors
  are ordinary execution state. They are allowed; dynamically detecting a
  source-level continuation misuse is not.
- The guarantee applies to accepted Fango and the compiler-owned ABI. Native
  code is trusted, but must not be given a public continuation API that turns
  unchecked callback behavior into the language's resume discipline.
- Exactly-once obligations concern terminating paths and explicit exceptional
  exits. They do not prove termination of arbitrary code or successful release
  of an external resource when its close operation fails.

## 2. Architecture decisions

| Decision | Reason and requirement |
| --- | --- |
| Preserve direct evidence passing | Fits the current typed Go ABI; avoids search and continuation allocation (P). |
| Default clauses end in exactly one tail resume | Makes continuation materialization unnecessary and admits a local structural proof (P, S). |
| Check captures and scope identities before erasure | Prevent hidden state/resource access from escaping behind closures or generic values (S). |
| Distinguish abort-only effects from resumptive effects | Gives exception ordering without executing a handler before its inner scopes unwind (P, S). |
| Track control transport through evidence and arrows | A callback or handler clause can abort/suspend even if its visible operation returns normally (P, S). |
| Keep `return` as normal-result transformation | Cleanup must also run on abandonment and failures in result transformations. |
| Expose cleanup through ordinary `Scope.bracket` calls | New cleanup semantics need not introduce `try`, `catch`, `finally`, `using`, or `defer` syntax. |
| Lower cleanup to an explicit Core scope | A handler for one known effect cannot protect against all exits in an open row (S). |
| Use selective defunctionalized machines for suspension | Portable Go loops and typed storage; no host-stack capture (P). |
| Keep raw continuations non-escaping | Limits lifetime and alias analysis; owned computation objects are a separate extension (S). |
| Exclude multi-shot continuation resumption | Avoid frame cloning and replay semantics; explicit search remains possible (P, S). |

Evidence passing and continuation representation solve different problems.
Evidence selects an operation's implementation; it does not by itself make
non-tail or suspending handlers direct calls. These choices are informed by
[generalized evidence passing](https://xnning.github.io/papers/multip-tr.pdf),
but the calling conventions below are proposals specific to Fango and Go.

### Semantic invariants across all milestones

1. Saturated operations execute once; partial application remains pure and
   captures the correct evidence without executing the operation.
2. Effects and control transport belong to individual curried arrows. Do not
   move effects from an early application to the final worker call.
3. Operation clauses run with their definition-site outer evidence. An inner
   clause forwarding an operation must not accidentally call itself.
4. The handled body uses the installed evidence. Subsequent resumed work sees
   that evidence again; its handler remains deep for the default model.
5. `return` transforms normal body completion exactly once. An abort clause's
   answer bypasses that transformation. Cleanup is a different scope boundary.
6. Scope identities denote handler activations, not just nominal effect labels
   or state value types. Recursive activations of the same handler are distinct.
7. Direct, aborting, and machine code agree on operation selection, evaluation
   order, return transformation, and cleanup ordering.
8. Generated packages remain deterministic per source module. A downstream
   consumer cannot silently change a dependency's worker ABI.

## 3. Current implementation and integration map

The existing fast path is worth retaining. Source checking and Core lint prove
the implemented tail-resume discipline with compiler-only clause identities.

| Area | Existing implementation / work to extend |
| --- | --- |
| Function types and rows | `internal/types/`, `internal/infer/infer.go`, `solve.go`, `unify.go`, `annotation.go` |
| Resume checking | owner-aware `tailResume` in `internal/infer/infer.go` |
| Staging | `internal/infer/stage.go`, `internal/staging/staging.go`; expanded source must get the same checks |
| Handler elaboration | `internal/elaborate/elaborate.go`; equation groups become decision trees |
| Calls and callback adaptation | `internal/elaborate/spine.go`; open callback rows are currently erased with captured evidence |
| ANF, lifting, specialization | `internal/elaborate/anf.go`, `lift.go`, `specialize.go` |
| Core contract | `internal/core/core.go`, `lint.go`, `rewrite.go`, `dump.go`, `tailcall.go` |
| Go emission | `internal/codegen/gen.go`: `handleExpr`, `resumeStmtsFor`, operation and function type emission |
| Existing loops | `internal/codegen/tailloop.go`, `internal/eval/tailloop.go`; shared eligibility predicate |
| Interpreter | `internal/eval/eval.go`; explicit evidence environment, no general continuation execution |
| Runtime distribution | `internal/runtimefiles/`, build materialization, `runtime/nativeworker/`, `runtime/nativewire/` |
| Concrete State consumers | Bundled `State`, `Writer`, and handler-local seeded `Random` are implemented; extend their checked Core contracts |
| Concrete resource consumers | Bundled `File` over `Scope.bracket`; `types.ResourceType`/`ResourceRunner` name further resource types and runners |
| Host effects / FFI | `stdlib/IO.fango`, `IO.native.go`, `File.native.go`, `internal/natives/`, native declaration validation, boundary wrappers and fallible results, worker protocol |
| Verification | `cmd/fango/e2e_test.go`, `testdata/run`, Core/checker goldens, REPL tests, `benchmarks/` |
| Editor surface | `editors/vscode/syntaxes/fango.tmLanguage.json`, `language-configuration.json` |

The eventual pipeline should make its proof boundaries visible:

```text
resolved/expanded source
    -> types, effects, resume ownership, captures, and control constraints
    -> typed semantic Core (Handle, scoped state, Bracket, explicit terminals)
    -> semantic Core lint
    -> ABI-family selection and Direct/Exit lowering
    -> selective machine lowering only for Machine computations
    -> lowered control/ownership lint
    -> Go emission or equivalent Core interpreter execution
```

These need not be separate packages initially. They must be separate checked
contracts: syntactic resume legality cannot be reconstructed reliably after
generic expression lowering has erased the construct. Staging and the REPL
must enter the same applicable boundaries rather than bypassing them through
prefix elaboration or an interpreter-only convenience path.

## 4. Milestone graph and stopping points

| Milestone | Depends on | Shippable result |
| --- | --- | --- |
| E7: selective execution machines | Implemented control ABI and cleanup scopes | Internal one-shot suspension and cleanup frames |
| E8: owned iterators and scoped non-tail handlers | Scoped capture Core, E7 | Pull traversal and checked non-tail resumption |
| E9: structured async and cancellation | Implemented file resources, E8 | Cooperative tasks, cancellation, nursery cleanup |

The shipped control-aware ABI preserves the tail-resume and scoped-state direct
fast path; synchronous cleanup scopes, the bundled `Fail` effect, typed IO
errors, and the scoped `File` resource API are implemented on top of it (see
the design and reference). No general continuation object exists.
The private E7 machine backend is implemented. E8a is underway behind a private
runtime boundary; source exposure remains disabled until its declarations and
lowering are complete. E8b and E9 remain deferred. No milestone requires
implementing the whole table at once.

## E8. Owned iterators and scoped non-tail resumption

### E8a: a scoped iterator consumer

Start with a structured iterator scope that owns the suspended producer. The
consumer runs within that scope; exiting it cancels unfinished production and
runs its cleanup. This limits the first lifetime problem and provides a real
use case for E7 without raw escaping continuations.

The private pull-owner layer is implemented in both runtime and interpreter:
it advances an E7 machine one yield at a time and deterministically abandons
unfinished production on close. Generated-machine fixtures exercise early
close across a pending cleanup scope. Inference and typed Core recognize the
future runner and terminal-consumer identities and reject a non-lexical
consumer, cursor alias/escape, or more than one terminal consumption; inference
reports the exact source occurrence and Core repeats the proof after
elaboration. Remaining E8a work is source exposure: introduce the
`Generator`/`Iterator` declarations and lower `yield` and the runner into the
private machine owner.

Provisional library use, with `Generator.yield` a new suspension-capable
operation, not an ordinary tail-only Yield declaration:

```fango
walk tree =
    case tree of
        Empty -> ()
        Node left value right ->
            walk left
            Generator.yield value
            walk right

main() =
    Generator.withIterator (\_ -> walk sampleTree) (\cursor ->
        Iterator.forEach print cursor)
```

`Empty`, `Node`, and `sampleTree` belong to the example. Start with scoped
consumer combinators such as fold, find, and take; define early termination to
close the producer scope, not merely drop a pointer and wait for GC. A later
public step API can be conceptually:

```text
next : Iterator<a> --consuming--> Done | Yielded(a, Iterator<a>)
```

Each step consumes its input cursor and transfers its capability to the next
one. A lexical owner may provide automatic cancellation at scope exit, but
must not allow two independently usable aliases to the same cursor. Returning
an owning iterator beyond its construction scope is a further sub-increment:
move all owned frames, captures, and cleanup obligations into the result, and
reject borrowed captures whose scope would end. Ordinary Fango function values
are not silently made linear as a substitute.

### Example generated traversal

This simplified machine illustrates frame storage rather than general module
dispatch. The Go iterator is private and used through the checked source API.

```go
type WalkFrame struct { Node *Node; PC uint8 }
type WalkIterator struct { Frames []WalkFrame }

func (it *WalkIterator) next() (int64, bool) {
    for len(it.Frames) != 0 {
        top := len(it.Frames) - 1
        frame := &it.Frames[top]
        switch frame.PC {
        case 0:
            if frame.Node == nil {
                it.Frames = it.Frames[:top]
                continue
            }
            left := frame.Node.Left
            frame.PC = 1
            it.Frames = append(it.Frames, WalkFrame{Node: left})
        case 1:
            frame.PC = 2
            return frame.Node.Value, true
        case 2:
            frame.Node = frame.Node.Right
            frame.PC = 0 // right recursion is a tail transition
        }
    }
    return 0, false
}
```

Storage is proportional to live traversal depth. There is no Go stack capture
and no compulsory allocation at each yield. Actual code must clear popped
references, incorporate evidence/cleanup, and handle exits as specified in E7.
Ordinary completion checks are allowed; duplicate advancement must be rejected
by source ownership analysis, not detected by a runtime consumed flag.

### E8b: checked non-tail resume within an operation clause

Only expose this after the internal machine and ownership checker exist. Use
an explicit declaration discipline rather than changing every existing
tail-only operation. `control` below is provisional syntax:

```fango
effect Choice
    control choose : () -> Bool

main() =
    print
        (handle (if choose() then 10 else 20) of
            choose () ->
                answer = resume True
                answer + 1)
```

This evaluates to `11`. The handled result returns to the clause's pending
addition frame. `answer` has the handler answer type, not the operation's Bool
result type. Tail implementations of such a control operation can still be
optimized when their effective transport permits it.

The initial non-tail discipline is scoped and exactly once on normal paths:

```text
resume token state: Available -> Consumed
resume v: requires Available, transitions before executing resumed work
normal clause exit: requires Consumed
explicit exceptional exit: abandons Available token and unwinds its owned work
```

Branches merge only compatible obligations. If one branch resumes and another
does not, a normal join is illegal. An aborting branch terminates instead of
contributing an Available token to the join. The result of resume is an
ordinary value; the token remains non-first-class. Reject passing it to a
helper, wrapping it in a lambda, storing it, returning it, and resuming again
after catching a failure raised by its first invocation. Ownership transfers
before execution of resumed work, not only after successful return.

Do not add an unrestricted public continuation type or a library `Discard`
method. Explicit abandonment of a scoped continuation can be added later as
a checked terminal instruction if a use case needs normal completion without
resumption; it must unwind pending resources. Until then reject it. The
compiler may abandon work on a statically identified exceptional exit.

### Acceptance and examples

- Traverse a deep tree in-order and terminate after a prefix; verify cleanup
  without consuming the entire producer.
- Yield lines from a scoped file, stop early, and observe exactly one close.
- Show illegal reuse after `next`, escaped borrowed captures, duplicate resume,
  resume after a caught resumed-body failure, and missing branch consumption
  as compiler diagnostics.
- Test nested State with suspended production: suspension preserves the cell;
  disposal destroys its scope after cleanup. A returned immutable state snapshot
  does not retain a mutable cell.
- Implement a continuation-result annotation example using scoped non-tail
  resume and compare with the reference semantics. Tail-only declarations
  retain the current errors for the same non-tail source.
- Keep state-machine-owned cleanup alive during suspension and discharge it
  deterministically at early termination. No source correctness depends on a
  GC finalizer or a check inside the private Go `next` method.

## E9. Structured async, cancellation, and REPL integration

### Deliverable and boundaries

Introduce cooperative tasks only after E8 can own and terminate suspended
computation. Start with a single event-loop scheduler and a structured nursery;
no detached tasks, shared mutable state across threads, or per-effect/per-task
goroutines are needed. Bounded native worker pools for genuinely blocking
foreign calls are an explicit infrastructure choice, not a continuation
implementation. Preserve P in both the compiler and interpreter.

Provisional ordinary-function API:

```fango
main() =
    Async.run (\_ ->
        Async.nursery (\scope ->
            first = Async.spawn scope (\_ -> fetch "first.txt")
            second = Async.spawn scope (\_ -> fetch "second.txt")
            a = Async.await first
            b = Async.await second
            print (a ++ b)))
```

`fetch` and the Async module are new example APIs. The nursery owns children;
scope exit joins completed children or cancels and drains unfinished ones.
Task result handles must have a stated contract: begin with consuming await,
not a copyable handle whose join-once property is checked at runtime. Sharing a
completed immutable result is a separate operation and needs no continuation.

### Event and ownership protocol

```text
Running(frame owner)
    -> Waiting(registration owns frame)
    -> Ready(queue owns frame)
    -> Running(frame owner)
    -> Completed(result) or Cancelling(cleanup frames) -> Completed(exit)
```

Every transition transfers sole advancement authority. Model the protocol in
compiler-owned runtime code and lint machine entry points; do not expose
arbitrary callbacks containing a copyable `resume`. External readiness can be
duplicated or race cancellation, so registration removal/generation bookkeeping
is still needed. That is event coordination, not dynamic enforcement of an
unchecked source continuation use. Tests must demonstrate that such races never
create a second owner of a frame.

Machine fragment:

```text
RequestRead:
    frame.pc := AfterRead
    return WaitReadable(fd, owned frame)

AfterRead(event):
    if event failed: begin exit propagation
    else continue with event data
```

A machine does not make a blocking Go call nonblocking. Select initial IO
adapters whose readiness protocol is implementable without launching a
goroutine per wait. If a portable adapter needs a bounded pool, document its
capacity/backpressure and account for its costs separately. Avoid depending on
Go netpoll or scheduler internals.

### Cancellation and resources

- Cancellation is cooperative at defined safe points. A tight compiler-generated
  loop must still poll; preserve interpreter tail-loop cancellation behavior.
- Cancellation starts unwinding; it does not discard a frame buffer before
  running its cleanup scopes. Nursery completion waits for that work.
- There is no safe point between acquire-success ownership transfer and cleanup
  registration. Partial acquisition remains the acquisition routine's duty.
- Mask repeated cancellation delivery during each synchronous release attempt,
  then continue the pending unwind. A release may report an error, which joins
  the cleanup completion envelope without skipping other releases.
- Reject suspension inside cleanup in the first release. Async finalization is
  a separate extension requiring ownership of a cancelling task while cleanup
  itself waits; do not smuggle it through an ordinary effect-polymorphic callback.
- Define child-failure policy: cancel remaining siblings, wait for their cleanup,
  and report the primary child failure with deterministic cleanup/secondary
  failures. Successful siblings do not silently overwrite a failure.
- State remains scoped. Shared mutable state between tasks requires a separate
  sharing policy; even one event loop does not make `get; await; put` atomic.
  Non-suspending state transitions can be atomic relative to that scheduler.

### Consumers and acceptance

Build a two-input concurrent reader, bounded producer/consumer example, and
timeout that cancels a resource-owning child. Delay channels/select until those
examples need them; their send/receive readiness and cancellation semantics must
preserve the same ownership protocol.

Wire REPL Ctrl-C to cancel the currently owned computation, drain cleanup and
native worker requests, and restore prompt input ownership. Preserve the
session's checker/environment after a cancelled execution. Test cancellation
while reading input, inside nested scopes, while a child fails, and after a
ready event has been queued. REPL loading/redefinition remains in the main
roadmap, but cancellation lifetime semantics belong here.

Use deterministic simulated events before timing-sensitive integration tests.
Stress early cancellation, duplicate readiness, event/close races, and repeated
failure recovery. Track live tasks, registrations, file handles, frame buffers,
and worker requests after each scenario. Leak tests should validate ownership
release, not assume that counting goroutines proves continuation safety.

## 5. Cross-cutting verification and delivery gates

Every implementation milestone must update implemented design/reference in the
same change and remove its completed roadmap material. This documentation-only
roadmap does not alter implemented syntax or diagnostics.

### Compiler and semantic checks

- Preserve Core lint before execution/emission. Add independent malformed-Core
  tests for each new invariant, rather than testing only that elaboration sets
  a flag. Preserve source provenance through synthetic scopes and machines.
- Keep lexer/parser/checker/Core/REPL goldens. Change expected output only for
  an intentional documented semantic change, never merely to silence a failure.
- Cover operation/return equation groups, nontrivial patterns, partial and
  higher-order calls, ADT-held functions, dictionaries, annotation checking,
  nested handlers, cross-module interfaces, and early curried effects.
- Run interpreter/compiler differential fixtures through the real CLI. Include
  negative fixtures so shared wrong behavior cannot appear as successful parity.
- Test splices, derivers, interpreter step limits, and checkpoint rollback when
  new metadata/control nodes are introduced. Do not make hidden external state
  stage-safe by erasing a row.
- Retain functional examples, deterministic/gofmt-idempotent generated Go,
  per-module stability, native-source invalidation, `go vet`, and existing
  benchmarks. The repository's `make test`, `make test-short`, `make vet`, and
  `make ci` retain their present roles.

### Editor and documentation surface

Any change to effect declaration modifiers, state-handler syntax, or resume
syntax must update the TextMate grammar in the same implementation change.
Update language configuration if comment/bracket behavior changes. Tokenize
stdlib, relevant testdata, and the new examples with `vscode-textmate`; adding
regexes without exercising them is insufficient. APIs introduced with ordinary
function syntax do not require reserving new words just for highlighting.

### Performance measurements

Add focused benchmarks alongside the existing gates. Run timing gates on a
controlled/idle host using `make test-perf`; do not move noisy time assertions
into ordinary correctness CI. Preserve existing ceilings unless deliberate
measurement supports a reviewed change.

| Case | Separate measurements / expectation |
| --- | --- |
| Reader and direct operations | Setup vs hot calls; no continuation/channel allocation per operation |
| State counter | `get`/`put` throughput, cell/evidence setup allocations, known vs indirect handler |
| Nested/translated effects | Cost vs handler depth; no repeated dynamic label search on the fast path |
| Higher-order traversal | Callback adapters, dictionary/evidence overhead, Direct/Exit/Machine families |
| Failure | Normal-path branches, payload allocation on failure, unwind depth |
| Bracket | Setup, nested cleanup, normal/failure path, no continuation object |
| Writer / parser | Application data allocations reported separately from effect overhead |
| Generator | Maximum live frames, buffer growth, allocations per yield at fixed depth |
| Async | Step dispatch, registrations, cancellation/drain latency, bounded native workers |
| Compiler | Cold/warm compile latency and generated package/code-size growth |

Use Go escape-analysis reports to explain unexpected cell/closure allocations,
and allocation benchmarks to validate improvements. Source scope proofs alone
do not force Go to allocate on the stack. See the
[Go allocation FAQ](https://go.dev/doc/faq#stack_or_heap). Do not use benchmark
numbers from another language's native backend as a Fango performance promise.

## 6. Supported use cases and deliberate exclusions

| Use case | Earliest support / limitation |
| --- | --- |
| Reader/configuration, direct effect translation | Existing proved direct path |
| State, Writer, per-run deterministic Random | Implemented, with scoped capture checking |
| Local memoization | State is implemented; cache pure computations or explicitly define skipped-effect semantics |
| Failure, early return, parser alternatives | Implemented; fresh attempts, no continuation cloning |
| Scoped files | Implemented: `File.withFile` and siblings over `Scope.bracket`, with `File.Handle` a compiler-known capability |
| Locks, temporary resources, atomic replace | Same mechanism; each needs its own bundled runner and resource type when an example asks |
| State rollback | Implemented with private immutable state; not automatic external rollback |
| Push generators | Direct tail handlers; no inverted control required |
| Pull generators and early consumer exit | E7/E8; owned frames and deterministic disposal |
| Non-tail one-shot continuation results | E8b; explicit scoped discipline, no raw escape |
| Cooperative async and structured tasks | E9; scheduler/IO adapters required |
| Multi-shot nondeterministic handler | Excluded by the selected one-shot model |
| Escaping raw resume closures | Excluded; owning iterators/tasks are separate checked abstractions |
| Arbitrary suspension through foreign Go frames | Excluded without a declared converted adapter |
| Unbounded suspended depth with bounded memory | Impossible in general; live pending work requires storage |
| Rollback of arbitrary files/network writes | Not implied by handlers; requires transactional APIs/compensation |

The hard requirements do not logically prohibit every multi-shot system:
explicit immutable continuations could support cloning without Go stack copying.
This roadmap deliberately excludes that cost and complexity. Search can still
use explicit trees/worklists, replayable pure computations, and fresh parser
attempts. Do not describe all nondeterministic algorithms as impossible.

## 7. Decisions to settle at the relevant milestone

These are bounded open decisions for their named milestones.

- **Observing a suppressed cleanup failure:** a release that fails while the
  body is already exiting is recorded in the exit, inner to outer, and no
  source API reads it. The concrete consumer now exists — `File.withOutput`'s
  close can fail after a body that already failed — and two questions remain
  open together: what a program may observe, and whether an exit can be
  classified as an ordinary non-error control transfer, which is the only way
  a failed cleanup could be made to supersede one. Choose the public
  observation API before promising a library contract.
- **A resource leaving through an outer handler's operation:** the capture
  analysis restricts what a scope *returns* and what a scoped runner's
  *result* may carry, but not what a callback stores through an operation of
  a handler installed outside the scope. A user-declared parameterized
  handler whose operation takes a `File.Handle` and stores it in its cell,
  installed around `attempt (\_ -> File.withFile path (\file -> stash file))`
  and returning the cell from `return`, is accepted today, and the same shape
  is accepted for any capture-capable resource, such as a record holding a
  closure. Closing it needs the analysis to track a callback parameter's flow
  into evidence, so a runner's call site can see that the resource it
  supplies reaches a scope that outlives it. The handle it leaks is an
  opaque id whose every operation fails as "closed handle", so the gap is a
  soundness gap in the sense of requirement S, not a memory-safety one.
- **Cleanup checks a synchronous scope cannot express:** early manual disposal
  of a borrowed handle and duplicated release authority cannot be stated
  because `File` exposes no close operation, and rejecting suspending cleanup
  needs E7 `Machine` transport to exist. Revisit if a resource API needs
  explicit early disposal.
- **Fallible natives beyond `File`:** the `(T, error)` boundary shape is
  admitted only to the bundled `File` module, because the compiler must
  recognize `Result IO.Error T` by spelling before name resolution. Opening
  it to user sidecars needs either resolution before module validation or a
  declared marker, and a decision on whether `IO.Error` is the only error
  vocabulary a sidecar may raise.
- **General operation-local polymorphism and builtin IO handling:** preserve
  this unfinished work from the old roadmap. Parameterized effects are not the
  same feature: an operation such as `fetch : Key a -> a` is universally
  instantiated at each call. Go has no generic function-valued struct fields
  or methods with fresh method type parameters. Before supporting it, choose
  checked request packages/defunctionalized operation codes, bounded module-owned
  specialization, or another explicit ABI. Specify dictionary passing for
  operation-local constraints, handler skolem checking, existential payload
  scope, answer types, and indirect calls. Add generic fetch/store and
  polymorphic-output fixtures. A typed source obligation must not become an
  unchecked `any` result assertion. Full builtin IO interception can follow
  when its actual declarations, default natives, and class evidence fit that
  ABI; fixed-signature Console effects remain useful without it.
- **Multiple named instances:** distinct activation identities are already
  required internally. Public named State instances, resource-addressed
  operations, and duplicate labels in rows need separate resolution/row rules.
  Do not introduce them incidentally through existing nested handlers.
- **E8 ownership surface:** start with scoped combinators; freeze any consuming
  iterator/task API before permitting escape of owning computation objects.
  Scoped non-tail resume and moving an owned machine are different features.
- **E9 advanced scheduling:** detached tasks, channels/select, parallel shared
  state, and async cleanup remain beyond the first structured scheduler. Each
  needs a concrete consumer and a checked lifetime/cancellation protocol.

## 8. Implementation reading

Use these as references for mechanisms and proof obligations, not as mandates
to reproduce another language's runtime or surface syntax.

- [Generalized Evidence Passing for Effect Handlers — Xie and Leijen](https://xnning.github.io/papers/multip-tr.pdf).
  Read the tail-resumptive optimization and evidence restoration sections when
  checking the implemented direct-handler and control-ABI foundation. Its
  general continuation machinery is broader than this plan.
- [Algebraic Effect Handlers with Resources and Deep Finalization — Leijen](https://www.microsoft.com/en-us/research/publication/algebraic-effect-handlers-resources-deep-finalization/).
  Use for scope lifetime and E7 abandonment questions; multi-shot initializer
  machinery is not required by the chosen one-shot model.
- [Effekt: Captures](https://effekt-lang.org/tour/captures).
  Informs the implemented distinction between execution effects and values
  retaining capabilities; Fango uses its own restricted summary rules.
- [Continuation Passing Style for Effect Handlers — Hillerström and colleagues](https://dhil.net/research/papers/cps-handlers-draft-april2017.pdf).
  Useful for specifying E7's control translation before choosing frame layout.
- [Defunctionalization at Work — Danvy and Nielsen](https://www.brics.dk/RS/01/23/).
  Background for turning continuation functions into explicit frame variants.
- [Control.Exception: bracket and finally](https://hackage.haskell.org/package/base/docs/Control-Exception.html).
  Library-facing cleanup abstractions and acquisition/cancellation concerns;
  Fango's initial cancellation is cooperative, not Haskell async exceptions.
- [Rust: Futures and the Async Syntax](https://doc.rust-lang.org/book/ch17-01-futures-and-syntax.html).
  A useful state-machine analogy for E7–E9, not a portable Go runtime recipe.
- [Go FAQ: stack or heap allocation](https://go.dev/doc/faq#stack_or_heap).
  Explains why a source non-escape proof is not an allocation guarantee for
  generated closures and evidence environments.

Keep every release honest about what is implemented. In particular, finishing
State does not imply aborting handlers; finishing synchronous bracket does not
imply async cleanup; finishing a private machine backend does not imply that
unrestricted continuation values are sound or supported.
