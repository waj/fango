# Synchronous foundation and native tasks

The synchronous foundation, explicit streams, immutable Lists, IO references,
and closure-based native Async tasks are implemented. Their contracts live in
[effect execution](design/effects.md), [task architecture](design/tasks.md),
[streams](reference/library-streams.md), and [Async](reference/library-async.md).
The old coroutine and Async roadmap stage identities remain deferred.

## Remaining work

- [Scoped readers and effect-polymorphic tasks](roadmap-scoped-effects.md):
  a pure eager test runner remains deferred. Scoped readers, generic task
  packaging, handler inheritance, and cooperative host interruption are implemented.

- Measure immutable cons allocation and task overhead on an idle host using the
  retained historical comparisons. Do not relax timing thresholds to manufacture
  parity; document any accepted allocation tradeoff.
- Improve private bulk List construction only when measurements justify it,
  preserving immutable published nodes. See [Lists](roadmap-list.md).
- Add task combinators when concrete applications need them, using the existing
  closure invocation boundary rather than a compiler primitive for each API.
- Define cancellation-aware blocking IO contracts for concurrent servers.
  Sharing native handles is supported; cancellation does not implicitly interrupt
  arbitrary blocking IO.

Generator syntax, STM, alternate executors, and general native background
callbacks remain deferred. Recursive producers can use callback
traversal; ordinary pull iteration uses explicit state. Expand common native ABI
capabilities when needed rather than recognizing library operation names.
