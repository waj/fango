# Synchronous foundation and native tasks

The synchronous foundation, explicit streams, immutable Lists, IO references,
and restricted native tasks are implemented. Their contracts live in
[effect execution](design/effects.md), [task architecture](design/tasks.md),
[streams](reference/library-streams.md), and [tasks](reference/library-tasks.md).
The old coroutine and Async roadmap stage identities remain deferred.

## Remaining work

- Measure immutable cons allocation and task overhead on an idle host using the
  retained historical comparisons. Do not relax timing thresholds to manufacture
  parity; document any accepted allocation tradeoff.
- Improve private bulk List construction only when measurements justify it,
  preserving immutable published nodes. See [Lists](roadmap-list.md).
- Add task combinators when concrete applications need them. Keep workers named
  and inputs/results transferable; avoid a compiler primitive for each API.
- Decide socket handoff and cancellation-aware IO contracts before enabling
  concurrent servers with shared or transferred handles.

Generator syntax, channels, shared handles, STM, alternate executors, and general
native background callbacks remain deferred. Recursive producers can use callback
traversal; ordinary pull iteration uses explicit state. Expand common native ABI
capabilities when needed rather than recognizing library operation names.
