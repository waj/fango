# fango list representation

This document owns the work of giving the bundled `List` an array-backed
runtime representation while keeping its linked-list surface. Implemented
architecture belongs in [the design](design.md) and user-visible behavior in
[the reference](reference.md); results are promoted there as milestones land
and removed from here.

## The problem

`List` is an ordinary ADT declared in `stdlib/List.fango`:

```fango
type List a = Nil | Cons a (List a) deriving (Eq, Ord)
```

It lowers through the generic ADT path: a Go marker interface plus one
heap-allocated constructor struct per element, discriminated by a type switch.
That is one allocation and one pointer chase per cell, and it is what the
design means by "cons-list allocation remains the main known structural
performance cost".

The goal is to change what the backends *represent* a list as, and nothing
else. The surface stays exactly as documented: `Nil`/`Cons`, bracket literals
and patterns, exhaustiveness, `deriving (Eq, Ord)`, the handwritten `Show`, and
every existing line of fango. This is the treatment `Bool` already gets — an
ordinary ADT to the checker with a native representation in the backends —
applied to a stdlib-declared parameterized recursive type.

Only the bundled `List` is affected. A user-declared
`type List a = Nil | Cons a (List a)` is a distinct nominal type with its own
identity and keeps the ordinary cons lowering.

## Representation: a chunked (unrolled) list

Elements live in fixed-size inline arrays filled downward, with a per-chunk
watermark recording how far the chunk has been filled.

```go
const listChunk = 32

type chunk[T any] struct {
	lo    int          // slots [lo, listChunk) are written and frozen
	elems [listChunk]T // inline: one allocation, no slice header
	next  List[T]      // what follows elems[listChunk-1]
}

type List[T any] struct {
	node *chunk[T] // nil is Nil — the zero value
	off  int       // this value's window starts at node.elems[off]
}
```

`Cons` claims the slot below the watermark when the value it is extending owns
the frontier, and otherwise starts a fresh chunk whose `next` is that value.
`Tail` advances the offset within the chunk, or follows `next` at the boundary.

### Why it is persistent

Every value satisfies `off >= node.lo` at creation: a cons sets `off = lo`
exactly, a tail sets `off+1 > off >= lo`, and a `next` field was itself a value
created earlier. Because `lo` only ever decreases, `off >= node.lo` holds
forever. When a cons writes slot `i = lo-1`, every value `w` already existing on
that node had `w.off >= lo = i+1 > i`, so `elems[i]` lies strictly below `w`'s
window and is unreachable from it. Only the value the cons just returned can
read it.

The frontier test is also exact rather than conservative: when `off > node.lo`,
slot `off-1` is already published and some other value may hold it, so the
copy-free path must be refused — and is. It is a dynamic linearity check costing
two comparisons, doing at run time what a uniqueness analysis would attempt
statically.

Consequences: `Cons` is O(1) worst case and never copies; a branch (consing
twice onto one value) costs one chunk rather than a copy, so there is no
asymptotic cliff; `Tail` is O(1); and a long-lived tail over-retains at most
`listChunk-1` slots plus a header, independent of list length. Because `next`
points toward the end of the list, holding a deep tail does not pin the prefix.

### Why `listChunk` is 32

A chunk is `24 + K*sizeof(T)` bytes, rounded to a Go size class. For `Int`
elements:

| K | chunk bytes (class) | bytes/elem | one-element list vs cons cell | break-even |
|---|---|---|---|---|
| 8 | 88 -> 96 | 12.0 | 4x | ~4 elements |
| 16 | 152 -> 160 | 10.0 | 6.7x | ~7 |
| 32 | 280 -> 288 | 9.0 | 12x | ~12 |
| 64 | 536 -> 576 | 9.0 | 24x | ~24 |

Bytes per element saturates at 32; going to 64 buys nothing there while doubling
the worst case. Above 32 the only remaining gain is allocations per element,
against a cons cell's one-per-element either way. The same knee appears at 32
for two-word elements. The waste is paid in full by short lists, which are
common, and by every branching cons. 32 is also the branching factor Clojure's
`PersistentVector` and Scala's `Vector` settled on for the same trade.

It is one constant, and the branching benchmark below is what should retune it.

### Iteration does not allocate in compiled Go

`List` is a value struct, so `Tail` produces two locals rather than an object.
Measured over a full traversal of a ten-thousand element list of `Int`:

| | time | allocations |
|---|---|---|
| compiled Go, chunked | 3.9us | 0 |
| compiled Go, cons cells | 7.7us | 0 |

Generated code stores lists in typed fields, never in `any`, so nothing boxes.
Traversal is faster than cons cells because the elements are contiguous and
there is no per-step interface type assertion.

The interpreter is different, because its uniform value type is `any` and a
two-word struct does not fit in an interface word — where today's tail is
already a boxed constructor pointer. It therefore pays one small box per `Tail`.
Measured in the interpreter's actual per-step shape, which already allocates a
frame and a variable map for every match, that is about a quarter more
allocations, four percent more bytes, and one percent more time. Avoiding it
would require a pointer-shaped list value, and every such scheme needs a
per-offset view object costing `16*listChunk` extra bytes per chunk — worse per
element than cons cells. The lever for interpreter allocation is its per-step
frame overhead, which would help everything rather than lists alone.

