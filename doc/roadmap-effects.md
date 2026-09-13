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
| E6: file resources, structured IO failures, grep-lite | Implemented cleanup scopes | Scoped file handles, typed IO errors, a bundled `Fail`, multi-file `grep` |
| E7: selective execution machines | Implemented control ABI and cleanup scopes | Internal one-shot suspension and cleanup frames |
| E8: owned iterators and scoped non-tail handlers | Scoped capture Core, E7 | Pull traversal and checked non-tail resumption |
| E9: structured async and cancellation | E6, E8 | Cooperative tasks, cancellation, nursery cleanup |

The shipped control-aware ABI preserves the tail-resume and scoped-state direct
fast path, and synchronous cleanup scopes are implemented on top of it. E6
extends the implemented state, exception, and cleanup foundation with resources
but no general continuation objects.
E7–E9 are explicitly deferred until a concrete suspension consumer warrants
their compiler and type-system cost. No milestone requires implementing the
whole table at once.

## E6. File resources, structured IO failures, and grep-lite

### Deliverable and rationale

Build the first concrete resource on the implemented cleanup scopes and
replace panic-based IO failures with typed values. The consumer is the
`grep`-lite example from the examples pipeline: pattern search across files
named on the command line, recursive directory walks, unreadable paths
reported and skipped, and grep's exit statuses (0 matches, 1 none, 2 error).
It forces every piece below and nothing beyond them: no suspension, no
continuation object, no goroutine per resource.

The existing `IO.readFile`, `writeFile`, `args`, and `exit` keep their types
and behavior. They are the legacy shapes; the new APIs sit beside them, and
the main roadmap owns their eventual deprecation.

### Surface

A bundled `Fail` module ends the per-file re-declaration of the failure
effect. It is deliberately not in the prelude, so modules that declare their
own `Fail` keep compiling:

```fango
module Fail exposing (Fail, fail, attempt, fromResult)

effect Fail error
    abort fail : error -> value

attempt : (() ->{Fail error | e} value) ->{e} Result error value
fromResult : Result error value ->{Fail error} value
```

`IO` gains a structured error vocabulary and two opaque handle types:

```fango
type Kind = NotFound | PermissionDenied | AlreadyExists | IsDirectory | NotDirectory | Other
type Error = { kind : Kind, path : String, message : String }
describeError : Error -> String

type Handle = Handle Int        -- exposed as `Handle`, never `Handle(..)`
type Directory = Directory Int  -- private to IO and File
```

`describeError` renders platform-stable text from the kind and path, so
fixture output does not depend on the host's error strings; `message` carries
the OS text and matters only for `Other`. Neither handle type derives `Show`
or `Eq`: the wrapped id must not leak through `show`, and a handle has no
meaningful equality. The constructors are private, so a program can obtain a
`Handle` only inside a scope and can never build, inspect, or compare one.

The file operations are declared inside the existing `effect IO` block in
call form, exactly like `readFileText`. The compiler adds `IO` to every
operation's type, so `openRead` is `String ->{IO} Result Error Handle` at each
use, gated by the row and rejected during staging by the existing rules. They
are not exposed; only fango wrappers in `IO` and `File` call them.

```fango
effect IO
    openRead   : String -> Result Error Handle = native
    openWrite  : String -> Result Error Handle = native     -- create or truncate
    openAppend : String -> Result Error Handle = native
    closeHandle    : Handle -> Result Error () = native
    handleHasInput : Handle -> Result Error Bool = native   -- EOF versus read error
    readHandleLine : Handle -> Result Error String = native -- readRawLine's contract
    writeHandle    : Handle -> String -> Result Error () = native
    readFileResult  : String -> Result Error String = native
    writeFileResult : String -> String -> Result Error () = native
    openDirectory      : String -> Result Error Directory = native
    readDirectoryEntry : Directory -> Result Error String = native -- "" at the end
    closeDirectory     : Directory -> Result Error () = native
    isDirectoryPath    : String -> Result Error Bool = native
```

