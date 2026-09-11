# fango list representation

The bundled `List` has an array-backed runtime representation behind an
unchanged linked-list surface. The architecture and its invariants are in
[the design](design.md) ("Go backend and runtime", "Interpreter and REPL"), and
the library surface and the complexity callers may rely on are in
[the reference](reference.md). This document owns what is still open.

## What it bought

Identical benchmark sources, compiled before and after, timed as whole
processes and reported as the minimum of repeated runs. "Before" is the
cons-cell representation with the combinators written the direct recursive way;
"after" is the array-backed representation with them written to accumulate and
reverse.

| case | before | after | change |
|---|---|---|---|
| `sum` | 58ms | 33ms | 1.8x faster |
| `mapfilter` | 51ms | 28ms | 1.8x faster |
| `branchcons` | 17.8ms | 17.8ms | unchanged |

`sum` calls no library function — its `build` and `sum` are both user code — so
its entire gain is the representation. `mapfilter` splits about 1.45x from the
representation and 1.27x from writing the combinators as loops. `branchcons` is
the guarantee holding: the workload where an array-backed list could have lost
is exactly level.

Whole-process timing understates all of this, because several milliseconds of
spawn and collection are common to both legs; see the note at the end of
[roadmap-calls.md](roadmap-calls.md).

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

## What a native `map` would need

`fangort.ListMap` exists and is measured: one forward pass over the source
chunks, 2.4 times faster than the accumulate-and-reverse implementation and
half its allocation. It is not yet reachable from fango. Wiring it up runs into
three separate gaps, none of them about lists:

- **Core lint rejects the declaration.** `matchNativeType` requires a
  declaration's effect row and the call site's to have tails alike, and a
  higher-order native's callback row is a variable that elaboration erases. The
  rule was written when every native was a closed scalar signature. It needs to
  bind a row variable the way it already binds a type variable.
- **A native cannot call a fango function.** No native takes one today, and the
  interpreter's native runtime has no way to apply a closure, nor to propagate
  an exit raised inside one back out through the native boundary.
- **A single template cannot serve both ABI families.** An effect-polymorphic
  worker has Direct and Exit members; the Exit member's callback returns an
  `Outcome` and the result is an `Outcome`. One Go expression string cannot
  spell both, so native lowering would have to become transport-aware, or a
  native would need one template per family.

Together these are the concrete content of extending the native ABI beyond
scalars, which the [roadmap](roadmap.md) holds open. The payoff is known and
the list side of it is already written; what is missing is the mechanism.
