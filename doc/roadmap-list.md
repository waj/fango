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

What remains between fango and the slice baseline is not list work.
[roadmap-calls.md](roadmap-calls.md) records where it actually goes: an
allocation per element at curried higher-order boundaries, and a Go frame per
element in recursion that builds a list.

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
- **Native acceleration for the combinators — measured, and not the lever.**
  The obvious next step is native `map` and `each`, and the numbers say no.
  `each` and `foldl` already compile to loops, so a native version removes
  nothing, and it would still receive a curried fango closure and pay an
  allocation per element for it; a `foldl`-based sum is accordingly *slower*
  than a hand-written non-tail-recursive one. A native `map` or `foldr` would
  remove real Go frames, but only inside the library, while the benchmark
  furthest from Go calls no library function at all. The two costs that
  actually dominate are general, and they are owned by
  [roadmap-calls.md](roadmap-calls.md). Revisit natives only if that work lands
  and a gap remains — the standard-library rule in [the roadmap](roadmap.md)
  asks for benchmark evidence, and the evidence currently points elsewhere.
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
