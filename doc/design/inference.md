# Type inference

Generalization, effect rows, instance evidence, and nominal record obligations.

[Design index](../design.md). Source and checks: [Inference](../../internal/infer), [Types](../../internal/types/types.go), [Dependency tests](../../internal/infer/module_test.go), [Subsumption tests](../../internal/infer/subsumption_test.go), [Instance tests](../../internal/infer/instance_resolution_test.go).

## Types, rows, and annotations

Inference is Hindley-Milner with parameterized nominal ADTs, qualified schemes,
annotations, and effect rows. Constraints carry reasons; solving uses
unification with an occurs check. Variables have general or row kind. An ADT
parameter used as a row tail becomes row-kinded without explicit kind syntax;
its runtime argument erases to Unit and its Go generic parameter is omitted.
Effect names in row-kinded argument positions resolve as singleton rows, and a
row written in one resolves as itself; the parameter's kind, which its use in
the declaration fixes, decides which reading a type argument gets, so a row at
a general-kinded parameter is a kind error rather than a name lookup.

Inside a handler subject, the annotation effect budget includes the enclosing
handled labels. Nested handler clauses use that lexical budget; a handler's own
clauses remain outside its activation. Restoring the outer budget before
checking those clauses prevents a handler from authorizing itself or ambient IO.

Annotation variables are rigid skolems. A label-free open row normalizes to
its tail, allowing the fresh row of a call to unify with an annotated tail.
Row inclusion preserves rigid residual tails while combining effects around
handlers. Argument compatibility is directional, including named callbacks,
partial applications, and stored callbacks. It adapts shared-row schemes without
adding source-level row inequalities. Class-constrained types remain invariant.

Nominal variance is the least fixed point of positive/negative occurrences in
fields. Mixed occurrences and effect-label arguments are invariant. Abstract
imports use the same proof because the checker retains their schemas; row kind
alone does not establish covariance. Definition annotations compare the count
and nominal names of known arrow effects before annotation equality can
populate inferred rows; type constraints check their arguments.

For `{L | e} ⊆ ρ` with rigid `e` and open `ρ`, include labels immediately but
defer the bare tail until another constraint closes `ρ`, or solve it last.
Binding `ρ` to the rigid tail too early would make statement order decide whether
later effects fit. Diagnostics retain constraint order, independent of solve order.
Applications separate callee-shape equality from directional argument checks;
fresh expected types and covariant ADT row arguments get their own row views.
For bounds sharing a tail, extra permitted labels need no equality, missing
required labels can extend a flexible tail, and rigid tails cannot gain labels.
Fully resolved applications of one nominal effect are distinct labels. An
application containing a type variable can overlap another occurrence, so
inference unifies their arguments before deciding whether the labels coincide.
Identical applications collapse to one label; incompatible unresolved overlaps
report an effect mismatch. Source annotations reject duplicate applications.
Solve shapes before row bounds; never retag a named binding while widening a use.

A pure handler runner may still need a polymorphic transport contract. Infer
this from controlled parameter types and executed call contracts, including
calls inside handlers but excluding latent lambda bodies. Handling an effect
does not convert an Exit-family callback into a Direct value.
Async callbacks retain their effect requirements. Known unsupported aborts and
scoped local permissions are rejected at the spawn boundary; dependencies hidden
inside inherited handlers are checked when rebuilding child evidence. Functions
and native handles may be shared. See [tasks](tasks.md).

## Scoped callback rows

The [scoped declaration](../reference/functions.md#scoped-callbacks) marks a
restricted universal row binder on a named runner's final callback. The runner
implementation instantiates that callback's row independently at each use,
with its residual effect row as a lower bound. The binder cannot occur in the
runner's other parameters, result, residual effects, or callback result.
Recursive scoped runners and first-class runner values are rejected; ordinary
rank-one schemes cannot preserve this callback contract on those paths.

Each saturated source call allocates a rigid permission label from the session
supply and extends the runner's residual row with it. Distinct allocations
coexist in ordinary effect rows. Expected scoped callback types are available
while checking lambda parameters, so nested scopes do not infer an outer
reader's row from the inner reader. In constraint groups containing scopes,
explicit label lower bounds propagate before fixed-tail upper bounds close
flexible rows. Known record projections contribute their rows before closure,
so a source effect cannot hide a later local-state or failure requirement.

A persistent `ScopeBoundary` rejects its label anywhere in the solved result,
residual effects, or outer environment types. Its structural walk includes
latent arrows, abstract and phantom type arguments, and effect arguments.
Outer roots include storage slots, predicates, recursive bindings, earlier
runner arguments, and pending record obligations. Obligations survive local
generalization and record solving and are rechecked before the enclosing
declaration group is published. This checks type-level permissions; it does not
analyze which objects a closure physically retains.

[Core](core.md#scoped-state-boundary) retains source signatures for contract
validation and erases permission labels from runtime types and evidence. Calls
under an abstract scoped callback still forward the invocation evidence row: a
runner may have installed domain handlers hidden behind that callback binder.

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
that member's body without specializing a sibling. An unannotated definition's
arrow becomes pure when its effect tail occurs nowhere else in the inferred
type; a tail whose single occurrence is a type argument, as in `Box e -> Int`,
stays quantified because closing it would narrow the callers rather than the
body. A local single-occurrence effect tail closes only when not free in an
enclosing scope or unfinished component. A local annotation sharing such a
tail checks argument/result shape
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
substitutes predicates with the body type. Resolution is directional. Given
evidence takes precedence, then a metavariable defers the whole predicate
because solving could still change the answer.

A predicate whose remaining variables are all rigid is composed: its evidence
is assembled from its arguments' evidence, which the classes reference owns as
[one head per type constructor](../reference/classes.md#instance-heads-and-blanket-instances).
Composition applies only to a constructed type. A bare variable is whatever the
caller instantiates it to, so only a given discharges it, and a blanket head is
less specific than a constructor head that could match at one of those
instantiations, so a blanket group defers as well. Composition has two results.
Discharging proves the predicate from the givens, and may use a head group with
several candidates: a candidate whose context a given satisfies is applicable
at every instantiation, while a candidate whose context is undecided here
defers the whole group. Reducing replaces the predicate with the predicates it
composes from, for a context still being built — an inferred scheme, a derived
instance — and requires a single candidate, since with a choice the applicable
one depends on evidence the caller may or may not have.

Leaving a predicate whole is always sound; it is the answer before composition
is attempted. So a composition that cannot finish — a cycle, the nesting limit,
a missing or ambiguous instance — defers rather than reporting, and only a
concrete use raises those as errors. Composition can therefore discharge an
obligation that would have been `MISSING CONSTRAINT`, and never turns a
checking program into a failing one.

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

A class default is an ordinary top-level function, `_default_<method>` in the
class's module, annotated with the method's type under the class's own
constraint and placed directly after the class in source order. It is checked
and emitted once. An instance omitting the method receives an eta-expanded
forwarding method whose self evidence supplies that constraint, so defaults
add nothing to dictionary construction. Forwarding is eta-expanded and a
nullary instance's method rebuilds its own dictionary inline rather than
naming the package-level value, so dictionaries and methods never form a
value-initialization cycle.

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

Discharge a projection by subsumption from the stored field to the use, not by
equality. The use site is typed before the receiver names a record, so its
demand reaches the projection first; equating the two would make a field
declared at a closed row disagree with any body performing more than that row
names, though the row a use allows is an upper bound. Widening adapts at the
projection, which is also where the wider arrow's evidence comes from.

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
