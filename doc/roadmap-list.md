# List representation

[Immutable cons storage](design/backend.md#list-representation) is implemented.
The [simplification direction](roadmap-simplification.md) replaces shared mutable
chunk frontiers. Historical measurements remain in Git; they are not measurements
of the current representation.

## Private bulk construction

Measure private bulk builders and contiguous storage for bulk operations without
mutating published values. Keep worst-case constant cons/head/tail, persistent
branching, structural equality, and allocation-free traversal. Do not add locks
or atomics to ordinary list construction to recover chunk reuse.

## Library operations

Length/indexing exposure and native bulk combinators still await concrete
consumers. Callback and recursion costs belong to [calling conventions](roadmap-calls.md).
Compare allocations and runtime on an idle host before accepting optimizations.
