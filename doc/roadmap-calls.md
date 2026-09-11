# fango calling conventions and recursion shapes

This document owns two unstarted pieces of work on how calls and recursion
compile. Both came out of measuring where the bundled `List` still loses to
handwritten Go after it became array-backed
([roadmap-list.md](roadmap-list.md)), but neither is about lists: they are
general properties of higher-order calls and of non-tail recursion, and each
fixes user code and the standard library at the same time.

Implemented architecture belongs in [the design](design.md); the self
tail-call loop both backends already perform is described there and in
[the reference](reference.md).

## What the measurements say

Building and summing a hundred-thousand element list of `Int`, one iteration,
against the same work on a Go slice:

| | time | allocations |
|---|---|---|
| Go slice, loops | 87us | 1 |
| chunked list, loops, plain callback | 256us | 3125 |
| chunked list, loops, **curried** callback | 958us | **103125** |
| chunked list, **non-tail recursion** | 1542us | 3125 |

Two costs dominate, and neither is a list operation. Currying adds one
allocation per element at a higher-order boundary. Non-tail recursion adds one
Go frame per element. Allocation volume is identical between the loop and
recursion rows, so the recursion cost is frames rather than memory.

A third result decides how to read these. `List.each` and `List.foldl` already
compile to loops, because they are self-tail-recursive; `List.filter` gets its
else branch. `List.map` and `List.foldr` do not. Yet a `foldl`-based sum runs
*slower* than a hand-written non-tail-recursive one, because `foldl` pays the
curried callback on every element while the recursive version pays nothing at a
higher-order boundary at all.

Native, chunk-aware implementations of the combinators are worth having, but
not for the reason they first appear to be, and not for all of them.
`runtime/fangort/list_cost_test.go` measures which costs chunk-awareness can
actually remove:

| operation | accessors or recursion | chunk-aware |
|---|---|---|
| traversal, no callback | 38us | 26us |
| fold, plain opaque callback | 90us | 99us |
| fold, curried callback | 766us | 755us |
| `map` | 811us | 199us |

Walking a chunk's array directly recovers most of the traversal gap. But once a
per-element callback the compiler cannot see through sits in the loop, the
traversal method stops mattering — the nested loop is in fact slightly worse,
because more state stays live across the call. So a native `each` or `foldl`
buys nothing: they are already loops, and their cost is the callback.

`map` is the opposite case. It gains four-fold, and none of that is traversal:
it is the recursion. A chunk-aware `map` mirrors each source chunk into a fresh
one, filling it head-to-tail so the callback still runs in element order, and
links forward — one pass, no recursion, no intermediate buffer, and the same
chunk count as the source. `filter` and `foldr` have the same shape.

That is the same win C2 below delivers, by a narrower and much cheaper route.
The difference is reach: a native fixes the library, while C2 fixes every
user-written function of the same shape — and the benchmark furthest from Go
calls no library function at all, only user-written `build` and `sum`. Landing
natives first is defensible on cost; it does not remove the reason for C2.

Callback-free operations are the third case, and the clearest one. `length`,
`reverse`, `==`, and future `append`, `take`, and indexing have no callback for
the traversal cost to hide behind, so they get the full traversal win —
`length` considerably more than that, since it can total each chunk's occupancy
instead of visiting elements at all.

## C1 — Uncurried callbacks at worker boundaries

A function value of type `a -> b -> c` is emitted as `func(A) func(B) C`, so a
worker that always applies such a parameter to both arguments still evaluates
`combine(value)` into a closure before it can apply it. Inside a generated
worker the callback is a parameter of unknown identity, so Go can neither
inline it nor stack-allocate what it returns: the closure is a real allocation,
once per element.

The fix is to pass those callbacks flat. The decision belongs to a *worker
parameter*, not to a type: the same function type stays curried everywhere
else.

**Eligibility**, computed from one `Def`:

- The parameter's type has at least two arrows.
- All but the last arrow are pure and Direct — empty effect row, no control —
  so applying the leading arguments can neither perform nor exit. This is the
  fact the design already states about syntactic multi-parameter functions:
  the body runs only after the final parameter. The last arrow may carry
  anything.
- Every occurrence of the parameter in the body is the base callee of exactly
  that many nested `App{CalleeKind: Value}` nodes, and the intermediate
  applications carry no evidence arguments. Any other occurrence — passed on to
  another worker, returned, stored in a constructor or a dictionary —
  disqualifies the parameter outright. Conservative and cheap to check.
