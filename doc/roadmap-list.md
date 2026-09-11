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

What remains between fango and the slice baseline is mostly not list work.
[roadmap-calls.md](roadmap-calls.md) records where it goes: an allocation per
element at curried higher-order boundaries, and a Go frame per element in
recursion that builds a list. The part that *is* list work — walking a chunk's
array rather than stepping a list value per element — is worth having for the
operations that have no callback to hide it behind; see the native combinators
below.

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
- **Chunk-aware native combinators**, which the representation makes possible
  and which `runtime/fangort/list_cost_test.go` measures. They split three
  ways rather than being one decision. `map`, `filter`, and `foldr` gain
  four-fold, entirely from replacing recursion with one forward pass rather
  than from faster traversal. `each` and `foldl` gain nothing: they are already
  loops, and once a per-element callback sits in the loop the traversal method
  stops mattering — their cost is the callback, which
  [roadmap-calls.md](roadmap-calls.md) owns. Callback-free operations —
  `length`, `reverse`, `==`, and future `append`, `take`, and indexing — get
  the full traversal win, `length` much more if it totals each chunk's
  occupancy instead of visiting elements. The standard-library rule in
  [the roadmap](roadmap.md) asks for benchmark evidence before a native; this
  is that evidence, and it says which ones.
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
