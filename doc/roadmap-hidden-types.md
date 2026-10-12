# Hidden state types

[Roadmap index](roadmap.md).

[Constructor-local hidden types](reference/types.md#hidden-state-types),
[typed storage workers](design/hidden-types.md#typed-storage-workers), and the
Stream/Iterator migration are implemented. Scoped library runners now use
[row extensions](reference/functions.md#scoped-callbacks), including closed
outer rows. Legacy pragmas remain accepted for compatibility.

## Surviving dynamic packages

The checked fallback supports packages whose storage cannot be propagated by
representation inference. Future optimization can specialize consumers at
surviving opaque boundaries, including large or separately compiled factories
that choose incompatible witnesses, hidden sum constructors, arbitrary stored
polymorphic callbacks, and mutually recursive storage shapes. Current bounded
branch distribution does not cover every such program.

Generate consumer specialization from user code, without recognizing Stream
operations by name. Dispatch outside the traversal loop and preserve strict
construction, invocation evidence, exits, descriptors, and immutable iterator
aliases. Keep module ownership and cache dependencies explicit. Retain the
occurs-check/finite-growth termination rule; recursively changing storage must
not create unbounded Go instantiations. A copied handler needs fresh activation
and resumption identities before a worker containing it can be duplicated.

Measure each expanded boundary against an equivalent typed implementation with
the [compiler harness](design/hidden-state-performance.md#library-migration).
Passing the current matrix does not establish zero overhead for every possible
existential program. Keep checked fallback support and existing performance
thresholds while extending coverage.
