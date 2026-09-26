# Native tasks and explicit streams

[Design index](../design.md). [Task semantics](../reference/library-tasks.md)
and [stream semantics](../reference/library-streams.md) own the public contract.

## Concurrent invocation boundary

`TaskSpawn` is the only concurrent Core invocation node. It names a closed
top-level worker and carries explicit input and result types. Elaboration and
Core lint check transferable data structurally; they do not analyze closure
bodies or infer sharing/retention graphs. Class dictionaries and residual
handler evidence do not cross the boundary. Workers establish their own effects.

Generated Go evaluates scope and input in source order, then starts a goroutine
calling the worker. The interpreter uses the same task runtime and a fresh
invocation environment with no inherited handler evidence. Installed definitions
and immutable data may be shared; lazy global initialization retains its existing
synchronization. Task completion publishes the result before closing a channel.
The channel provides synchronization for repeated waits and result reads.

The Task module implements structured scopes using `Runtime.Scope.bracket`.
The runtime protects the scope's child registry with a mutex, cancels children
on close, and joins every child. Context cancellation is explicit and cooperative.
No coroutine dispatcher, transformed stack frame, or compiler poll is involved.
Only `Task.spawn` needs a compiler invocation boundary; scope bookkeeping,
waiting, cancellation, and timers are ordinary native sidecars.

## Library state and traversal

`Runtime.Ref` is an ordinary native module for IO-marked mutable storage. Its
sealed type index ensures a reference cannot be read at a different type.
Reader and Writer capture task-local references. Such references are not
transferable task data. Ordinary reference operations need no compiler cases.

Stream and Iterator are Fango records and ordinary recursive functions. State
is explicit in each step's return value. No compiler node recognizes streams,
iterators, map, filter, take, zip, or fold. Resource ownership belongs to the
scope performing the traversal, so there is no suspended-stack cleanup protocol.
