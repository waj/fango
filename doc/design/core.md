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
| AsyncLaunch / AsyncRebase | Typed closure invocation, sealed result index, child evidence replacement |
| ParallelMap | Pure callback, matching List indices, bounded concurrency |
| AsyncSupervise | Runner thunk owning cooperative host cancellation through cleanup |
| FailureInspect | Checked descriptor and Maybe packaging; no target/resumption access |

## Scoped state boundary

`Runtime.Local.run` is a general local-state intrinsic. It lowers to existing
Let, Lambda, App, and NativeCall nodes: allocate a private `Runtime.Ref`, build
read/write callbacks, and call the consumer. The cell's value index and callback
row are checked against the bundled Cell declaration before lowering. No Core
node recognizes Reader; `Reader.withBytes` is ordinary Fango code using this
primitive. The native reference synchronizes individual reads and writes, with no
scheduler. Scope permissions require no runtime identities.

Scoped definitions retain their quantified `SourceType` and a `Scoped` marker;
call sites retain their instantiated source signature. Core lint validates the
restricted callback shape, full application, one fresh permission beyond the
residual row, and absence of that permission in outward call types. It rejects
permission labels in runtime function types. Exported schemes and serialized
Core preserve this metadata. Environmental and outer-storage independence are
proved by source inference before row erasure; Core's signature checks do not
reconstruct that source environment.

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
reuse requires forwarding the identical row without overlays. Lint rejects
missing call rows and stale evidence. Direct and Exit members share this ABI.
Abort projections select a fixed activation before unwinding.

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

## Lint boundaries and control normalization

Core lint rejects unsolved metavariables, malformed instantiations, mismatched
callee/evidence, invalid handlers, and residual open source rows. It reconstructs
binding types for fields, handler parameters/state/results, and resources. It
also independently reconstructs [structural evidence summaries](ownership.md)
and compares lexical evidence stacks after transforms.

Transport-polymorphic calls/operations in arguments, guards, fields, prefixes,
and return transformations are ANF-hoisted. Exit emission must inspect an
Outcome before evaluating the next source expression. Lint checks conventions
on callees/evidence and rejects control-producing nodes in unhandled slots.

Stage lint additionally admits checked quotes and reflected constants for
compile-time evaluation. Emission rejects compile-time values. Core dumps show
non-Direct conventions so ABI decisions remain reviewable.
