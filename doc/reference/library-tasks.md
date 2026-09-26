# Tasks

[Reference index](../reference.md). Source: [Task](../../stdlib/Task.fango).

## Spawning

`Task.scope use` creates a scope and runs `use scope`. `Task.spawn scope worker
input` starts a Go-backed task. The worker must be a named top-level function
with context and input parameters, with no class constraints:

```fango
import Task

worker : Task.Context -> Int ->{IO} Int
worker context number =
    ignore (Task.sleep context 1)
    number * 2

main() = Task.scope (\scope ->
    task = Task.spawn scope worker 21
    print (Task.await task))
```

Spawn must be fully applied. Capturing closures, local functions, partial
applications, and native workers are rejected with `TASK BOUNDARY`.
Input and output must be concrete immutable data: scalars, strings, Bytes,
and algebraic data whose fields are themselves transferable. Functions,
unresolved type variables, native handles, references, and task/scope/context
handles cannot cross the boundary, including when wrapped in another type.
Each worker establishes its own handlers and opens its own resources.

## Results and scope cleanup

`Task.await task` has effect `IO` and returns `Result Task.Error a`. Repeated
awaits observe the same terminal result. Application failures belong in the
worker's result type; the runtime errors are `Cancelled`, `Panicked String`,
and `ClosedScope`. A worker's host panic is captured as `Panicked`; a host panic
is not a language abort and does not promise language bracket cleanup.

A scope joins all children before normal return. On a language abort, bracket
cleanup requests cancellation of every child and then joins them. It does not
implicitly turn an unobserved child failure into a parent failure. Handles may
be retained after scope exit to inspect completed results. A retained closed
scope rejects new spawning with a task whose outcome is `ClosedScope`.

## Cancellation

`Task.cancel task` requests cancellation. Workers cooperate through
`Task.isCancelled context`, `Task.sleep context milliseconds`, and
`Task.awaitIn context task`; all have effect `IO`. Sleep returns `False` on
cancellation. `awaitIn` returns `Err Cancelled` when its caller is cancelled;
it does not cancel the awaited task. An already completed result remains
observable. A worker that returns after observing cancellation has a cancelled
outcome, and its return value is discarded.

There are no compiler-inserted cancellation polls. Ordinary blocking native IO
is not interrupted by task cancellation. A worker that never returns or checks
cancellation can keep its scope open. Nested scopes create their own task
contexts; cancellation does not automatically propagate into a nested scope.
