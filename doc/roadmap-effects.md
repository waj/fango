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
existing reproducer, they may use proposed APIs. State-handler notation and
control/ownership annotations are marked as provisional. Go and Core snippets
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
- Add scope/capture checking before introducing hidden mutable state or scoped
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
| Add state parameters to handlers | Express State/Writer/Random without general continuations or global native cells (P). |
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
| Concrete State consumer | `stdlib/Random.fango`, `Random.native.go`; currently a process-global PRNG cell |
| Host effects / FFI | `stdlib/IO.fango`, `IO.native.go`, `internal/natives/`, native declaration validation and worker protocol |
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
| E1: scoped evidence and capture checking | Proven tail-resume Core | Compiler foundation for local state/resources |
| E2: parameterized tail-resumptive state | E1 | State, Writer, seeded Random, stateful examples |
| E3: control-aware calls and callback ABIs | Proven tail-resume Core; integrate E1 metadata | Stable direct/exit transport and higher-order calls |
| E4: abort-only effects and Result | E3 | Failure handlers; integrate E2 for State/failure examples |
| E5: synchronous cleanup scopes | E1, E4 | `Scope.bracket` / `finally`, generic cleanup across exits |
| E6: resource APIs and native error boundaries | E5 | Useful file/resource examples and structured IO failures |
| E7: selective execution machines | E3, E5 | Internal one-shot suspension and cleanup frames |
| E8: owned iterators and scoped non-tail handlers | E1, E7 | Pull traversal and checked non-tail resumption |
| E9: structured async and cancellation | E6, E8 | Cooperative tasks, cancellation, nursery cleanup |

E2 and E3 can be developed as independent increments after their prerequisites,
but no implementation should erase information the other needs. E1–E2 form a
useful release with no new non-local control flow. E1–E6 form a useful release
with state, exceptions, and resources but no general continuation objects.
E7–E9 are explicitly deferred until a concrete suspension consumer warrants
their compiler and type-system cost. No milestone requires implementing the
whole table at once.

## E1. Scoped evidence and capture checking

### Deliverable and rationale

Add compiler-only scope identities and capture summaries while preserving
ordinary function syntax. Do not expose mutable handler cells or scoped file
handles until this check can prove their valid lifetime (S).