`File` owns the scoped API over `IO.Handle`; the type lives in `IO` because
it must be declared beside the natives that name it and `IO` cannot import
`File`:

```fango
withFile   : String -> (IO.Handle ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
withOutput : String -> (IO.Handle ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
withAppend : String -> (IO.Handle ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
readLine   : IO.Handle ->{IO, Fail IO.Error} Maybe IO.Line
write      : IO.Handle -> String ->{IO, Fail IO.Error} ()
read       : String ->{IO} Result IO.Error String
writeAll   : String -> String ->{IO} Result IO.Error ()
listDirectory : String ->{IO} Result IO.Error (List String)
isDirectory   : String ->{IO} Result IO.Error Bool
```

`withFile path use` is `Scope.bracket` over an acquisition that raises `fail`
on an `Err`, so a failed open releases nothing; a failed close after a
successful body is the scope's failure, and after a failed body it is
recorded as suppressed, exactly as the implemented scope semantics say.
`readLine` reuses the console line helpers, so a file line has the same
EOF, terminator, and U+FFFD contract as `readLine()`.

Passing a named worker whose closed row lacks `Fail IO.Error` to `withFile` is
a row mismatch; wrap it in a lambda. That is the general higher-order row
subsumption question the main roadmap owns, not an E6 task.

### Mechanism A: fallible natives

The scalar sidecar ABI grows one shape, available only to `effect IO`
operations in the bundled `IO` module: a result `Result Error T`, with `T` a
boundary scalar, Unit, or a wrapper type from Mechanism A′, is implemented by a
Go function returning `(T, error)` (or `error` alone for Unit). A non-nil
error is classified by one shared `fangort` helper into a kind code, the
path, and the underlying message, and both backends construct
`Err (Error {...})` from those scalars; a nil error constructs `Ok`.

- Module validation runs before name resolution and sees spellings, so
  recognition of `Result Error T` is spelling-based and therefore restricted
  to the module the compiler controls; anywhere else it is
  `FALLIBLE NATIVE NOT ALLOWED`. Type checking then verifies the resolved
  shape: `Result.Result`, `IO.Error` with fields `kind`, `path`, `message`,
  and `IO.Kind` with exactly the six constructors in order, which is the
  compiler's contract with the classifier.
- The natives are effect operations, not value natives, because bundled value
  natives require an in-process registry entry and that registry cannot build
  ADT values. Staging is unaffected: the operations are not registered, so
  compile-time evaluation still rejects them.
- Sidecars may import only the Go standard library, so the classifier is
  invoked by generated code and by the worker's dispatch loop, never by
  `IO.native.go`. The worker protocol carries the failure in its own field,
  separate from infrastructure faults and native panics.
- Generated code emits the branch as straight-line Go at the call site — no
  `panic`, no `defer` — using the same constructor emission as ordinary ADT
  literals. The interpreter builds the same constructor values; both backends
  therefore agree by construction.
- The sidecar joins relative paths onto the working directory, so a raw
  `PathError` would carry a per-run absolute path. Every error is relabeled
  with the path the program supplied before it leaves the sidecar.

### Mechanism A′: opaque scalar wrappers at the boundary

A general boundary rule, not specific to IO: a native parameter or result may
name a type declared in the sidecar's own module with exactly one constructor
holding exactly one boundary scalar. It crosses to Go as that scalar and is
wrapped back into its constructor on return, in both backends. With a private
constructor this yields a type-safe opaque handle with no new runtime
representation; a two-field type, a record, a polymorphic type, or a type
from another module stays a `NATIVE ABI` error.

### Mechanism B: handles as capabilities

The bracket `ScopeID` never leaves the intrinsic — a callback's resource is an
abstract capture variable — so a handle escaping its scope is caught by the
type-based rules, and the whole mechanism is one switch: `IO.Handle` and
`IO.Directory` are compiler-known resource types, identified by canonical ADT
name, that `canCarry` treats as capability-carrying. Consequently:

- A `withFile` body may not return the handle, a closure over it, or a
  `Maybe`/record/list holding it: `RESOURCE ESCAPES`.
