# Scoped readers and effect-polymorphic tasks

Remaining task API and type-system work following the
[synchronous foundation](roadmap-simplification.md). Scoped memory readers are
implemented; the task contracts below remain targets. They do not reactivate the coroutine roadmap.
Current behavior remains in [readers](reference/library-readers.md) and
[tasks](reference/library-tasks.md).

## Reader: the complete effect row

DONE

`Reader.withBytes` and complete reader rows are [reference
contracts](reference/library-readers.md#reader). The pure constructor supports
independent nested cursors and preserves additional consumer effects. All buffering constructors now use scoped state; see the complete
[Reader and Writer contracts](reference/library-readers.md).

## Fresh scopes and escape checking

DONE

The restricted [scoped callback contract](reference/functions.md#scoped-callbacks),
[source escape checks](design/inference.md#scoped-callback-rows), and
[erased local-state boundary](design/core.md#scoped-state-boundary) are implemented.
General first-class rank-two types and recursive scoped runners are outside this
contract; they are not prerequisites for the task API below. Executing scoped
local state during compilation remains limited by the existing sidecar-native
restriction; extending the stage native host is separate unfinished work.

## Async orchestration and Task results

Replace the named-worker Task API with closure-based Async orchestration.
Business effects remain in the caller's row; the native runner exposes IO at
its boundary. Task and channel handles may outlive a runner. Native handles
and references may be shared, with runtime closed-handle errors and synchronized
operations rather than a blanket transfer restriction.

The target public API is:

```fango
effect Async err
type Task err value
type Channel value
type Outcome value = Completed value | Cancelled

run : (() ->{Async err, IO, Fail err | effects} value)
      ->{IO | effects} Outcome (Result err value)
within : (() ->{Async err, Fail err | effects} value)
         ->{Async err, Fail err | effects} value
spawn : (() ->{Async err, IO, Fail err | effects} value)
        ->{Async err | effects} Task err value
completed : Result err value ->{Async err} Task err value
await : Task err value ->{Async err, Fail err} value
awaitResult : Task err value ->{Async err} Outcome (Result err value)
wait : Task err value ->{IO} Outcome (Result err value)
cancel : Task err value ->{Async err} ()
cancelScope : () ->{Async err} ()
cancelled : () ->{Async err} Bool
checkpoint : () ->{Async err} ()
sleep : Int ->{Async err} ()
channel : Int ->{Async err} Channel value
send : Channel value -> value ->{Async err} Bool
receive : Channel value ->{Async err} Maybe value
close : Channel value ->{Async err} ()
parMap : (a -> b) -> List a -> List b
```

`err` is the common application failure type of a scope's tasks. `Fail err`
remains the effect for raising that failure; cancellation is a separate outcome,
not another application error. Do not expose Async.Error, a channel-closed error,
or a Go-panic result. The pure serial runner is deferred. `parMap` needs a bounded
internal scheduler and exposes no task handles.

`run` creates an independent root; `within` creates a child scope. Every scope
drains children and cleanup before completing. Body failure wins over child
failure; otherwise report the earliest submitted unobserved failed child.
Observation delivers a failure, and repeated observations retain the same result.
Application failure cancels siblings; individual child cancellation does not
cancel its parent. `await` propagates target cancellation, whereas `awaitResult`
exposes it. Current-task cancellation uses private Exit control and runs cleanup.
Nonpositive sleeps act as checkpoints. Arbitrary blocking IO is not automatically
interrupted by cancellation.

Channels have independent lifetimes and explicit idempotent close. Negative
capacity means zero, zero is unbuffered, a closed send returns False, and a
closed channel drains buffered values before receive returns Nothing. Transfer,
close, and cancellation must linearize without lost wakeups or Go channel panics.
The [internal runtime](design/tasks.md#async-runtime-foundation) implements these
scheduling foundations; public library integration remains unfinished.

## Generic scheduling representation gate

Keep Direct/Exit, honest effect rows, structural capture summaries, and sealed
native type indices. Use one checked concurrent invocation boundary rather than
one compiler primitive per public scheduling combinator. Native opaque payloads
must retain their source type index; an untyped result getter is not an encoding.

Inheritance must obey these rules:

- Stateless resumptive handlers inherit only when their dependencies qualify.
- `with shared state = initial` shares the state cell and serializes each entire
  operation. `taskLocal` takes a shallow snapshot at spawn and gives the child
  its own cell; native references inside that snapshot remain shared.
- An unmarked stateful handler is rejected at compile time when inherited.
  Check through generic helpers, imports, closures, records, and dictionaries.
  A check confined to the lexical spawn expression is insufficient.
- Rebuild inherited evidence transitively, preserve aliasing and shadowing, and
  replace Async and matching Fail evidence with child boundaries. Never copy a
  parent's abort target into a child. Other abort effects must be handled inside
  the child; local scoped permissions cannot be inherited.
- Parent return transformations do not transform child task results.

The remaining compiler work includes a persistent inheritance requirement for
higher-order function contracts, checking those requirements at handler use,
and reconstructing child evidence in both backends. Existing result-capture
summaries alone do not express which invocation of an abstract callback forks
its supplied handler evidence. Do not substitute runtime rejection or assume
all ambient handlers are inherited regardless of the child's effects.

## Delivery gates

1. DONE — Fresh callback rows, permission-aware composition, source escape
   checks, exported contracts, and Core signature validation are implemented;
   see [scoped callbacks](reference/functions.md#scoped-callbacks).
2. DONE — Pure memory readers compose across multiple instances and consumer
   effect rows in both backends; see [readers](reference/library-readers.md#reader).
   Scoped constructors preserve source, sink, and consumer effects.
3. Validate heterogeneous closure task packaging, inherited-handler policies,
   and child abort boundaries across modules in the evaluator and Go backend.
   Include negative tests through higher-order and generic helper calls.
4. Deliver the generic native orchestration API and migrate Task callers.
   Verify failure precedence, repeated observation, cancellation cleanup,
   channels escaping runners, and bounded pure parMap.

Preserve Core lint, differential/functional tests, editor grammar when syntax
changes, and vet. Inspect generated Go for ordinary state access and erased
scope identities. Measure performance only on an idle host after there is an
implementation to measure.
