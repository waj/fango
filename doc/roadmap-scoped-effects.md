# Scoped readers and effect-polymorphic tasks

Remaining task API and type-system work following the
[synchronous foundation](roadmap-simplification.md). Scoped memory readers are
implemented, as is the native Async API and its host interruption contract.
Deferred extensions do not reactivate the coroutine roadmap. Current behavior belongs in
[readers](reference/library-readers.md) and [Async](reference/library-async.md).

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
contract; they are not prerequisites for the task API below. Stage execution
of other sidecar-native operations remains limited by the existing native-host
restriction; extending that host is separate unfinished work.

## Async orchestration and Task results

The [Async API](reference/library-async.md) and
[closure/evidence boundary](design/tasks.md#async-runtime-foundation) are
implemented. Task callers use Async, and the separate named-worker compiler and
runtime path has been removed. Closure, native-resource, inherited-handler,
concurrent publication, and host interruption checks cover the replacement.

The pure serial runner remains deferred. Runtime rejection of unsupported
inherited abort dependencies is the initial contract; inferred static
constraints for generic helpers remain a possible later improvement.

## Delivery gates

1. DONE — Fresh callback rows, permission-aware composition, source escape
   checks, exported contracts, and Core signature validation are implemented;
   see [scoped callbacks](reference/functions.md#scoped-callbacks).
2. DONE — Pure memory readers compose across multiple instances and consumer
   effect rows in both backends; see [readers](reference/library-readers.md#reader).
   Scoped constructors preserve source, sink, and consumer effects.
3. Validate heterogeneous closure task packaging, shared handler activations,
   and child abort boundaries across modules in the evaluator and Go backend.
   DONE — See the [inheritance contract](reference/library-async.md#handler-inheritance-and-aborts)
   and its higher-order, generic-helper, and runtime rejection tests.
4. Deliver the generic native orchestration API and migrate Task callers.
   DONE — Async callers, cooperative host interruption with cleanup and REPL
   recovery, and retirement of the named-worker path are implemented.


Preserve Core lint, differential/functional tests, editor grammar when syntax
changes, and vet. Inspect generated Go for ordinary state access and erased
scope identities. Measure performance only on an idle host after there is an
implementation to measure.
