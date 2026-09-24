# Core and evidence invariants

The typed compiler/interpreter contract, elaboration transforms, and independent verification.

[Design index](../design.md). Source and checks: [Core types](../../internal/core/core.go), [Core lint](../../internal/core/lint.go), [Rows](../../internal/core/rows.go), [Elaboration](../../internal/elaborate), [Lint tests](../../internal/core/lint_test.go), [Row tests](../../internal/core/rows_test.go).

## Typed representation

Every Core definition/expression is typed. Generic definitions declare type
parameters and uses supply explicit instantiations. Applications identify
workers, constructors, natives, operations, or indirect functions. Core contains
no imports, operators, structural records, or unresolved source effect tails.
Type variables appearing only inside erased nominal row indexes do not become
runtime type parameters. Variables in executing arrow evidence remain runtime
parameters, including effects whose operation signatures do not use them.

One pattern-matrix compiler handles case, equations, lambdas, handler groups,
and destructuring, including multiple argument columns. It checks coverage and
redundancy before emission, preserves ordered overloaded-literal/pin guards,
and lowers record views to constructor columns. Patterned workers use
deterministic hidden parameters and enter their decision tree only on final
application. One identifier-only row keeps its names and needs no tree.

Generic integer patterns call `fromInt` and Eq; scalar patterns retain literal
trees. Redundancy uses preceding structural coverage without assuming distinct
custom numeric literals are unequal. Pins are stable repeated tests, but do not
claim exhaustiveness. Source rules live in [patterns](../reference/types.md#pattern-matching).

## Dictionaries, calls, and intrinsics

Dictionaries are internal single-constructor ADTs containing typed method
functions. Qualified workers take dictionary parameters; conditional factories
take their context dictionaries. Construction/projection use ordinary Core
constructor/Case rules. Known evidence calls its method worker directly.
Exact scalar native forwarders and identities may bypass wrappers.

Go evidence arguments precede dictionaries, which precede source parameters.
Effects belong to method application, never dictionary construction. A method
implementation with no syntactic parameters must construct its function purely.
Capture metadata accompanies evidence until backend erasure.

Saturated pure natives become `NativeCall` keyed by canonical declaration.
`Prog.Natives` records schemes, arities, templates, modules, and effect owners;
lint validates declaration instantiation instead of switching on spelling.
Native effect operations stay Perform so lexical handlers can intercept them.

An intrinsic is a resolved bundled native implemented as Core, absent from the
native table. Its annotation uses ordinary type/row scope instead of scalar ABI
validation. Callback compatibility is ordinary directional argument checking;
intrinsic-specific checks concern ownership and lowering.

| Node/boundary | Invariant |
| --- | --- |
| ResumeTail | Exactly one owning tail resume on each normal path |
| ControlExit | Operation descriptor, payload, and lexical target agree |
| Bracket | Only in Runtime.Scope.bracket; unique scope, Unit release, joined child control |
| CoroutineScope | Only in Runtime.Coroutine.with; owner, pause factory, and driver protocols agree |
| CoroutineAdvance | Checked handle, reply/Step protocol, residual row, exclusive access; close returns Unit |
| Suspend | Unowned host-driven Machine fixture boundary; not emitted from source |
| FailureInspect | Checked descriptor and Maybe packaging; no target/resumption access |

## Residual evidence rows

Source row tails erase after evidence requirements are derived. Each arrow
retains whether its ABI had a residual row, independently of transport
polymorphism; pure factories do not inherit their returned callbacks' rows.
Substitution, field instantiation, adapters, and synchronous lowering preserve
this metadata recursively through returned arrows and nominal arguments.

Core names row binders and explicit arguments, including lexical overlays and
deferred callback evidence. Its verifier checks binder scope, arrow agreement,
and exact activation identities. Rewrites, capture reconstruction, and free-row
analysis distinguish invocation binders from captured rows. Equal source types
cannot retag a stored value to a different residual-row ABI.

The interpreter passes rows explicitly on worker/closure calls; deferred
operations resolve through them at execution. Captured evidence stays fixed,
and mutable-frame closure snapshots retain only referenced rows. Self-frame
reuse requires forwarding the identical row without overlays. Machine calls
and cursor transitions retain typed row inputs; lint rejects stale deferred
evidence or missing call rows. Frames and handlers restore rows on completion.

Direct, Exit, and Machine members share this explicit ABI. Elaboration records
instantiated residual labels before erasure. Abort projections select a fixed
activation before unwinding. Capture contracts substitute the actual row's
owners while cursor advancement holds its borrow. Captured outer rows retain
resource captures; invocation rows are local binders.

## Adapters and specialization

A higher-order worker's erased callback row may differ from the concrete
argument ABI. Eta expansion preserves the original binding and substitutes its
execution, evidence, captures, and per-arrow control. Inputs adapt
contravariantly and results covariantly. Nominal values needing representation
changes use typed constructor/Case reconstruction and recursive local helpers;
identically erased row-indexed values require no traversal. Local references,
including partial applications, use their binding's actual ABI. A record
field's binder is one of those: it holds the arrow the constructor stored,
which is what the record's row argument erases to, so a projection wanted at a
wider row adapts there rather than retagging the binder. The literal adapts
the same way on the way in, and one stored value therefore serves projections
at different rows.

Bounded scalar specialization emits Int/Float variants for effect-free source
workers with one numeric type parameter and only standard scalar constraints.
Workers with handlers stay generic to preserve lexical evidence. Substitute
already-resolved dictionaries, simplify known projections/forwarders, and
redirect only calls supplying those exact dictionaries; never rerun selection.
Both variants are emitted by the defining module regardless of consumers.
Strict Let bindings prevent duplication and preserve beta-reduction order.
The generic worker remains available and all variants pass ordinary lint.

Checked definitions also expose bounded execution templates for small Machine
wrappers. The whitelist permits strict bindings, matches, constructors, known
calls, coroutine advancement, and opening an existing Work package against its
owner. Work opening preserves the runtime owner check and introduces no
authority. The whitelist admits no callbacks, evidence binders, resource
owners, state, cleanup, or staging nodes. Recursive call cycles and bodies over
48 expression nodes are excluded. Templates remain unexpanded semantic Core;
the original body and capture contract are the interpreter and ownership
reference. Their contents participate in the module ABI fingerprint.

After semantic Core validation, Machine lowering may instantiate these
templates at saturated, statically known calls. It substitutes types, freshens
local bindings and decision-tree names, and forwards the caller's checked
residual evidence. Arguments become strict bindings in their original order.
Expansion is limited to four nested templates and 128 added expression nodes
per worker. Calls outside this whitelist or budget retain their ordinary ABI.
This is an automatic execution optimization; it introduces no source pragma.

## Lint boundaries and control normalization

Core lint rejects unsolved metavariables, malformed instantiations, mismatched
callee/evidence, invalid handlers, and residual open source rows. It reconstructs
binding types for fields, handler parameters/state/results, and resources. It
also independently reconstructs [capture contracts](ownership.md), compares
complete lexical evidence stacks, and repeats ownership proofs after transforms.

Transport-polymorphic calls/operations in arguments, guards, fields, prefixes,
and return transformations are ANF-hoisted. Exit emission must inspect an
Outcome before evaluating the next source expression. Lint checks conventions
on callees/evidence and rejects control-producing nodes in unhandled slots.

Ordinary lint rejects Machine Core unless the canonical Runtime.Coroutine.with owner
enables the private boundary, and always rejects raw host Suspend nodes.
Pre-machine lint admits checked Machine/Suspend nodes
while preserving other semantic invariants; staging additionally admits checked
quotes/reflected constants. Emission rejects compile-time values. Core dumps
show non-Direct conventions so ABI decisions remain reviewable.