### Rejected: a flat slice with copy-on-branch

Storing the whole list in one reversed, append-only slice with an index gives
the best traversal locality and degenerates into exactly the slice baseline the
runtime benchmarks measure against, so it would post the best numbers on the
list cases. It is rejected because consing onto a shared tail is O(n) in time
and bytes every time, and because a tail pins the entire backing array, so a
short tail of a long list retains the long list. Both are triggered by the
shape of the backtracking solver and the interpreter on the
[examples pipeline](roadmap-examples.md), and the reference currently promises
that an existing tail is shared. Optimizing the benchmark into a quadratic
cliff on the roadmap is the wrong trade.

Both schemes are observationally identical to cons cells: values are immutable,
so sharing is not observable.

## Runtime API

`runtime/fangort` is the home, because it is the one package both the
interpreter and every generated package already import, and because a list type
must be nameable in the signature of every generated worker that mentions one.
The scalar, out-of-process sidecar ABI cannot carry a polymorphic container, and
sidecar packages are leaves of the generated dependency graph.

```go
func  ListNil[T any]() List[T]
func  ListCons[T any](head T, tail List[T]) List[T]
func (l List[T]) IsEmpty() bool
func (l List[T]) Head() T
func (l List[T]) Tail() List[T]
func  ListEq[T any](eq func(T, T) bool, a, b List[T]) bool
func  ListShow[T any](show func(T, bool) string, v List[T], nested bool) string
```

Invariants the backends rely on:

- The zero value is `Nil`, so no emission path has to initialize a list local.
- `List` is deliberately not comparable with Go `==`, so an accidental identity
  comparison is a compile error rather than a silently wrong answer.
- `ListEq` has no physical-equality short circuit. Comparing representations
  first would be observable: a list holding a NaN is not equal to itself.
- `ListShow` reproduces the *structural* derived spelling, which the interpreter
  mirrors byte for byte. The bracket display users see is the handwritten `Show`
  instance in `stdlib/List.fango` and is unaffected.
- The watermark is a benign non-atomic mutation only because evaluation is
  single-threaded. Making the frontier claim a compare-and-swap is what the
  design would need under concurrency.

## Compiler integration

Recognition happens once, at declaration, in `internal/infer`: the bundled
`List` type and its two constructors are matched by canonical symbol, their
shape is validated, and a representation discriminator is recorded on the
declared type and constructor information. A shape mismatch is an internal error
rather than a silent fallback — the detectable form of the lockstep invariant
that [unembedding the bundled sources](roadmap.md#unembedding-the-bundled-sources)
will need. Because the discriminator rides on structures both backends already
hold, neither needs new plumbing, and a locally declared list type is simply
never marked.

Core is untouched: constructors, patterns, decision trees, the linter's
constructor rules, capture analysis, reflection, and the deriver all continue to
see an ordinary parameterized ADT. The Go backend stops emitting a marker
interface and constructor structs for the type, maps it to the runtime type,
emits construction as runtime calls, compiles its decision-tree node to an
emptiness test with head and tail projections rather than a type switch, and
delegates its derived equality and display to the runtime. The interpreter makes
the matching changes to construction, matching, structural equality, and
display.

The type needs no `Exit` representation family of its own: its constructor
fields are a bare type variable and the recursive occurrence, so the
Direct/Exit distinction is carried entirely by the element type argument, which
one generic runtime type absorbs.

## Milestones

- **L1 — runtime structure.** The runtime list and its tests, with the sharing
  invariant exercised directly: linear building, branching, chunk-boundary
  tails, structural equality including NaN, and the empty and single-element
  edges.
- **L2 — recognition.** The representation discriminator and its shape
  validation, read by nothing yet.
- **L3 — Go backend.** Type mapping, construction, the decision-tree node, and
  the derived equality and display delegations. Necessarily one step: a
  partially switched backend does not compile.
- **L4 — interpreter.** The same representation on the interpreter side. Kept
  separate so the differential suite runs once with a single leg switched, and
  so the structural-equality path is independently bisectable.
- **L5 — library and benchmarks.** `List` gains the combinators the benchmarks
  need, and the runtime benchmarks are rewritten to call them instead of
  declaring private cons types, which is the only way those gates can measure
  this work at all. A branching case is added, since nothing currently gates the
  one dimension where chunking is worse than a cons cell, and the ratio ceilings
  are re-recorded in the same change so the gates keep arbitrating.

## Open questions

- Retuning `listChunk` once the branching benchmark exists.
- Whether fixed-size chunks are right at all, or whether chunk sizes should grow
  geometrically along a spine. Growth needs a slice rather than an inline array,
  costing a header and a second allocation per chunk, and it is only worth it if
  short lists and branching turn out to dominate.
- Whether the representation should be exposed as cheap length and indexing.
  Both are available from it, and neither is expressible on the current surface.
- The native-acceleration path for the combinators. Inline native templates over
  runtime helpers are available now and would need matching interpreter entries.
  Carrying lists across the sidecar ABI is a larger question that belongs with
  the FFI work, and both presuppose the shared representation this document
  introduces.
- Whether any other bundled type deserves a compiler-known representation, and
  what the criterion is.
