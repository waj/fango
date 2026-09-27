# List representation

[Immutable cons storage](design/backend.md#list-representation) is implemented.
Historical measurements remain in Git; they are not measurements of the current
representation or of the candidate atomic chunk frontier.

## Private bulk construction

Measure private bulk builders and contiguous storage for bulk operations without
mutating published values. Keep worst-case constant cons/head/tail, persistent
branching, structural equality, and allocation-free traversal. An atomic-frontier chunk implementation remains a candidate, subject to this
acceptance gate:

- Use otherwise identical builds on an idle host, in two alternating rounds,
  with at least 15 samples per primary workload in each round.
- Pass persistence, branching, and concurrent correctness/race tests.
- Reduce both allocation count and allocated bytes for linear construction by
  at least 25%.
- In each round and every primary workload, the upper 95% confidence bound of
  the chunk/cons runtime ratio must be at most 1.05. Include branching, not only
  linear construction and mapping.
- Pass existing performance gates without changing their baselines.

Retain cons storage if chunks fail a gate. Rerun inconclusive measurements on an
idle host; do not select a representation from loaded-host timing. This
comparison has not been completed.

## Library operations

Length/indexing exposure and native bulk combinators still await concrete
consumers. Callback and recursion costs belong to [calling conventions](roadmap-calls.md).
Compare allocations and runtime on an idle host before accepting optimizations.
