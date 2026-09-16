# Go backend and native runtime

Runtime representations, deterministic module emission, tail loops, build caching, and native boundaries.

[Design index](../design.md). Source and checks: [Codegen](../../internal/codegen/gen.go), [Build driver](../../internal/build/build.go), [Native validation](../../internal/modules/modules.go), [Tail predicate](../../internal/core/tailcall.go), [List runtime](../../runtime/fangort/list.go), [Module stability tests](../../cmd/fango/modules_test.go), [Native worker](../../internal/nativehost).

## Representations and ABI

Code generation builds formatted Go AST/source and invokes supported `go build`.
It does not depend on Go runtime internals, stack maps, barriers, or scheduler ABI.

| Fango | Go representation |
| --- | --- |
| Int, Float | int64, float64 |
| String, Char | Valid UTF-8 string, Unicode-scalar rune |
| Bool | bool; treated as True/False ADT by checking |
| Unit | Shared fangort.Unit/UnitValue when represented |
| Function | Typed Direct/Exit/Machine callable record |
| Ordinary ADT | Typed marker interface and constructor structs |
| Parameterized definition | Go generics with explicit instantiation |

Row-kinded ADT parameters are omitted from Go generics; their Core arguments
erase to Unit. Class dictionaries and factories use the ordinary typed internal
ABI. [Effect transport](effects.md#direct-exit-and-machine) and
[Core](core.md) own family/evidence contracts.

Concrete Unit parameters/results erase at direct worker and operation boundaries,
but remain values at first-class, polymorphic, ADT, and handler-closure boundaries.
Erasing an argument never erases its evaluation: preserve left-to-right effects
and materialize Unit only where a value is required. A handler return closure
returns Unit even if its enclosing worker uses a void result.

Expression-position If/Case/Seq use typed immediately invoked closures. In Exit
mode each closure returns Outcome at its own expression type, not at the outer
worker's result type. Normal leaves wrap at that same type. Propagation never
drops an exit or turns a language type check into a failed host assertion.

## List representation

The bundled List is recognized once by canonical symbol and validated shape;
subsequent passes compare nominal identity. Checking, deriving, reflection, Core,
and lint still treat it as an ordinary parameterized ADT. User cons types keep
normal lowering. The backends share fangort.List: a spine of fixed-size inline
arrays filled downward behind a two-word value.

List emits no marker interface/constructor structs. Construction is a runtime
call and matching uses emptiness/head/tail instead of a type switch. Its own
fields cannot be controlled; family differences are carried by its element type.
Eq/show retain the exported names and generic signatures of ordinary lowering
but delegate to runtime support, keeping element-operation synthesis independent
of storage.

A chunk watermark only decreases. Cons may claim the slot below it only when
the extended list owns the frontier; every existing value's offset is at or above
the watermark at creation. New cons is therefore invisible to all prior values,
without copying, and has worst-case constant cost even under branching. The
interpreter uses the same runtime representation. Public complexity belongs in
[collections](../reference/library-collections.md#list).

Dict is an ordinary opaque Fango [weight-balanced tree](../../stdlib/Dict.fango)
with cached subtree sizes.
Its balance invariant belongs to its module; it needs no compiler representation.

## Module emission and build cache

One generated Go module contains the entry package main, dependencies beneath
modules/, shared fangort, and native sidecar packages. Cross-package workers,
types, constructors, dictionaries, and effect evidence use a typed exported
internal ABI. Direct source imports remain Go edges even if unused; generated
types may add transitive type-owner imports.

Aliases and batch lifted names are deterministic and independent of numeric
identity allocation. Symbol mangling spells separators/operator characters as
words (for example `Basics.++` becomes `v_Basics_dot__plus__plus_`); disjoint
identifier/operator alphabets and the ban on leading underscores prevent
collisions. A dependency's generated package cannot depend on its consumers.
Project emission is the single generation path used by CLI and tests.

The driver writes only changed files beneath persistent .fango/build and removes
only stale generated paths recorded in its manifest. sources.json lists logical
names, root-relative or `<stdlib>/` paths, and SHA-256 hashes in deterministic
dependency order. Source/graph changes trigger Go build even when Go bytes match;
Go's cache reuses unchanged packages. Build copies the executable; run reuses it
while inputs are unchanged. [Commands](../reference/commands.md) owns output
paths and managed-directory safeguards.

## Self tail-call loops

Both backends use the pure Core eligibility predicate. It requires a saturated
self-worker call at identity type instantiation with unchanged evidence, reached
through the tail skeleton (Let body, If branches, Case leaves, Seq tail), never
through a lambda, handler, RHS, scrutinee, guard, or argument.

A definition is excluded if a lambda or handler clause/return captures a parameter
the loop mutates; Go closures capture locals by reference. Unchanged threaded
parameters may be captured. The reference owns the
[public guarantee](../reference/functions.md#tail-call-guarantee).

Go emits one for loop and simultaneous reassignment of changed arguments plus
continue. A dedicated return-position walker keeps continue out of nested
function literals. Seq tails do not introduce IIFEs; erased Unit arguments use
the ordinary ordered temporary prelude. Unchanged arguments, including dictionaries,
are omitted from jumps; a fully unchanged call is bare continue. No unreachable
return is needed after an infinite loop. The interpreter uses the same predicate,
builds the next parameter frame after evaluating arguments, shares decision-tree
dispatch, caches eligibility per definition pointer, and polls cancellation on
every loop iteration.

## Native boundaries

Primitives are declared in bundled Fango modules rather than a compiler catalog.
Inline native templates are bundled-only and retained for scalar primitives and
compiler-only representations. Validation requires a Go expression with each
positional placeholder exactly once, using only placeholders, compiler intrinsics,
predeclared names, and fangort. Emission reparses, scrubs positions, and substitutes
typed AST expressions with precedence intact.

Bundled and user call-form sidecars follow the same declaration correspondence,
standard-library import restriction, and Unit-erased scalar ABI. Go compilation
checks function bodies/types. Native effect operations supply a default only when
no Fango handler handles them. Each sidecar's hash enters sources.json and edits
or removals invalidate/prune generated packages.

Every materialized sidecar gets FangoHost, a reserved process-global interface
for input/output, arguments, directory, and exit, without hidden call parameters.
Its single source declaration is copied beside each sidecar with rewritten runtime
imports. Module-specific logic remains in its owner: IO owns console behavior,
File owns file/directory tables, Random supplies system entropy; deterministic
PRNG transitions and state remain Fango handler code.

Boundary shapes are resolved once after constructor declaration and stored in
native metadata, never re-derived by backends:

- A same-module single-constructor/single-scalar wrapper is projected before a
  call and reconstructed after it. The loader recognizes its declared shape;
  checking confirms resolved types. Interpreter CtorVal wrapping matches Go.
- Bundled File value natives additionally map Go `(T, error)` to Result IO.Error.
  fangort.ClassifyIOError supplies one shared kind/path/message classification;
  checked IO.Kind constructor order is its ABI. Go emits the Result construction
  at the call site; the worker sends failure separately from infrastructure errors
  and panics. User sidecars do not get this fallible shape.

File handle IDs are never reused. Tables persist per compiled process or native
worker session. Errors retain the caller's path rather than host-expanded absolute
paths. Opaque resource/lifetime checks prevent accepted Fango from using stale
handles; native code is trusted. Raw operations stay unexposed and native calls
are emitted only where wrappers reference them.

## Interpreter native worker

Ordinary call-form evaluation uses one persistent process per sidecar set. Shared
nativehost/nativewire packages own protocol and execution; generation supplies
imports, registry, and FangoHost bindings. Embedded support files are materialized
with AST-rewritten repository imports. Cache keys hash sorted destination paths
and exact bytes of the entire module, including fixed support sources.

A framed scalar protocol carries calls and reverse host requests over a dedicated
loopback connection, leaving process stdio outside the control channel. The active
interpreter host answers requests, sharing one buffered reader with the prompt.
Globals persist across calls, but importing new sidecars rebuilds the worker.
Panics are reported and reproduced; host exit becomes an interpreter exit error.
This is lifecycle isolation, not a security sandbox.

The in-process native registry is limited to templates, compiler representations,
and explicitly stage-safe bundled behavior. File registry entries validate arity
but refuse execution; actual file calls use the worker. Staging rejects user
sidecars, residual effects, and process-observing natives.