- The parameter name is not shadowed by an inner binder.

**Where the decision lives.** A shared predicate in `internal/core`, a pure
function of the definition, exactly as `DetectTailLoop` is today. Callers hold
the whole program and already index every definition, so a call site recomputes
the callee's answer instead of reading a new Core field, and the two cannot
disagree. Core gains no node and no field, and the interpreter needs no change
at all: it applies one argument at a time and never sees a Go type.

**Emission.** The parameter's Go type becomes `func(A0, ..., Ak) R`, where `R`
is the last arrow's result under the current transport, so the Exit family
member returns an `Outcome` exactly as it does now. An application chain whose
base names an uncurried parameter emits as one call. At call sites each
argument is adapted: a literal lambda nest of sufficient depth emits directly
as a flat Go function literal, which is the case that pays and the common one;
anything else gets a wrapper that calls it curried, costing what today costs.
Partial applications of the worker reach the same call path, so their eta
wrappers adapt too.

**What it deliberately does not change.** The representation of a function
*value*. ADT fields, class-dictionary methods, and effect evidence keep the
curried form; only worker parameters gain the flat one, with the adapter
bridging. Widening it further would multiply the existing Direct/Exit
representation families by an arity dimension.

**The property to be careful about.** This is the first place a Go
representation depends on something other than the fango type, so the design's
"representations are type-directed" claim needs amending rather than quietly
breaking. Keeping the predicate shared is what stops codegen disagreeing with
itself across module boundaries.

**Gates.** The differential suite; a codegen unit test pinning both the flat
signature and the wrapper form; a `foldl`-heavy runtime-ratio case, since the
existing list cases exercise the callback boundary only incidentally.

Expected payoff: the curried fold row collapsing toward the plain one — the
allocations are most of that difference — and one fewer allocation per element
at every higher-order call in any program. This is the only one of the three
directions that reaches the cost `each` and `foldl` actually pay.

## C2 — Loops for list-building recursion

`map`, `filter`, `foldr`, and every user-written `build`-shaped function recur
once per element, so a long list costs a long Go stack. The recursion is not
tail recursion and the existing loop rewrite does not apply, but it is tail
recursion *modulo a constructor*: the recursive call is the last argument of a
`Cons`.

**Shape.** A self-recursive worker where every tail-skeleton leaf is either
non-recursive or a `List.Cons` application whose last argument is a self call
at the identity instantiation with unchanged evidence. Because Core evaluates
constructor arguments left to right and the recursive call is last, everything
the iteration must do has already happened when the recursion is reached — so a
forward loop preserves evaluation order and effect order exactly, which is the
whole soundness argument.

**Transform.** A loop that appends each head in turn and finishes with the
base case's value.

**Runtime support.** A forward builder needs no intermediate buffer, which the
`map` prototype demonstrates: allocation is identical to the recursive version.
Chunks fill downward, because that is what makes a cons worst-case constant
time, so a builder that does not know the length fills each chunk from index
zero and, at the end, shifts the final partial chunk to the high end and sets
its watermark — one move of at most a chunk's worth of elements per list.
Chunks under construction are unpublished, so backpatching each one's
successor as the next is allocated is sound.

**Scope.** Restricted to `List.Cons`. A general version needs a mutable hole in
the value under construction, which an immutable chunk spine does not offer;
other constructors keep recursing.

**Interaction.** `filter`'s else branch is already a self tail call, so one
definition can need both rewrites in a single loop body. The two eligibility
predicates have to compose rather than exclude each other.

**Exclusions.** A recursive call under a lambda or a handler, a different type
instantiation, changed evidence, or any leaf where reordering would move an
effect.

**Backends.** The design's rule is that both backends share the tail-call
predicate so they optimize the same set of definitions, with the differential
suite as referee. The same argument applies here, and the interpreter should
either implement the loop or the predicate should say so explicitly.

**Gates.** Differential fixtures for effect order in `map` with an effectful
callback, `filter` exercising both branches, and lists long enough to span many
chunks; the runtime-ratio gate; and the `branchcons` case, which must not
regress.

## A measurement caveat that affects both

The runtime-ratio gate times whole processes. For the list cases most of the
baseline's wall clock is process spawn and collection rather than the work
being compared, so the published ratios understate the difference in compute —
in-process the same programs differ by considerably more than the gate reports.
That is not a reason to distrust the direction of the gate, but it is a reason
not to treat its numbers as the target for either item here. Reproducibility of
both performance gates is already open in
[the roadmap](roadmap.md#product-polish).