- A scoped handler's result that could retain a handle is rejected:
  `STATE RESULT ESCAPES`.
- `File.withFile`, `withOutput`, and `withAppend` are trusted scoped runners
  like `State.run`: their call sites are checked against the instantiated
  result type, and a user-written generic wrapper over them is rejected the way
  one over `State.run` is today. A resource runner's own body is allowed to
  call `Scope.bracket` with a polymorphic result, which is the same
  forwarding exemption `State.run` has.

Core lint recomputes the summaries with the same analyzer, so the proof is
repeated after elaboration. Runtime checks on the handle table (an unknown or
closed id) remain defensive and unreachable from accepted fango.

### Mechanism C: fixtures with files

The differential harness accepts, beside `X.fango`, an `X.files/` seed
directory copied into a fresh temporary working directory per leg, an
`X.args` list, and an `X.status` expected exit code; today every non-zero
exit is a harness failure. Failures are scripted portably, without `chmod`: a
seeded directory opened as a file succeeds at open and fails at the first
read with `IsDirectory`, which is the "failure after opening" case; a missing
path or parent is `NotFound`; a path through a regular file is
`NotDirectory`. `PermissionDenied` is covered by the classifier's unit test.

### Acceptance

- Copy, line-count, nested-scope, and error-kind fixtures run through both
  backends with byte-identical output; negative fixtures pin every escape
  diagnostic above plus the wrapper-shape and fallible-shape errors.
- A user sidecar fixture proves a private wrapper type round-trips through
  the boundary with its id unobservable.
- A REPL transcript shows a failing `withFile` body leaves the session
  usable and the handle closed.
- `grep`-lite runs with arguments and exit codes under both backends.
- Exploratory: a user-declared stateful handler *outside* the scope that
  stores the handle in its cell and returns it through `return`. If that is
  accepted, it is a pre-existing gap for every capture-capable resource, to
  be recorded in section 7 rather than fixed here.

No goroutine, channel, or continuation is introduced. The worker process is
lifecycle isolation for natives, not a suspended computation.

## E7. Selective one-shot execution machines

### Deliverable and rationale

Build the internal suspension backend with private Core fixtures first. Do not
enable source-level general resume merely because the machine can run it.
E8 supplies its static ownership contract. State, abort-only effects, cleanup
scopes, and E6 remain useful without E7.

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
tagged failures. A call with unknown transport uses its implemented compatible family;
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

Move cleanup-scope obligations into scope-owned machine storage. Suspension keeps them
pending. Completion or abandonment runs them in lexical nesting order. A
cleanup may call ordinary Direct/Exit functions initially; suspending cleanup
remains statically rejected.

An exit targets a live owning boundary and destroys only the intervening
continuation segment after its cleanups. Mark ownership consumed in compiler
control flow; do not add public `Resume`/`Discard` objects with dynamic checks.
Keep the original completion envelope while cleanup runs. A cleanup failure
changes that envelope the way a synchronous scope does, without skipping
remaining cleanups.

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
| Scoped files, locks, temporary resources | Mechanism implemented as `Scope.bracket`; E6 concrete APIs |
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
  source API reads it. Two questions remain open together, and E6 is where a
  concrete consumer appears: what a program may observe, and whether an exit
  can be classified as an ordinary non-error control transfer, which is the
  only way a failed cleanup could be made to supersede one. Choose the public
  observation API before promising a library contract.
- **Cleanup checks a synchronous scope cannot express:** early manual disposal
  of a borrowed handle and duplicated release authority need the E6 resource
  capability type before they can even be stated, and rejecting suspending
  cleanup needs E7 `Machine` transport to exist. None of them is checkable
  today, and none is reachable today either.
- **E6 resource/native ABI:** settled in the E6 section above — an opaque
  scalar-wrapper type erased only at the Go boundary, a handle table owned by
  the sidecar in both backends, and fallible natives restricted to the bundled
  `IO` effect. Promote to the design when E6 ships.
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
