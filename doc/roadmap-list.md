# Roadmap: list representation

Open representation/API choices. The implemented persistence invariant lives in
[List design](design/backend.md#list-representation), public complexity in
[collections](reference/library-collections.md#list), and comparative callback/
recursion measurements in [calls](roadmap-calls.md#what-the-measurements-say).

## Open questions

- **Retune listChunk.** It is 32 because bytes/element saturates for scalar and
  two-word elements; 64 doubles short-list/branching waste. Validate with branchcons
  rather than relying only on sizes. Branching compares against cons cells.
- **Geometric growth.** External arrays add a slice header/allocation per chunk.
  A hybrid could retain small inline arrays for fresh/branching chunks and grow
  linear spines, at the cost of two storage modes. Measure actual workloads first.
- **Length and indexing.** Decide whether representation-aware operations should
  become public APIs; length currently traverses elements.
- **Chunk-aware combinators.** Callback-free length/reverse/equality and future
  append/take/indexing can benefit from traversal directly; length could total
  chunk occupancy. each/foldl are dominated by callbacks, addressed by C1 in the
  calls roadmap. map/filter/foldr could avoid their intermediate list/reversal;
  C2 offers a general compiler alternative.
- **Other compiler-known representations.** Require a concrete measured benefit
  before making another bundled type special.

## What a native map would need

The ListMap prototype in [runtime cost tests](../runtime/fangort/list_cost_test.go)
uses one forward chunk pass. Earlier measurements found about 2.4x speed and half
the allocation versus accumulate/reverse. This is evidence for an experiment,
not a portable threshold or a current benchmark run.

Three boundary changes remain:

- Native matching must reconcile callback row variables with erased call-site rows;
  matchNativeType currently requires matching tail presence.
- Native execution needs checked Fango callback invocation and exit propagation,
  which the scalar sidecar/interpreter native ABI does not provide.
- Lowering must handle Direct, Exit, and Machine callable members and their distinct
  completion protocols; one scalar Go-expression template cannot encode all three.

Keep callback order, resource/capture checks, and consumer-independent emission.
Implementing library natives would not replace C2's benefit to user-written recursion.
