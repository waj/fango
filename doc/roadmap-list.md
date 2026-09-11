# fango list representation

The bundled `List` has an array-backed runtime representation behind an
unchanged linked-list surface. The architecture and its invariants are in
[the design](design.md) ("Go backend and runtime", "Interpreter and REPL"), and
the library surface and the complexity callers may rely on are in
[the reference](reference.md). This document owns what is still open.

## What it bought

Measured on identical programs, the same benchmark sources compiled by the
cons-cell representation and by this one:

| case | cons cells | chunked | change |
|---|---|---|---|
| `sum` | 7.80x | 4.49x | 1.84x faster |
| `mapfilter` | 5.82x | 4.52x | 1.65x faster |
| `branchcons` | 2.19x | 1.55x | fango time unchanged |
| `tree` (control) | 1.07x | 1.09x | unchanged |

Linear building is where it wins; branching is a wash, which is what a
worst-case-O(1) cons was chosen for. `branchcons` races handwritten cons cells
rather than a slice, because that is the workload where an array-backed list is
the one that has to pay.

## Open questions

- **Retuning `listChunk`.** It is 32 because bytes per element saturates there
  for both scalar and two-word elements, so 64 would double the waste a short
  list and a branching cons each pay in full without buying anything back. That
  reasoning is about sizes, not about measurement; `branchcons` is the gate that
  should now settle it.
- **Whether chunks should grow geometrically along a spine** rather than being a
  fixed size. Growth needs a slice instead of an inline array, costing a header
  and a second allocation per chunk, and pays only if short lists and branching
  turn out to dominate real programs. A hybrid — a small inline array for fresh
  and branching chunks, a larger external one once a frontier owner exhausts
  its chunk — would recover most of the linear advantage while keeping the
  worst case, at the price of two storage modes.
- **Whether to expose cheap length and indexing.** Both are available from the
  representation and neither is expressible on the current surface; `length` is
  a traversal today. This is a surface question, not a representation one.
- **Native acceleration for the combinators.** `map`, `filter`, and `foldl` are
  ordinary fango. An inline `native` template over a runtime helper is available
  now and would need a matching interpreter registry entry; carrying a list
  across the sidecar ABI is a larger question that belongs with the FFI work.
  Both presuppose the shared representation, which is why neither was worth
  doing before it existed. Neither should happen without benchmark evidence,
  per the standard-library rule in [the roadmap](roadmap.md).
- **Whether any other bundled type deserves a compiler-known representation**,
  and what the criterion is. `List` earned it on a measurement; that is the bar.

## A bug this work surfaced

Passing a list of effectful functions to a higher-order worker type-checks and
then fails Core lint, with the argument's row disagreeing with the parameter's.
It reproduces identically before this work, so it is not a representation
problem; it belongs with
[effect-row subsumption](roadmap.md#effect-row-subsumption-for-higher-order-arguments).
`testdata/run/list_of_functions.fango` is written around it, annotating every
element into one closed row.