An effect row says which operations execution may perform. A capture summary
says which particular local capabilities a value retains. One cannot replace
the other, especially because Fango currently adapts callbacks to erased-row
ABIs by closing over evidence. See
[Effekt's capture checking](https://effekt-lang.org/tour/captures) for the
distinction; the implementation below is a proposed restricted application.

### Proposed compiler metadata

```text
ScopeId                    -- fresh static binder, instantiated per activation
CaptureSet = set<ScopeId> | capture variables for generic value parameters
ValueInfo  = { type, captures }
Evidence   = { nominalEffect, typeArguments, scope, captures, control }
```

Introduce each local state's or resource's identity as a fresh skolem during
checking. Preserve its relation to runtime evidence without emitting a runtime
"is this scope still live?" check. The static binder and runtime activation
token serve different purposes; reentrant calls instantiate new activations.

At a scope boundary, require that the local identity is absent from the
escaping result and its transitive captures. Forbid writing a value capturing
that identity into an outer state cell, native storage, or retained callback.
Track captures through records, ADTs, function composition, dictionary method
values, partial operations, eta expansion, and generalized locals. A returned
function may require fresh caller-supplied evidence later without capturing
this activation; do not reject it merely because its arrow mentions an effect.

Do not impose short lifetimes on every immutable evidence value. A pure Reader
closure whose environment is durable and contains no local resource/state
obligation may remain first-class. Scope restrictions follow the capabilities
actually captured. Preserve existing legal pure evidence captures unless a
specific new lifetime rule applies, and test that compatibility explicitly.

Initial scope checking may conservatively reject some safe higher-order
programs. It must still admit standard traversals whose callbacks are used
synchronously and not retained. Add capture-polymorphic summaries for these
workers rather than silently trusting arbitrary callbacks.

An example rejection, in provisional scoped-API notation:

```fango
saved = File.withFile "input.txt" (\file -> \_ -> File.readAll file)
```

```text
RESOURCE ESCAPES
The returned function captures `file`, whose scope ends at this withFile call.
Return the contents, or use the function within the file's scope.
```

The same diagnostic must arise if the closure is hidden inside an ADT or
stored through an outer State effect. Returning immutable data read from the
file remains valid. Do not solve this by turning the returned function's hidden
resource access into an apparently pure closure on the Go heap.

### Implementation sequence and acceptance

1. Add metadata and constraints in inference, substitution, generalization,
   instantiation, and annotation checking. Keep scope names out of normal
   printed types unless needed to explain an error.
2. Propagate through module interfaces, instances and dictionaries, elaboration,
   lambda lifting, and callback adaptation. Check before open-row erasure.
3. Carry enough evidence into Core for lint to check scope introduction,
   availability, and non-escape after rewrites.
4. Apply the same checks to REPL inputs and generated/staged code. Checkpoints
   must roll back new scope/capture metadata on a failed input.
5. Test higher-order pass-through, enclosing-scope captures, legal returned
   pure closures, nested same-type scopes, records containing callbacks,
   cross-module wrappers, and deliberate escape through outer state.

Do not add a general public rank-N type language just for this milestone.
Scope-polymorphic intrinsic signatures can be compiler-owned initially. The
open implementation choice is how much capture polymorphism ordinary user
wrappers need; settle it with `runState`, `withFile`, and List traversal tests
before freezing metadata. A blanket ban on all escaping functions is not an
acceptable substitute for tracking the relevant captures.

## E2. Parameterized tail-resumptive handlers

### Deliverable and proposed surface

Implement State first, then move deterministic Random state into each handler
activation. State is not a reason to enable non-tail resume: explicit handler
parameters preserve the P fast path.

The following state notation is a candidate, not accepted syntax. Resolve it
with parser experiments before implementation; retain the semantics below even
if the spelling changes. `with` here would be contextual, and the state binder
is visible in operation and return clauses, not the handled body.

```fango
effect State s
    get : () -> s
    put : s -> ()

type StateResult s a = { value : a, state : s }

runState initial action =
    handle action() with current = initial of
        get () -> resume current with current
        put next -> resume () with next
        return value -> StateResult { value = value, state = current }

incrementAndSum() =
    before = get()
    put (before + 1)
    after = get()
    before + after

main() =
    result = runState 10 incrementAndSum
    print result.value
    print result.state
```

Expected output is `21` and `11`. Start with one handler parameter; a nominal
record can carry several fields. Avoid committing to a multi-parameter resume
syntax before one state value has a consumer.

The user-level type shape is:

```text
runState : s -> (() ->{State s | e} a) ->{e} StateResult s a
```

E1 adds the hidden scope/capture obligations; this ordinary-looking type alone
does not prove local-cell non-escape.

### Semantics and lowering

1. Evaluate initial state once before entering the handled body.
2. Each clause receives a snapshot binding for its current state. Ordinary
   source bindings remain immutable; the compiler owns the changing cell.
3. Evaluate the operation result and next-state expressions left to right,
   exactly once, under the clause's outer evidence environment.
4. Commit the next state immediately before the successful tail resume.
   If evaluation exits before that point, do not commit an unevaluated update.
5. Subsequent operations see the committed state. The return clause sees the
   final state once, after normal body completion.

Conceptual operation implementation:

```text
clause(arguments, stateSnapshot) -> (operationResult, nextState)
```

Illustrative direct Go ABI, with Unit erased at operation boundaries:

```go
type StateEvidence[S any] struct {
    Get func() S
    Put func(S)
}

func runState[S, A any](initial S, body func(StateEvidence[S]) A) StateResult[S, A] {
    current := initial
    ev := StateEvidence[S]{
        Get: func() S { return current },
        Put: func(next S) { current = next },
    }
    value := body(ev)
    return StateResult[S, A]{Value: value, State: current}
}
```

There is no continuation. The body continues when `Get` or `Put` returns.
Closure/cell allocations, if any, belong to handler setup, not each operation.
Do not promise stack allocation through an unknown Go function value. Optional
specialization can use an explicit `*StateCell[S]` environment and known
workers, or inline accesses; benchmark before making that representation
mandatory. Never use a process-global current-state pointer.

Nested handlers allocate distinct cells even with identical `State Int` types.
Same-label forwarding inside a clause resolves to outer evidence. Explicit
simultaneous access to two independent instances is a later named-capability
design; nominal labels alone do not supply it.

E3 must subsequently allow a State clause to invoke aborting outer evidence.
Tail-resumptive discipline describes its use of resume, not a guarantee that
every operation callback can use a plain-return ABI under every interpretation.

### Consumers and implementation tasks

- Add `State` and `StateResult` as ordinary bundled definitions where possible.
  Handler state is a compiler feature; the operations themselves need no native
  implementation.
- Implement Writer using an immutable accumulator initially. A private mutable
  Go builder is a later representation optimization that needs an ownership
  proof before returning its contents.
- Rewrite `Random.runSeeded` to carry the PRNG state in the handler; preserve
  the existing deterministic algorithm, inclusive/reversed range behavior,
  and golden output. Keep entropy acquisition separate in `runSystem`.
- Remove the process-global `SwapSeed`/advance machinery only when both
  backends can use the replacement. Audit the native registry, worker protocol,
  and staging exception that currently rejects process-state-observing Random.
  Deterministic seeded computation can become stage-safe after that audit;
  system entropy must not become compile-time observable accidentally.
- Update AST/checker state binding, handler metadata, Core state cell/parameter
  representation, clause termination, interpreter evidence, and Go emission.
- Preserve state snapshots captured by legal local closures; a reference to a
  mutable Go parameter must not change the meaning of a source snapshot. Apply
  the existing tail-loop capture discipline to the new environment as well.

### Examples and acceptance

Build a running counter, Writer-based diagnostic collector, deterministic
guessing-game test, and a small evaluator with an environment and state. Verify
nested seeded runs do not disturb outer draws. Once E4 permits abandonment,
verify that discarding an inner local state does not implicitly commit it to
the outer cell; explicitly forwarded outer effects still have their specified
observable consequences. Test parameterized ADT state, callback traversals,
grouped operation patterns, and state expressions with residual effects.

Benchmark many `get`/`put` pairs separately from handler setup: direct operations
must add no allocations proportional to operation count. Retain the full
existing runtime and compile-latency benchmark gates. Update the TextMate
grammar and tokenize representative fixtures if state syntax is introduced.

At this milestone's completion, correct the reference's statement that tail
resumption itself prevents state: the old limitation was absence of handler
parameters, not the tail-resumptive execution strategy.

## E3. Control-aware calls and stable callback ABIs

### Deliverable and rationale

Introduce control transport as compiler metadata before allowing new source
exits. Keep direct code direct (P), and prevent row erasure from concealing
non-local control (S). This can ship as a compiler refactoring with unchanged
source semantics and private synthetic Core tests for the new transport.

Keep two concepts separate:

```text
Operation discipline: TailResume | AbortOnly | ScopedOneShot (future)
Execution transport:  Direct | Exit | Machine (future)
```

Discipline determines how a clause may use its own resume. Transport describes
what executing a function or evidence callback can do, including through other
effects. A tail-resumptive State clause can execute `fail "bad"` before its
resume; then a function whose source row mentions only State still needs the
Exit ABI when passed that interpretation. A syntactic scan for `fail` in the
function itself cannot establish Direct transport.

### Analysis and representations

Compute transport from body operations, calls, residual rows, supplied evidence,
and closure captures. Internal handlers can discharge control effects: a
function catching all its exits may expose Direct transport despite using Exit
code internally. Unknown row tails require control variables or a conservative
transport; they must not default to Direct.

One candidate inference model is:

```text
Arrow = { argument, effects, result, control }
control ∈ { Direct, Exit, Machine, control variable }
join(Direct, Exit) = Exit
join(Exit, Machine) = Machine
```

Effect-instance evidence needs transport information too. Thread it through
substitution, instantiation, dictionary methods, annotations, higher-order
schemes, and module-owned worker interfaces. An explicit source annotation
cannot erase an inferred control obligation.

Go cannot use a type parameter to select between a plain result, an Outcome,
and a step protocol. Emit concrete ABI families instead of pretending a
`Control` generic parameter can change a Go function's signature:

```text
worker$direct(evD, args...) -> A
worker$exit(evX, args...)   -> Outcome<A>
worker$machine(...)        -> Step                 -- introduced in E7

StateEvidenceDirect<S>.Get() -> S
StateEvidenceExit<S>.Get()   -> Outcome<S>
```

For transport-polymorphic definitions, generate a bounded family from the
defining module's own source contract. Prefer one joined transport for a worker
and widening adapters over a combinatorial variant for every combination of
callback modes. Monomorphic direct workers need no unused Exit version unless
their declared polymorphism requires it.

Here transport polymorphism includes abstract effect evidence even when the
source has no type variables or open row. For example,
`() ->{State Int} Int` can be called with an aborting interpretation of State;
a closed row is not proof of Direct transport. Infer that evidence-dependent
mode in the defining module's contract and emit the required family there.

A Direct callback can be adapted to Exit by calling it and wrapping its result.
An Exit callback cannot be made Direct by dropping its exit field, swallowing
failure, or panicking. It requires a real enclosing handler that discharges the
exit, or a diagnostic. Machine adapters obey the same rule later.

### Implementation sequence

1. Preserve control constraints until after effect solving and evidence
   requirements are known. Derive an explicit call convention for every Core
   definition, lambda, operation slot, and application.
2. Extend `adaptFunctionValue` and eta expansion to adapt transport as well as
   evidence. Preserve E1 captures, partial application, and per-arrow timing.
3. Represent Exit results as a tagged sum conceptually:

   ```text
   Outcome<A> = Normal(A) | ExitRequest(target, operation, payload)
   ```

   In Go, choose typed structs/tags and typed operation payload variants where
   feasible. A shared erased dispatch boundary may box payloads, but every
   producer/consumer must be tied to the same checked operation descriptor.
   Do not turn failed runtime type assertions into the type system.
4. Lower ANF call results so exit propagation occurs before the next source
   expression. Include calls in arguments, guards, record construction, native
   adapters, return transformations, and handler clause prefixes.
5. Teach Core lint to check call convention, evidence-slot agreement, complete
   Outcome handling, and absence of discarded exit paths. Extend dumps so
   goldens make ABI choices inspectable.
6. Implement equivalent interpreter outcomes. Keep interpreter-internal errors
   distinct from language-level ExitRequest values.

Native calls that cannot invoke Fango callbacks need only declared native
error conversion. Do not allow a future foreign callback to suspend or abort
across an ordinary foreign frame without an explicit adapter and checked ABI.

### Acceptance and examples

- Pure List map, effectful map, a callback stored in an ADT, a dictionary method,
  and an annotated open-row handler wrapper choose compatible transports.
- A pure callback can be used in an Exit worker without losing its lexical
  evidence or changing evaluation timing.
- An interpreted State `get` that calls an outer failure handler propagates
  through a worker with no textual failure operation.
- Early-effect curried functions preserve their early transport; partially
  applied final-effect workers remain pure at construction.
- Adding a downstream Exit consumer does not alter already-generated dependency
  bytes or select different class instances.
- Existing direct-only emitted functions do not acquire per-call outcome tags
  unnecessarily. Measure any unavoidable adapters and emitted code-size growth.
- Cross-module and recursive control constraints terminate; no mode is guessed
  from a call site's available Go type after erasure.

## E4. Abort-only effects, Result, and state/failure composition

### Deliverable and source discipline

Add exception-like failure without saving continuations (P). Statically declare
that these operations do not resume (S), rather than interpreting any missing
resume in an existing resumptive clause as an implicit abort.

The operation-level `abort` marker below is a proposed spelling, not current
syntax. A declaration marker is preferred to guessing discipline from whichever
handler happens to be installed; it stabilizes checking and exception ordering.
Finalize its spelling before changing the parser/editor grammar.

```fango
effect Fail error
    abort fail : error -> a

type Result error value = Err error | Ok value

attempt action =
    handle action() of
        fail error -> Err error
        return value -> Ok value

readPositive text =
    case parseInt text of
        Nothing -> fail "expected an integer"
        Just n -> if n > 0 then n else fail "expected a positive integer"

main() =
    result = attempt (\_ -> readPositive "-3")
    case result of
        Ok n -> print n
        Err message -> print message
```

`parseInt` is a proposed library consumer. The example's result-polymorphic
`a` requires a narrow extension: an abort-only operation may have a universally
quantified result variable because it never produces a normal operation
result. Do not use this to claim arbitrary operation-local polymorphism works.
No generic method returning a caller-selected `a` is needed in the evidence
slot: the operation emits an exit payload and its normal path is unreachable.
Check that such result variables do not occur in payload arguments or hidden
constraints that this implementation cannot represent. General polymorphic
operations have a separate decision in section 7.

An abort clause has no resume binding. Reject any `resume` occurrence as
`RESUME IN ABORT CLAUSE`. Existing resumptive clauses still require a tail
resume on every normal path; a call to an abort-only effect can terminate an
exceptional path and discharge its pending obligation. In particular:

```text
if valid then resume answer else fail error
```

is legal once its failure is known to be abort-only. A branch returning an
ordinary value instead of resuming is still illegal. Extend the tail-resume judgment with
explicit exit terminals, not a permissive "may call an effect" exception.

### Exit routing and ordering

An abort operation must evaluate arguments, identify its target activation from
evidence, and return an ExitRequest. Do not execute the target clause at the
perform site. Propagate the request until that target's boundary, finalizing
intervening scopes once E5 is available. Then invoke the abort clause with its
outer evidence and payload.

```text
handleBoundary(h, body):
    outcome = run body with h installed
    Normal(value)          -> run h.return(value) in outer evidence
    Exit(target=h, op, arg) -> run h.clause[op](arg) in outer evidence
    Exit(target=other, ...) -> propagate unchanged
```

If the clause itself exits, propagate its new request; do not route it back to
the same clause. If the return transformation exits, do not rerun it. Handler
activation identity is essential when recursive invocations install identical
effect labels. Use an addressable non-zero-sized token or another explicit
unique-activation representation; distinct zero-sized Go pointers are not a
sound identity scheme.

Typed Go sketch for one statically resolved failure target:

```go
func checked(x int64, fail FailEvidence) Outcome[int64] {
    if x < 0 {
        return exitInt(fail.Target, FailOperation, "negative input")
    }
    return normalInt(x)
}

func calculate(x int64, fail FailEvidence) Outcome[int64] {
    r := checked(x, fail)
    if r.IsExit { return r }
    return normalInt(r.Value + 1)
}
```

The `exitInt` helper constructs a checked payload; it does not call the failure
clause. Normal paths need no continuation allocation. Consider struct layouts
that keep normal results unboxed and measure whether failure payloads allocate.

### State and transaction examples

With E2, implement a parser carrying its cursor in State and failing with a
structured parse error. Use nested handlers to make state visibility explicit:

```text
runState initial (attempt action)
    -> StateResult<S, Result<E, A>>
    -- final state remains observable after a caught failure

attempt (runState initial action)
    -> Result<E, StateResult<S, A>>
    -- failure discards the local state handler's normal result
```

These lines describe compositions, not literal Fango application syntax.
The second composition does not undo outer state changes or IO. A transaction
over an immutable state value can run against a private trial value and commit
only on success:

```text
trial := parent.current
outcome := runAttemptWithPrivateState(trial, parser)
on success: parent.current := outcome.finalState
on failure: leave parent unchanged
```

An alternative parser runs as a fresh computation from the original cursor.
This permits ordinary backtracking without resuming one continuation twice.
Mutable native maps require copying/undo logs; sharing a map pointer is not a
transaction. External writes require domain-specific compensation.

### Implementation and acceptance

- Add operation discipline to declarations, resolution, imported metadata,
  inference, Core, dump formats, and generated evidence types.
- Add `Result` to the stdlib here or as a preceding independent library change;
  do not make a runtime Outcome implementation leak into its public definition.
- Preserve nominal effect identity, handler coverage, exhaustive equation
  groups, and operation argument evaluation before the exit is raised.
- Test non-Unit results, generic handled answers, narrow polymorphic abort
  results, nested same-label recursion, failure in clauses/return groups,
  and unrelated residual effects.
- Test State/failure handler ordering with observable final states. Add a
  cursor parser with an alternative that intentionally fails after consuming
  input, then succeeds from the original position.
- Top-level unhandled custom effects remain compile-time errors. Choose an
  explicit IO/CLI runner policy for designated application failures; do not
  silently install default handlers for all Fail instances.
- Extend staging deliberately: a handled pure Fail may be evaluated at compile
  time, while unhandled effects and native access remain prohibited. Keep
  `Meta.fail` diagnostic attribution and rollback behavior intact.

## E5. Synchronous cleanup scopes without new cleanup syntax

### Deliverable and rationale

Expose `Scope.bracket` and optionally `Scope.finally` using ordinary function
application. Give them compiler-supported scope semantics. A `finally` keyword
or handler clause is not required; any later syntax is optional sugar.

Do not repurpose `return`: it remains a normal-result transformation. Generic
cleanup must run when an arbitrary residual effect exits, and when a handler's
return transformation or abort clause fails. A handler for one named Fail
cannot guarantee this for an open effect row. The distinction is motivated by
[deep finalization](https://www.microsoft.com/en-us/research/publication/algebraic-effect-handlers-resources-deep-finalization/).

Proposed APIs, using existing Fango expression syntax:

```fango
withFile path action =
    Scope.bracket
        (\_ -> File.open path)
        File.close
        action

main() =
    text = withFile "input.txt" (\file -> File.readAll file)
    print text
```

`File` is a future E6 API. E5 first tests the mechanism with a fake resource
whose acquire/use/release operations record events. `Scope` is identified by
resolved declaration identity, not by the spelling of arbitrary user functions.
Do not extend user native templates with unrestricted compiler intrinsics.

### Type and lifetime contract

Conceptual signature before adding scoped capture constraints:

```text
bracket : (() ->{ea} r)
       -> (r ->{er} ())
       -> (r ->{eb} a)
       ->{ea union er union eb} a

finally : (() ->{eb} a) -> (() ->{er} ()) ->{eb union er} a
```

The union notation is explanatory, not source row syntax. Scope introduction
also constrains `a` and its captures not to retain a borrowed resource identity.
For a resource-owning bracket, acquisition transfers ownership to the scope,
the body borrows it, and release is its sole terminal disposal authority.
The body must not manually close the borrowed handle. Abstract APIs and E1's
scope checking must enforce that distinction; non-escape alone does not prevent
double-close inside the scope.

A generic bracket over an ordinary integer does not make that integer linear.
Special resource contracts apply when the resource type/capability carries an
ownership obligation. E6 must define that native resource type, and the compiler
must distinguish the body's borrowed view from the release authority without
exposing unchecked casts. Compiler-owned scope-polymorphic signatures are
acceptable initially; typed wrappers still have to preserve their constraints.

The intrinsic calls the body once on successful acquisition. It registers
release before the body executes and invokes release once when the scope exits.
Reject attempts to move or duplicate a pending cleanup obligation in Core.
This does not prove that arbitrary release code terminates or that an OS close
succeeds. Initially reject cleanup with a suspending control effect and require
its own scope-local obligations to be discharged before it returns.

### Lifecycle and observable order

| Event | Required behavior |
| --- | --- |
| Acquisition fails | Propagate failure; no release of an unacquired resource |
| Body returns normally | Release, then expose its result |
| Failure caught within the body | Continue body; release at actual scope exit |
| Exit targets an outer handler | Release before invoking the outer abort clause |
| Return transformation fails inside scope | Release before propagating that failure |
| Nested scopes exit | Release in reverse successful-acquisition order |
| Release fails | Continue outer cleanup; apply the failure policy below |
| Future machine suspends | Keep resource and obligation alive; do not release |
| Future cancellation/abandonment | Release as part of terminating owned work |

For an outer Fail handler enclosing a file scope, require this trace:

```text
open -> body -> fail requested -> close -> outer fail clause
```

The reverse placement of scopes has a different trace. If a cleanup scope
encloses a handler, it covers both the body and whichever handler result path
runs; that scope releases after the handler clause completes. Write tests for
both arrangements. Do not apply one implicit finalizer ordering to every
possible syntactic nesting.

Finalizers execute with their lexical outer evidence, not whatever inner
handler stack happened to be installed at the exit point. Store that evidence
in the obligation. Finalizer code may itself use nested, well-scoped handlers.

### Core and direct Go lowering

Use an explicit Core node or equivalent verified region structure:

```text
Bracket {
    scope: ScopeId,
    acquire: Thunk<Resource>,
    release: Owned<Resource> -> Outcome<Unit>,
    body: Borrowed<Resource, scope> -> Outcome<A>,
    captures, residualEffects, control
}
```

It must intercept all language exits, not just exceptions with one payload
type. Lower function-shaped source intrinsics during elaboration; add rules to
the Core rewriter/linter, interpreter, codegen, and staging whitelist.
Staging may execute a scope only if all constituent code is stage-safe; an
empty surface row does not permit native resources at compile time.

```go
func bracket[R, A any](acquire func() Outcome[R],
    release func(R) Outcome[Unit], use func(R) Outcome[A]) Outcome[A] {
    acquired := acquire()
    if acquired.IsExit {
        return propagate[A](acquired.Exit)
    }
    resource := acquired.Value
    bodyResult := use(resource)
    cleanupResult := release(resource)
    return combineOutcomes(bodyResult, cleanupResult)
}
```

These ordinary Go functions implement P without saved continuations. A plain
result variant can elide Outcome plumbing if the scope cannot exit. Go `defer`
is an optional lowering for suitable synchronous cleanup, not the definition
of language lifetime. Never put `defer close(resource)` in an operation
callback implementing `resume resource`: it runs when that callback returns,
before the resumed computation uses the resource.

### Cleanup failure and cancellation policy

Adopt this default for synchronous cleanup:

- Body succeeds and cleanup succeeds: return the body value.
- Body succeeds and cleanup fails: propagate cleanup failure.
- Body fails and cleanup succeeds: preserve the original failure.
- Both fail: preserve the original failure as primary and attach cleanup
  failure as secondary information in deterministic inner-to-outer order.

An ExitRequest may denote a non-error control exit as well as a diagnostic
failure. Secondary failures cannot just be attached to an arbitrary user's
payload without a representation contract. Before shipping, define an internal
completion envelope and public reporting mechanism: an ordinary non-error exit
with failed cleanup should become a designated cleanup failure retaining the
original exit as context. Keep this conversion in the scope/runner protocol,
not in every user's Fail payload. Tests must pin all combinations.

This policy ensures outer resources still get their release attempt even if an
inner release fails. Initial release code can report ordinary failures but may
not suspend or deliberately jump to an unrelated non-error control target;
reject that unsupported cleanup mode statically. It may catch such control
internally and return normally. Do not silently discard an unsupported exit.

For later cooperative cancellation, install the obligation without a cancellation
delivery point after successful acquisition. If acquisition itself creates an
external resource and then can fail before returning it, acquisition owns its
partial-work cleanup. Registration cannot repair an incorrectly implemented
acquire function. While releasing, defer repeated cancellation delivery until
that release attempt completes; then continue unwinding. E9 formalizes the
scheduler protocol. Fatal process exit and nonterminating cleanup are not
covered by an exactly-once invocation guarantee.

### Acceptance and a smaller library-only alternative

A library-only bracket can catch one known Fail into `Result`, release, and
re-raise. Keep that as a test/example explaining the difference, but do not use
it as the implementation of a guarantee over arbitrary open rows. Its type
would need to exclude all other abandoning effects.

Test fake resources with event logs for every row of the lifecycle table,
nested cleanup failure, failure in acquisition, target handler identity,
failure in return transformations, legal use in a callback, and direct/Exit
ABI adaptation. Reject escape, early manual disposal of borrowed handles,
duplicated release authority, and suspending cleanup. Verify there is neither
a continuation object nor a dynamic consumed-state check in generated code.

## E6. Resource APIs, native boundaries, and useful IO errors

### Deliverable and rationale

Build a concrete file API using E5, and replace selected panic-based IO failures
with structured results/exits. Keep the foreign interface narrow. A file
resource is not a scalar integer users can retain after closing (S), and a
resource callback must not force the general continuation runtime (P).

Suggested consumer APIs, all provisional:

```fango
copy input output =
    File.withFile input (\reader ->
        File.withOutput output (\writer ->
            File.copy reader writer))

main() =
    result = attempt (\_ -> copy "input.txt" "output.txt")
    case result of
        Ok () -> print "copied"
        Err error -> print (File.describeError error)
```

Build a line-counting example and a copy example with scripted failure after
opening the second resource. Do not call the copy operation transactional:
closing resources does not undo bytes already written. Add an atomic-replace
example only with explicit temporary-file/rename semantics and cleanup.

### Representation and boundary tasks

- Introduce an abstract scoped file capability and an owned close authority.
  A borrowed handle permits reading/writing but does not expose public close.
  Decide whether low-level owned open/close are compiler-private initially;
  that is preferable to exposing unconstrained manual ownership prematurely.
- Extend bundled native handling deliberately for this type. The existing
  sidecar ABI accepts closed scalars/Unit, and the interpreter uses a separate
  persistent native worker. A generated-program `*os.File` cannot simply be
  serialized to that worker. Use worker-owned opaque IDs behind checked
  capabilities and explicit release messages, or a focused bundled resource
  boundary with equivalent semantics. IDs are not source-level resource types.
- Define worker shutdown, broken transport, partial acquisition, and cleanup
  acknowledgment. The interpreter waits for release completion when the source
  scope exits; it must not report success while cleanup remains queued.
- Keep infrastructure/protocol faults separate from ordinary file errors and
  invalid source lifetime transitions. Runtime checks protecting a native handle
  table may remain defensive, but cannot substitute for Fango scope checking.
- Preserve existing IO line endings, EOF representation, host binding,
  arguments, directory, formatting, and interpreter/compiler parity.
- Convert expected OS failures to typed `Result` values at the native boundary;
  a Fango wrapper can raise the designated Fail effect. Native panics remain
  foreign failures with an explicit boundary policy, not the implementation of
  language exceptions. Do not promise recoverable cleanup after `os.Exit`.
- Start with fixed-signature custom Console/File effects for testing and
  redirection. Full builtin IO interception remains gated by its declaration
  and polymorphism representation; do not bypass the current rejection without
  checking native defaults, classes, and evidence timing.
- Keep resource access and system entropy forbidden during staging. Retain the
  existing interpreter worker isolation and native-source cache invalidation.

### Acceptance

Run compiled/interpreted copies and line-counting with scripted acquisition,
read, write, and close failures; compare result diagnostics and cleanup traces.
Test nested files, callback captures, REPL recovery, worker shutdown, and
concurrent test isolation. No goroutine-per-resource/effect or continuation
channel is introduced to implement a synchronous language operation. Existing
worker process infrastructure is not a suspended Fango continuation engine.

## E7. Selective one-shot execution machines

### Deliverable and rationale

Build the internal suspension backend with private Core fixtures first. Do not
enable source-level general resume merely because the machine can run it.
E8 supplies its static ownership contract. E1–E6 remain useful without E7.

Use an ANF-to-control-flow lowering, optionally expressed through selective CPS
internally, followed by defunctionalization. Emitting chains of Go closures
calling one another is not acceptable: it recreates stack growth without TCO
and often allocates per transition. The output must be an iterative dispatcher
over explicit frames (P). Background reading:
[CPS for effect handlers](https://dhil.net/research/papers/cps-handlers-draft-april2017.pdf)
and [defunctionalization](https://www.brics.dk/RS/01/23/).

### Frame construction

1. Identify actual suspension points after evidence/control solving, including
   indirect callbacks and operation clauses whose residual effects suspend.
2. Split execution into basic blocks at those points, calls into Machine
   workers, normal returns, and exits.
3. Compute liveness. Save only values required by later blocks, plus relevant
   evidence, scope ownership, result destinations, and cleanup obligations.
4. Generate typed frame variants and a dispatcher. A single PC plus locals
   suffices only for a single activation; non-tail recursion and indirect
   calls need explicit caller frames.
5. Express tail transitions as frame reuse/parameter assignment and iteration.
   Preserve captured source snapshots before mutating reused frame fields.
6. Use indices or stable storage where frame buffers can grow. Do not retain
   pointers into a slice across an append that may relocate its backing array.
7. Clear dead references when popping/reusing frames so GC does not retain
   completed trees, resources, evidence, or large intermediate results.

Conceptual internal representation:

```text
Frame {
    codeId, pc,
    liveLocals: typed variant,
    caller: frame index,
    resultSlot,
    lexicalEvidence,
    cleanupBoundary
}

Step = Continue | Call(frame) | Return(value)
     | Request(target, operation, typedPayload)
     | Exit(completion)

Machine { frames, activeFrame, pendingCompletion, scopeOwners }
```

These names are not public runtime APIs. Keep transitions in a loop; a
dispatcher must not recursively call itself on every `resume`, callback return,
or subsequent effect. A tagged PC is execution state, not a runtime test of
source resume linearity.

### Calls, modules, and evidence

Direct workers still use their existing ABI. Machine workers call Direct code
normally when its evidence is proven non-suspending. Exit adapters preserve
tagged failures. A call with unknown transport uses its E3-compatible family;
never suspend through an unconverted direct caller and attempt to reconstruct
that caller's Go frame afterward.

Cross-module frames require a stable dispatch interface, such as an exported
module-owned step worker and typed frame constructor. Preserve package DAGs and
deterministic source-module emission; do not fuse unrelated modules into a
single generated switch just to support mutual recursion. Minimize interface
boxing, but prefer a checked module boundary to an unsafe global frame union.

Retain deep handler semantics: resumption reinstates the handled computation's
evidence, while operation clause prefixes and return transformations use their
lexical outer evidence. Save distinct frames for these contexts. Non-tail
resumption later needs a destination for the completed handled result in the
operation clause, not just the operation result's destination in the body.

### Cleanup and exits in a machine

Move E5 obligations into scope-owned machine storage. Suspension keeps them
pending. Completion or abandonment runs them in lexical nesting order. A
cleanup may call ordinary Direct/Exit functions initially; suspending cleanup
remains statically rejected.

An exit targets a live owning boundary and destroys only the intervening
continuation segment after its cleanups. Mark ownership consumed in compiler
control flow; do not add public `Resume`/`Discard` objects with dynamic checks.
Keep the original completion envelope while cleanup runs. A cleanup failure
changes that envelope according to E5 without skipping remaining cleanups.

### Acceptance and costs

Use private machine fixtures for repeated operations, nested handlers, a
non-tail recursive tree traversal, mutual recursion across module boundaries,
deep caller chains, return transformations, early exit, and cleanup failure.
Compare with a simple reference execution model and the direct backend where
the program belongs to both subsets.

Measure stack depth independent of operation count. Separately measure maximum
live frame depth, frame-buffer growth, per-step allocations, dispatch time,
and output size. One-shot operation permits reuse, not unbounded computation
with zero storage. Fixed-depth repeated suspension should amortize frame
storage rather than allocate a closure/channel at every event.

Implement an equivalent loop in the Core interpreter. Do not use its old
recursive evaluator stack as the suspended continuation, and do not activate
the retired goroutine engine for interpreter parity. E7 ships only when its
machine Core is independently linted and has no source-accessible unchecked
continuation entry point.

## E8. Owned iterators and scoped non-tail resumption

### E8a: a scoped iterator consumer

Start with a structured iterator scope that owns the suspended producer. The
consumer runs within that scope; exiting it cancels unfinished production and
runs its cleanup. This limits the first lifetime problem and provides a real
use case for E7 without raw escaping continuations.

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
  the E5 completion envelope without skipping other releases.
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
| State, Writer, per-run deterministic Random | E2, with E1 scope/capture checking |
| Local memoization | E2; cache pure computations or explicitly define skipped-effect semantics |
| Failure, early return, parser alternatives | E4; fresh attempts, no continuation cloning |
| Scoped files, locks, temporary resources | E5 mechanism, E6 concrete APIs |
| State rollback | E4 with private immutable state; not automatic external rollback |
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

- **E1 capture polymorphism:** choose the minimal summary language that permits
  synchronous higher-order wrappers and prevents hidden capability escape.
  Prove it on ADT/dictionary/eta-adaptation examples before row erasure changes.
- **E2 state spelling:** finalize contextual binder/resume syntax, evaluation
  order, and diagnostic spans. One record-valued parameter is sufficient
  initially. No state-syntax choice implies a need for cleanup syntax.
- **E3 ABI families:** validate the joined-mode strategy against recursive
  higher-order functions and per-module determinism. Document exactly when a
  Direct/Exit adapter is generated and what metadata survives erasure.
- **E4 abort spelling and result polymorphism:** freeze operation discipline in
  declaration metadata. The special abort-result variable is not a general
  generic-operation implementation. Keep mixed abort/resume handlers outside
  the first exception release.
- **E5 completion envelope:** settle how cleanup errors are reported alongside
  arbitrary failure payloads and how cleanup failure supersedes a non-error
  exit. Choose public observation APIs before promising a library contract.
- **E6 resource/native ABI:** choose an abstract capability and owned-release
  representation compatible with both generated Go and the interpreter worker.
  Do not expose worker IDs as freely usable source handles.
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
  Do not introduce them incidentally while implementing E2 nesting.
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
  checking the direct-handler foundation through E3. Its general continuation machinery is broader than this plan.
- [Algebraic Effect Handlers with Resources and Deep Finalization — Leijen](https://www.microsoft.com/en-us/research/publication/algebraic-effect-handlers-resources-deep-finalization/).
  Use for E5/E7 scope lifetime and abandonment questions; multi-shot initializer
  machinery is not required by the chosen one-shot model.
- [Effekt: Captures](https://effekt-lang.org/tour/captures).
  Useful for E1's distinction between execution effects and values retaining
  capabilities. E1 still needs its own Fango-specific rules and implementation.
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
