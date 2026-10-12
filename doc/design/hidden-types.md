# Hidden constructor types

[Design index](../design.md). [Source rules](../reference/types.md#hidden-state-types).

## Inference and Core proofs

`CtorInfo.Hidden` lists ordinary constructor-local binders in first-use order.
They occur in the field schema, never in the public nominal result. Construction
instantiates them with fresh metavariables, shared across the fields. Pattern
opening instantiates them with fresh rigid variables. Each pattern boundary
checks the solved enclosing result, residual effects, and outer environment for
escape; repacking consumes an abstract type without exposing it in the result.

Core constructor applications retain their concrete `Witnesses` independently
of public `TyArgs`. Each constructor case declares fresh `Hidden` binders scoped
to its field bindings and subtree. The linter checks declaration ownership,
witness and binder counts, substituted field representations, and rigid-variable
scope independently of inference. Record updates of independent fields open and
repack the same witnesses. Ordered pattern lowering preserves separate abstract
identities in different source equations, including nested and inferred patterns.

## Typed storage workers

After source checking and bounded inlining, representation inference retains a
hidden state as an internal type parameter. Each eligible single-constructor
ADT has a compiler-only ordinary ADT with its public parameters followed by its
hidden storage parameters. Construction fixes those parameters; opening relates
field storage to them without changing the source checker's abstract identities.
Go therefore receives an ordinary generic struct and typed step function.

A separate constraint trial follows fields, callbacks, constructor applications,
recursive calls, and worker inputs and results. It solves storage equality,
generalizes remaining representation parameters, and publishes an internal
worker alongside the source worker when their signatures differ. A caller can
select this worker from its header, without reading the dependency body.
Internal types and worker headers travel through the module codec and ABI
fingerprint, including modules that declare only types. A changed private
witness invalidates callers of the changed storage ABI. Internal names use a
reserved leading underscore.

Trials preserve checked invocation protocols, residual evidence, strict
evaluation order, and immutable published values. Copied callback and row
binders are fresh. Handler bodies can retain typed local storage in place;
workers with copied handler activations retain their general representation.
The ordinary Core linter validates the transformed program. The occurs check
rejects recursively growing storage, and unsupported or incompatible trials
leave the checked worker intact. No unbounded monomorphization occurs.

Runtime reflection maps a storage type back to the original nominal identity
and public parameters. Its extra parameters are implementation details;
inspection cannot reveal a hidden schema through its optimized representation.

## Runtime representations

The interpreter stores witness descriptors with a constructor value and installs
them in the opening's frame. Generated Go uses checked `any` conversions for
surviving hidden packages, with adapters for dependent function or nominal field
types. Every hidden witness has a stored descriptor, restored at each opening;
repacking and public-parameter representation conversions retain it. This is a
general fallback, with no unsafe pointer or function conversions. Repacking
between abstract binders with identical erased representations preserves the
existing fields instead of adding callback adapters or traversing their data.

Known construction/opening pairs can cancel during Core simplification. Field
expressions retain strict evaluation order and are evaluated once; the case's
abstract types are substituted with the packing witnesses throughout its body.
The resulting computation uses concrete field types and ordinary worker calls.
Inlining refreshes opening binders and substitutes packing witnesses along with
public arguments. Unused packages with already evaluated fields are removed.
Bounded unfolding exposes small factories and consumers across module boundaries,
including workers that only forward an invocation row. A bounded row-forwarding consumer
after a finite runtime choice can move into each branch, keeping its concrete
state throughout the traversal. The [inlining rules](core.md#inlining) bound
code growth and preserve callback invocation evidence.
[Callable member selection](lowering.md#callback-contracts) then removes local
callback bundles that would otherwise prevent Go from inlining the step.

A surviving package can hold callbacks or resource values whose types are
absent from its public parameters. Such a nominal type is not structurally
inspectable or rebuildable, and metaprogramming exposes its identity and public
arguments without a field schema. Deriving cannot assume instances for hidden
types. Cached declarations and Core retain both declaration-local binders and
opening/packing proof data through the ordinary object codec and ID remapping.

[Stream](../../stdlib/Stream.fango) and [Iterator](../../stdlib/Iterator.fango)
use hidden state in their public types. Their typed factories, transformations,
and recursive consumers use the same generic storage mechanism as user ADTs;
there is no Stream-specific lowering. See the
[compiler migration measurements](hidden-state-performance.md#library-migration)
for the measured coverage and the remaining general fallback boundaries.
