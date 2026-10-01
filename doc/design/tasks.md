# Native tasks and explicit streams

[Design index](../design.md). [Async](../reference/library-async.md)
and [stream semantics](../reference/library-streams.md) own the public contract.

## Library state and traversal

The bundled Reader and Writer constructors use the
[scoped state boundary](core.md#scoped-state-boundary) for local buffers.
Their operations propagate complete rows; a runner discharges only its fresh
permission, preserving source, sink, and consumer effects. Scoped local state
must be installed inside the task.

Stream and Iterator are Fango records and ordinary recursive functions. State
is explicit in each step's return value. No compiler node recognizes streams,
iterators, map, filter, take, zip, or fold. Resource ownership belongs to the
scope performing the traversal, so there is no suspended-stack cleanup protocol.

## Async runtime foundation

The internal Async runtime separates application failure from cooperative
cancellation, drains child scopes before publishing task completion, and
reports the earliest submitted unobserved child failure unless the body itself
failed. Repeated task observations are stable. Cancellation propagates down
the task tree; cancelling one child does not cancel its parent. Go panics are
not translated into application outcomes.

Its channels linearize transfer, close, and cancellation withdrawal under one
lock. Closed channels retain buffered values until drained and reject blocked
and future sends. A channel has no owning runner. File and socket wrappers
serialize complete reads, allow close to interrupt blocking reads, and reject
later operations on closed handles. Socket writes are serialized separately.

The [Async API](../reference/library-async.md) uses these primitives. Its Fango
implementation installs child failure and cancellation boundaries before invoking
the user's callback. `AsyncLaunch` owns the concurrent call and seals the complete
`Result err value` under one native type index; `AsyncRebase` reconstructs its
inherited evidence. `ParallelMap` invokes pure callbacks with bounded concurrency
and preserves order without exposing task handles.

Each handler activation carries an origin, a factory for rebuilding its
operation closures, and a publication hook. The parent invokes the hooks of
every activation visible in the launch row before the task starts, which
switches those state cells and their dependencies to synchronized access;
inheriting an unpublished activation fails deterministically. Rebuilding
preserves the shared state cell and immutable lexical values; it substitutes child Async, cancellation, and matching Fail
boundaries transitively. Origin memoization preserves activation aliases, and
row shadowing retains the visible activation. Return clauses are not rebuilt.
Abort origins have no factory: an unsupported abort dependency fails at runtime
before the user callback executes. Type descriptors check replacement indices,
including indirect dependencies and deferred row projections.

Callback dependency summaries retain a known callee's own row rather than the
ambient row's widened upper bound. Closed callback arguments narrow the residual
row supplied to generic workers; open or indirect sources retain forwarding.
This prevents unrelated parent effects from becoming child dependencies.

The interpreter starts a fresh invocation environment and uses the same scope,
channel, and evidence-rebuilding runtime as generated Go. Imported capture scopes
retain their defining identities when loading cached modules, even when an
importer's result summary also mentions them. Lazy global initialization retains
its synchronization when tasks share the environment.

`AsyncSupervise` marks the private thunk enclosing `Async.run`. Generated Go
emits an ordinary invocation; the interpreter suspends periodic host polling
through the runner and its cleanup. The native root takes its context from
`FangoHost.ExecutionContext`, so host interruption cancels the whole task tree.
Cancellation is observed at source Async operations; it cannot cut cleanup short
at an incidental interpreter tick. Each worker evaluation has a fresh host
context, so an interrupted runner does not cancel later REPL inputs.

Pure parallel mapping inherits the caller's polling ownership. Outside a
supervised runner, its workers return host interruption to the evaluator after
joining; inside a runner, they cannot interrupt language cleanup.
