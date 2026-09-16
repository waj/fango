# Type inference

Generalization, effect rows, instance evidence, and nominal record obligations.

[Design index](../design.md). Source and checks: [Inference](../../internal/infer), [Types](../../internal/types/types.go), [Dependency tests](../../internal/infer/module_test.go), [Subsumption tests](../../internal/infer/subsumption_test.go), [Instance tests](../../internal/infer/instance_resolution_test.go).

## Types, rows, and annotations

Inference is Hindley-Milner with parameterized nominal ADTs, qualified schemes,
annotations, and effect rows. Constraints carry reasons; solving uses
unification with an occurs check. Variables have general or row kind. An ADT
parameter used as a row tail becomes row-kinded without explicit kind syntax;
its runtime argument erases to Unit and its Go generic parameter is omitted.
Effect names in row-kinded argument positions resolve as singleton rows.

Annotation variables are rigid skolems. A label-free open row normalizes to
its tail, allowing the fresh row of a call to unify with an annotated tail.
Row inclusion preserves rigid residual tails while combining effects around
handlers. Argument compatibility is directional, including named callbacks,
partial applications, and stored callbacks. It adapts shared-row schemes without
adding source-level row inequalities. Class-constrained types remain invariant.

Nominal variance is the least fixed point of positive/negative occurrences in
fields. Mixed occurrences and effect-label arguments are invariant. Abstract
imports use the same proof because the checker retains their schemas; row kind
alone does not establish covariance. Definition annotations compare known arrow
effects exactly before annotation equality can populate inferred rows.

For `{L | e} ⊆ ρ` with rigid `e` and open `ρ`, include labels immediately but
defer the bare tail until another constraint closes `ρ`, or solve it last.
Binding `ρ` to the rigid tail too early would make statement order decide whether
later effects fit. Diagnostics retain constraint order, independent of solve order.
Applications separate callee-shape equality from directional argument checks;
fresh expected types and covariant ADT row arguments get their own row views.
For bounds sharing a tail, extra permitted labels need no equality, missing
required labels can extend a flexible tail, and rigid tails cannot gain labels.
Conflicting arguments of one nominal effect fail the distinct-label check.
Solve shapes before row bounds; never retag a named binding while widening a use.

A pure handler runner may still need a polymorphic transport contract. Infer
this from controlled parameter types and executed call contracts, including
calls inside handlers but excluding latent lambda bodies. Handling an effect
does not convert an Exit-family callback into a Direct value.

## Dependency groups and generalization

Resolved references, including callbacks and nested bodies, determine strongly
connected components. Checking is dependency-first with deterministic
source-order traversal; emitted declarations retain source order. Function-only
components receive monomorphic provisional bindings. Solve their bodies,
effects, annotations, and record obligations together before generalization.
Propagate residual class obligations throughout a component; resolve concrete
ones in each body's source context. Independent components stay independently
polymorphic. Cycles containing ordinary values are rejected.

Variables absent from one member's public scheme are instantiated/defaulted in
that member's body without specializing a sibling. A local single-occurrence
effect tail closes only when not free in an enclosing scope or unfinished
component. A local annotation sharing such a tail checks argument/result shape
immediately but defers effect comparison and row equality until the component
is solved. Comparison precedes equality so annotations cannot invent effects.

Top-level values/functions and local syntactic functions/lambdas generalize.
Other local values remain monomorphic, preserving strict evaluate-once behavior
under lifting. `main` is ground. Integer expressions use `Num.fromInt`; decimal
literals and `/` stay Float. Unconstrained runtime metavariables default to Unit.
Numeric defaulting and source diagnostics are defined in
[classes](../reference/classes.md#defaulting).

## Class evidence and instance environments

`Scheme.Preds` stores nominal single-parameter obligations; instantiation
substitutes predicates with the body type. Resolution is directional and selects
only concrete predicates. Any metavariable or quantified variable defers the
whole predicate, including `Show (Box a)`. Given evidence takes precedence.
Generalization keeps residual predicates as dictionary parameters rather than
reducing them through an instance prematurely.

Overlap checks cover the entire graph; use-site visibility covers the defining
module and its transitive dependencies. Registration is source-ordered even
when body checking is dependency-ordered. Typed declarations and factories
retain the owner and instance cutoff used by inference, defaulting, elaboration,
and specialization. Later declarations cannot alter earlier concrete evidence;
polymorphic calls use caller evidence.

[Instance selection](../reference/classes.md#instance-selection) owns the exact
head/context precedence rules. Implementation must preserve these boundaries:

- Canonical head/context identities ignore variable spelling, duplicate
  predicates, and predicate order. Instance symbols distinguish builtin `()`
  from a user type named Unit.
- Availability probes and actual selection use the same rules. Missing evidence
  can make a context inapplicable; cycles, depth limits, and ambiguity are errors.
  Never fall back to a less-specific head when its selected group fails.
- Blanket-context class dependencies must be acyclic across all alternatives.
  Structured contexts need not decrease, so use-site resolution tracks active
  predicates and bounds growing chains. Repeated sibling requirements are valid.
- Numeric default eligibility may inspect alternative contexts independently
  without selecting evidence; resolve the original predicates after defaulting.
- Each module emits its own method workers and dictionary factories once.

An instance method has self evidence for its head under its declared context.
Elaboration can reuse the owner's workers/factory directly; it does not add a
public self constraint or perform fresh polymorphic lookup. Other polymorphic
method calls use supplied dictionaries, including partial applications and
lifted locals. Specialization cannot replace that evidence.

## Deriving

A deriver supplies method bodies as Code; the compiler owns the instance head,
method parameters, and exhaustive traversal skeleton. Generated methods use the
ordinary checker/elaborator and whole-graph overlap checks.

A first pass probes generated methods without a declared context and records
residual predicates; a second declares exactly those. Unused/phantom fields
therefore add no constraints. The instance is installed during the probe so
direct recursion reuses it; other cycles follow ordinary resolution rules.
Derive is a syntax dependency above Basics and Meta, below its consumers.
See [metaprogramming](metaprogramming.md) for expansion and rollback.

## Records and destructuring

Records share nominal identity, schemes, deriving, and constructor lowering
with single-constructor ADTs. Projection, update, and inferred literals/patterns
defer until the receiver type is known. Schema candidates never guess a type.
Resolve obligations to a fixed point: a field's type may determine another
receiver, or an outer literal may determine an inner one. Only obligations
owned by the binding being closed may fail when a pass learns nothing.

Every generalizing checker resolves records before predicates: top-level and
local functions, prompt expressions, instance methods, and deriver methods.
When an inferred form needs its annotation as context, unify that annotation
first but compare effects against the pre-unification zonk. Other declarations
keep the ordinary path.

Elaboration uses constructor, Let, and exhaustive one-constructor Case nodes.
Updates evaluate their receiver once and their fields in source order. Record
patterns fill omitted fields with wildcards and reorder to schema order; hidden
constructors never appear in source diagnostics. Top-level destructuring creates
one private monomorphic subject plus projections; local destructuring uses a
strict Let/Case chain. Both evaluate the RHS once and install binders together.
