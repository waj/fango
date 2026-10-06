# Native tasks and explicit streams

[Design index](../design.md). [Async](../../stdlib/Async.fango)
and [stream semantics](../../stdlib/Stream.fango) own the public contract.

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

The native Async runtime stores an ordinary task value or cooperative
cancellation, and drains child scopes before publishing completion. Every exit,
including normal return, seals registration, cancels unfinished children, and
waits for their cleanup without inspecting their values. The active-task registry
removes a task at publication; handles keep its stable outcome without retaining
the owner's registry. Registration and closure share the registry lock: a late
launch returns a cancelled handle without executing its callback. A returned application
Result has no runtime failure state, observation flag, or sibling cancellation
policy. Repeated and concurrent observations are stable. Cancellation propagates
down the task tree; cancelling one child does not cancel its parent or siblings.
Go panics are not translated into application outcomes.

Every spawned task has its own implicit lifetime scope. An explicit nested
`Async.scope` retains the executing task's identity while changing the current
lifetime owner. Propagating an observed cancellation targets that executing task,
including its other scopes and descendants; normal nested exit cancels only the
nested scope's children. Draining waits without a cancellation-sensitive select,
so cancellation arriving during cleanup cannot let the owner return early.

Task selection subscribes to each candidate's completion under its publication
lock, checking already-published outcomes at registration. One buffered
notification selects an index; completion, cancellation, and return withdraw all
subscriptions. There are no watcher tasks or process-global selection state.
Selection creates no ownership relationship and exposes no native payload:
the Fango observer reads the selected handle's sealed result index.

Its channels linearize transfer, close, and cancellation withdrawal under one
lock. Closed channels retain buffered values until drained and reject blocked
and future sends. A channel has no owning runner. File and socket wrappers
serialize complete reads, allow close to interrupt blocking reads, and reject
later operations on closed handles. Socket writes are serialized separately.

The [Async API](../../stdlib/Async.fango) uses these primitives. Its Fango
implementation installs child Async and cancellation boundaries before invoking
the user's callback. Nullary Async manages lifetimes; `Task value` retains only
the completion's value index. The spawn callback keeps its own declared budget,
and all of its effects, including IO, are charged at launch. `AsyncLaunch` owns
the concurrent call and seals the whole value under the `RawTask value` native
storage index; lint independently matches that index to `Outcome value` and
verifies the nullary scope representation and constructors. `AsyncRebase`
reconstructs inherited evidence and permits only child Async and cancellation
overrides, never application abort replacements. `ParallelMap` invokes pure
callbacks with bounded concurrency and preserves order without task handles.

Application aborts must be handled inside spawned callbacks. `spawnAttempt` is
an ordinary Fango wrapper installing Fail locally and returning a Result; it
adds no native completion state. Root and nested runner bodies execute in their
caller's goroutine, so an abort leaving them propagates to its lexical handler.
Their bracket release cancels and drains remaining children first. An abort
caught inside the body does not leave that lifetime boundary. `runOutcome`
returns the root value or cancellation; `run` wraps it for Unit-returning
bodies and discards the outcome only after cancellation and draining finish.

`both`, `all`, and `race` are Fango functions over local Fail handling, explicit
scopes, and task selection. `all` removes each selected handle from its pending
list and stores successful values by original index in an immutable dictionary;
`both` tags its heterogeneous values and uses the same collector. `race` selects
one terminal outcome. The selected Result leaves the scope before Fail is raised
in the caller, so losing tasks drain first. Application failure is observable
only after the failing task's own cleanup and descendant drain. Caller or selected
participant cancellation remains cancellation; caller cancellation during drain
takes precedence over a selected application failure. Later losing errors and
cleanup failures suppressed beneath cancellation are discarded. The API comments
in [Async](../../stdlib/Async.fango) own these policies and empty-input behavior.

Cancellation-sensitive Async operations in a finalizer still stop immediately
once cancelled; cancellation masking is not provided. The drain itself is native
and unconditional. Cancellation-safe close and cancel operations remain usable.

Each handler activation carries an origin, a factory for rebuilding its
operation closures, and a publication hook. The parent invokes the hooks of
every activation visible in the launch row before the task starts, which
switches those state cells and their dependencies to synchronized access;
inheriting an unpublished activation fails deterministically. Rebuilding
preserves the shared state cell and immutable lexical values; it substitutes
child Async and cancellation boundaries transitively. Origin memoization
preserves activation aliases, and row shadowing retains the visible activation.
Return clauses are not rebuilt.
Abort origins have no factory: an unsupported abort dependency fails at runtime
before the user callback executes. Type descriptors check replacement indices,
including indirect dependencies and deferred row projections.

Callback dependency summaries retain a known callee's own row rather than the
ambient row's widened upper bound. Closed callbacks and callback lists narrow
the residual row supplied to generic workers. Lists can subtract their element
callbacks' explicit handled labels while forwarding a shared abstract tail;
other open or indirect sources retain forwarding.
This prevents unrelated parent effects from becoming child dependencies.

The interpreter starts a fresh invocation environment and uses the same scope,
channel, and evidence-rebuilding runtime as generated Go. Imported capture scopes
retain their defining identities when loading cached modules, even when an
importer's result summary also mentions them. Lazy global initialization retains
its synchronization when tasks share the environment.

`AsyncSupervise` marks the private thunk enclosing `Async.runOutcome`, which
also underlies `Async.run`. Generated Go emits an ordinary invocation; the
interpreter suspends periodic host polling
through the runner and its cleanup. The native root takes its context from
`FangoHost.ExecutionContext`, so host interruption cancels the whole task tree.
Cancellation is observed at source Async operations; it cannot cut cleanup short
at an incidental interpreter tick. Each worker evaluation has a fresh host
context, so an interrupted runner does not cancel later REPL inputs.

Pure parallel mapping inherits the caller's polling ownership. Outside a
supervised runner, its workers return host interruption to the evaluator after
joining; inside a runner, they cannot interrupt language cleanup.
