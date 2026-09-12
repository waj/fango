# fango design

This document describes the compiler and language architecture implemented in
the repository. User-facing behavior belongs in [the reference](reference.md),
while unfinished work and possible directions belong in [the roadmap](roadmap.md).

## Goals and constraints

fango is a strict, statically typed, purely functional language with an
Elm/Haskell-flavored surface. It aims for whole-program type inference,
algebraic data types, pattern matching, direct-style algebraic effects, quick
builds, and generated programs whose representation and calls are close to
ordinary Go.

The implementation stays deliberately small: one Go toolchain, no compiler
framework dependencies, a local source-module model, and a compiler-bundled
experimental standard library. Laziness,
self-hosting, a package manager, and a general optimizer are not current
features.

## Language semantics

Evaluation is strict and call-by-value. Ordinary definitions are pure. Effects
are tracked in rows and executed only where evidence is available: inside a
handler, at an effectful call site, or by the entry-point IO runner.

Top-level declarations and block bindings are scoped in source order. Forward
references and shadowing are rejected. A block evaluates its bindings and Unit
statements eagerly in order, then evaluates exactly one result expression.
Top-level functions may recurse; local function bindings may recurse, but
ordinary value self-reference is an undefined-name error.

Contiguous same-name, same-arity function rows are one equation group. Each row
gets an independent pattern scope, while the recursive function name,
annotation, argument vector, result type, and final-arrow effect row are
shared. Handler operation and return groups obey the same row semantics.
Single-row lambdas and destructuring bindings pass through the identical
coverage invariant rather than introducing a runtime match-failure path.

Functions are curried at the language level. A syntactic multi-parameter
function nevertheless has a known worker arity, allowing saturated calls to
compile directly while partial applications allocate typed closures.

An operator is an ordinary value whose name is punctuation, so every operator
other than `&&` and `||` is an application of a declared value and both
operands are evaluated. `&&` and `||` are surface syntax that elaborates to
`If`, which is what makes them short-circuit. Core has no operator node at
all: after fixity resolution an operator application is a call, so nothing
downstream switches on an operator spelling.

Custom types are nominal and identified internally by a generation-stable
integer `Unique`, not their printed name. Constructors inhabit a separate
namespace. Pattern matching is exhaustive, and redundant branches are rejected.
Pinned patterns compare against an existing immutable value through `Eq` and
are conservatively refutable for coverage. Nominal record patterns are keyed,
partial views of a visible schema. `Bool` behaves as the predefined
`True | False` ADT to the checker while using native Go booleans in generated
code. Bracket list expressions and patterns are parser sugar for the bundled
`List.Nil` and `List.Cons` constructors, so they use the same inference,
coverage, and evaluation rules as explicit constructor code. `List` is an
ordinary parameterized ADT to the checker, the deriver, reflection, Core, and
the linter; only the backends know it is stored as an array spine.

Tuple syntax is the same arrangement without the representation half:
`(a, b)` and `(a, b, c)` are parser sugar for the bundled `Tuple.Pair` and
`Tuple.Triple` types, in type, expression, and pattern position alike. They
are ordinary nominal ADTs everywhere below the parser — no new type former,
no structural typing, and `Eq`/`Ord` derived in fango rather than synthesized
by the compiler. Arity stops at three because each one is a separate bundled
declaration, not because anything in the compiler counts. `Tuple` is a
prelude root, since syntax that always parses must always resolve, including
in the REPL.

## Functions and effects

The internal function type is `TFun{Arg, Eff, Ret}`. Effects belong exclusively
to individual arrows: applying `A ->{e} B` performs `e`, while applying
`A -> B` is pure. Every curried arrow has independent timing. In
`A ->{IO} (B -> C)`, IO occurs at the first application; in
`A -> (B ->{IO} C)`, it occurs at the second.

Function values, including effectful Unit functions, are first-class and
execute only through explicit application. `f()` is surface sugar for applying
`f` to Unit, and a definition `f() = body` uses the existing one-parameter
worker representation with a discarded Unit parameter.

Native value declarations may carry the same effect rows as ordinary
functions, including `IO` and user-declared effects. The row participates in
inference and checking; a sidecar call uses the scalar ABI of its annotation
and receives no hidden evidence argument.

A syntactic multi-parameter worker executes its body only after its final
parameter, so inferred effects belong to the final arrow and earlier partial
applications are pure. A one-parameter function whose body returns a lambda is
different: effects before constructing the lambda belong to the outer arrow,
while effects in the lambda body belong to the returned arrow.

Effect rows contain distinct, nominal effect labels and an optional open tail.
Row solving supports inclusion and union for nested and higher-order calls.
Effect declarations may be parameterized. General runtime operation-local
polymorphism is not supported; the one exception is an abort-only operation's
single caller-selected result variable, which cannot occur in its payload. A
saturated operation application performs; a partial application is a pure
closure.

Custom effects have one uniform operation discipline per declaration. A
resumptive effect remains complete, one-shot, and tail-resumptive: every
operation clause ends in exactly one tail `resume` on every normally completing
path. An abort-only effect declares every operation with `abort`; its clauses
have no resume binding and return the handler answer directly. A saturated
abort is an explicit exceptional terminal, so it can discharge a pending tail
resume obligation on that path. Escaping and general continuations remain
rejected.

Each resumptive clause and its resume occurrences carry a compiler-only owner
identity. Source checking proves that the owner's resume occurs only as the
terminal action of every normal path, including equation groups, nested
handler returns, lambdas, operands, and staged results. Typed Core preserves
the owner in `ResumeTail`, and Core lint independently re-establishes the same
control-flow and type invariant after elaboration transforms. Abort operations
instead elaborate to typed `ControlExit` terminals. The optional `return`
clause transforms normal completion only; an abort answer bypasses it. Handling
builtin `IO` is currently rejected. These restrictions let both backends use
stack-local evidence and direct returns or tagged exits, without goroutines,
channels, panic sentinels, or continuation objects.

A handler may own one parameterized state cell. The initial expression runs
once before evidence installation. Operation and return clauses receive an
immutable snapshot binding; the handled body does not. A stateful tail resume
evaluates its operation result and next state left to right, commits the update,
then returns the operation result through the existing direct evidence call.
Core keeps the state binder, initial expression, type, and every next-state
expression until both lint and capture analysis have checked them. The
interpreter stores the cell on its evidence activation; Go emission uses one
closure-captured local per handler setup. Neither backend allocates or captures
a continuation, and nested same-effect handlers own distinct cells.

A cleanup scope is a second kind of region, introduced only by the bundled
`Scope.bracket` intrinsic. Acquisition runs once; on success the release runs
exactly once when the scope exits, including when the body is carrying a
tagged exit aimed at an outer handler, and including when a handler's return
transformation fails inside it. Nested scopes release in reverse acquisition
order. A release is an ordinary closure created at the call site, so it runs
with its own definition-site evidence rather than whatever handler stack was
installed where the body exited; no obligation record is needed to arrange
that. A release that fails while the body was already exiting does not become
the answer: the body's exit stays primary and the release's exit is recorded
in it, inner to outer. A release that fails after a successful body is the
only failure and propagates on its own.

Scopes carry their own `ScopeID`, and the acquired resource binds it, so the
existing non-escape analysis applies to a borrowed resource exactly as to a
scoped handler activation. Inside the intrinsic the body applies an abstract
callback to the resource, which the conservative indirect-call rule always
treats as retaining it, so the intrinsic itself is exempt and its callers are
restricted instead: a call whose instantiated result can carry a capture is
rejected when the instantiated resource type can carry one too. A scope over a
scalar — `Scope.finally`, whose resource is Unit, among them — leaves its
result unrestricted. Because a scope's ScopeID is also a scoped capability,
storing the resource in another scoped handler's evidence is rejected by the
same cross-scope rule that governs state cells.

Every handler activation also has a compiler-only `ScopeID`. Evidence in Core
therefore names both its nominal effect and the activation (or an abstract
capture variable when a worker or lambda receives the evidence from its
caller). This distinction is load-bearing for nested handlers of the same
effect: operation selection remains nominal, while capture and availability
checks distinguish the two lexical capabilities. Scope and capture identities
are erased by both runtime backends; they are not liveness flags.

Abort evidence additionally carries a fresh runtime target token. Performing
an abort evaluates its payload left to right and returns an `ExitRequest`
without running the clause at the perform site. Intervening computations
propagate the request until the matching target boundary; that boundary
restores definition-site outer evidence and evaluates the clause. Foreign exits
continue outward, and exits from a clause or return transformation are never
routed back into the same activation. Target tokens are pointers to a
non-zero-sized runtime value, so recursive activations of the same nominal
effect remain distinct.

Capture summaries are separate from effect rows. Each worker parameter and
caller-supplied evidence parameter binds a capture variable, and a fixed-point
analysis records which of those variables or concrete scopes may occur in the
worker's result. Calls substitute actual argument/evidence captures into that
summary. Lambdas retain the captures used by their body after removing their
own term and evidence binders; constructors and records retain field captures;
matches, partial applications, dictionary values, lifted locals, and callback
row adapters propagate them. Unknown indirect calls conservatively retain
capture-capable arguments. Concrete scalar and Unit results cannot carry a
capture, and nominal ADT schemas are inspected transitively so ordinary
synchronous traversals returning immutable data are admitted.

Source-declared stateless effects remain durable by default, preserving
existing Reader closures. A parameterized handler is scoped regardless of its
nominal effect's default policy. Compiler-owned state/resource APIs may also
mark evidence scoped, mark an
operation result as borrowing its evidence, or mark an operation as retaining
arguments. A scoped handler rejects a result that transitively retains its
activation, including a closure hidden in an ADT or one passed through another
worker. A retaining operation also rejects storing an inner scoped capture in
different evidence. Core lint independently recomputes summaries, checks
scope introduction and exact evidence-stack availability, and repeats the
non-escape proof after ANF, lifting, callback adaptation, and specialization.
The bundled polymorphic State, Writer, and seeded-Random runners carry a hidden
capture boundary: calls whose instantiated result can carry their local
capability are conservatively rejected; immutable scalar and transitively
capture-free ADT results are admitted.

Execution transport is compiler metadata distinct from both effect rows and
operation discipline. Each Core arrow, definition, lambda, application, and
effect-evidence slot records a `Direct`, `Exit`, or future `Machine` lower
bound, plus whether the mode is selected from its enclosing control context.
An open row or abstract custom-effect evidence is transport-polymorphic even
when the source type has no ordinary type variables: a handler interpretation
may perform a non-local exit before returning the operation's apparent result.
The metadata is inferred from solved rows and supplied evidence, not by
scanning a function body for a particular operation name. It belongs to each
curried arrow independently, preserving early-effect and partial-application
timing. `Machine` is represented in the contract but rejected until selective
machine lowering is implemented.

Transport-polymorphic definitions have one joined contract rather than a
variant for every combination of callback and evidence modes. Their defining
module always emits Direct and Exit ABI members, so a downstream consumer
cannot change dependency output. A Direct callback widens to Exit through an
eta wrapper that calls it and returns `Normal`; the inverse conversion is
illegal. ADTs, including class dictionaries, that transitively store a
transport-polymorphic function receive corresponding module-owned Direct and
Exit representation families. This keeps stored callbacks typed without
boxing every ordinary value or guessing an ABI after row erasure.
Pure wrappers inside one of those members retain its representation family:
entering a pure lambda changes that lambda's execution protocol, but does not
switch its controlled parameters, constructor results, or nested values back
to the Direct family. A named pure worker that produces a controlled value
likewise has Direct- and Exit-family members even though both members use the
Direct execution protocol; the family selects the result representation
independently of whether the worker itself returns an outcome.

## Compiler pipeline

The batch pipeline is:

```text
source -> lexer -> parser -> AST -> inference -> typed AST
       -> elaboration -> Core -> Core lint -> Go AST -> go build
```

The hand-written lexer records byte spans and line/column positions but does
not synthesize layout tokens. The recursive-descent parser applies the offside
rule from token columns. Operator runs parse flat rather than into a tree,
because fixity is declared in source and a file is parsed before the module
graph exists; internal/fixity groups them afterwards. A token at
the innermost layout column normally ends the current construct; the parser
exempts single tokens that open a construct, and an `if` additionally exempts
its own `then` and `else` at the column of its `if`, so a chain of arms can
align under one `if` rather than staircasing rightward. Bracket lists lower
immediately to right-nested constructor applications or patterns; an omitted
tail lowers to `List.Nil`. Tuple syntax lowers the same way, to a
saturated `Tuple.Pair` or `Tuple.Triple` application or pattern; a
parenthesized item with no comma stays a grouping. AST and diagnostic dump formats are stable
golden-test interfaces.

Batch compilation first discovers the complete module graph. The entry
directory provides local modules, where `Foo.Bar` maps to `Foo/Bar.fango`, and
an embedded provider supplies standard-library modules. Bundled names are
reserved rather than silently shadowed by local files. Modules from both
providers are parsed and their declared public interfaces validated before a
deterministic dependency-first topological order is chosen, with lexical
tie-breaking.

The embedded prelude used by the REPL and focused checker tests follows the
same bundled dependency closure instead of maintaining a parallel module
list. Its roots are `Basics`, `Meta`, `Derive`, and ambient `IO`; ordinary
imports and syntax-driven `Meta`, `Derive`, and `List` edges recursively add
their dependencies. The checker retains that resolved owner set so fixture
projections can omit the whole prelude while still elaborating and linting it.
Bundled modules therefore use the public standard-library types rather than
private substitutes.

Primitives are declarations rather than a compiler catalog. Every ordinary
module implicitly loads the hidden `Basics` module, whose native values define
the scalar implementations of class methods, and the bundled `IO` module declares the ambient IO
effect and its operations. Basics also declares the standard operators and
their fixities, which any module may do — there is no privileged operator
set. `native "..."` templates remain bundled-only. They are retained for
inlined Basics scalar primitives and Meta's compiler-only representations. The
loader validates each template as a Go
expression, requires every positional argument exactly once, and permits only
placeholders, compiler intrinsics, Go predeclared names, and the `fangort`
qualifier. Code generation reparses and position-scrubs the template, then
substitutes typed Go AST expressions with precedence preserved.

Both ordinary and bundled modules can instead declare a call-form native
and place a `<Module>.native.go` sidecar beside the source. The loader applies
the same rules to both: `package native`, standard-library-only imports,
bidirectional declaration/function correspondence, and a closed scalar,
Unit-erased ABI. Each content hash joins `sources.json`; the build synchronizer
materializes each loaded sidecar as a separate package below `native/`, so
edits and removal invalidate the build and prune stale generated files. Go
compilation remains the final body/type check. Native effect operations use the
same ABI and are eligible only when no Fango handler handles the operation.
Generated sidecar packages also receive a reserved `FangoHost` process-global
whose interface covers input, output, arguments, working directory, and exit;
there is no hidden function parameter or module-specific calling convention.
That binding has one ordinary Go source in the standard-library tree. Project
and worker materialization copy it beside every sidecar and rewrite its runtime
import for the private generated module, so the repository build and generated
packages compile the same declaration.

Fixity resolution runs between parsing and name resolution. Because a fixity
is declared in source and may live in any module of the graph, the parser
cannot shape an operator run as it reads one; it records the run flat and
`internal/fixity` rebuilds it into operator applications once every file is
parsed. Resolution happens before name resolution, so the operators it
produces are canonicalized like any other reference, and it covers quoted
code, so a quote groups the way it would have written inline. The invariant
that pays for the split: no unresolved run survives module loading, so
inference, elaboration, and staging only ever see operator applications.
Fixity binds a spelling rather than a value — a class declares an operator
and its instances implement it — so the table is graph-wide, and a run's
shape does not depend on which module it is written in. Module-scoped
fixity, where an import could change how a run parses, is deferred.

Name resolution rewrites module-level declarations and imported references to
opaque, collision-free canonical symbols before inference. Local binders keep
their source names. An operator is an ordinary name here too: it resolves
through the same scope, and the implicit prelude injects the ones `Basics`
exposes so arithmetic needs no import. The resolved modules are merged in graph order and checked
with one graph-wide fresh-name supply and one set of builtin identities. This
shares nominal ADT and effect identities safely across module boundaries while
an import can seed only its direct dependency's declared public interface.
Core contains no import syntax, but top-level definitions retain their source
module owner so the Go backend can recover compilation boundaries. `Prog.Entry`
identifies the selected entry definition independently of its printed name.
Nominal record schemas follow type visibility, while field visibility follows
`Type(..)`. Resolution records the visible nominal candidates for each field
use; inference never treats a label as a structural type constraint. A named
literal or pattern is gated by resolving the type name itself; an inferred one
has no name, so those per-label candidates are the whole gate and are recorded
for patterns as well as expressions.

Inference and elaboration are separate because code generation is
type-directed. Elaboration resolves defaulting,
derives evidence requirements, collapses application spines, chooses direct or
indirect calls, lambda-lifts polymorphic locals, compiles matches to decision
trees, and puts expression-shaped control flow into ANF where Go needs
statements.

## Type inference

Inference is Hindley-Milner with parameterized ADTs, qualified schemes, explicit
annotations, effect rows, and two variable kinds: general and row. ADT
parameters whose uses require a row variable are inferred as row-kinded
parameters; this permits declarations such as `type Foo eff = Foo (() ->{IO |
eff} ())` without explicit kind syntax. The row kind is source-level metadata:
runtime/Core types erase those arguments to Unit, and generated Go types omit
their generic parameters. It generates
reason-tagged equality/inclusion constraints, solves them by unification with
an occurs check, and generalizes at binding boundaries. A label-free open row
normalizes to its tail during unification, so an annotation's rigid row
variable unifies with the fresh row a call site mints — this is what lets a
handler wrapper carry an explicit open-tail annotation. Annotation variables
are rigid skolems, preventing an annotation from claiming more polymorphism
than its body supplies. Variable spelling never grants numeric or other
capabilities.

In a row-kinded ADT argument, an effect name is contextual row syntax: `Foo IO`
is resolved as the singleton row `Foo {IO}`. Parameterized effect applications
use the same rule, while ordinary type-kinded arguments retain ordinary type
resolution.

Row inclusion preserves a rigid residual tail when composing effects around a
handler: an outer row such as `IO | eff` retains the annotated `eff` tail while
adding effects performed by handler clauses. This allows an annotated handler
to carry both residual effects from a row-kinded ADT payload and its own `IO`
work.

Inclusion is order-independent. `{L | e} ⊆ ρ`, where `e` is an annotation's
rigid tail and ρ a surrounding row that is still open, is answered by putting
`L` in ρ and binding ρ's tail to `e` — correct as a final answer, but a row
ending in a rigid tail cannot absorb a label afterwards, so answering it in
place would make a body's call order decide whether it checks: an `{e}` call
before an `{Exception ex | e}` one would close the row against `Exception`.
The solver therefore splits that shape into the labels, included immediately
and leaving ρ open, and the bare tail, deferred. A deferred tail is solved
once something else closes ρ's own tail — usually the annotation — and
otherwise last, when the binding is the answer rather than a guess. Because
the labels land first, a surrounding row may hold effects the callee does not
perform, which is what lets an `{IO, Exception ex | e}` body call an
`{Exception ex | e}` argument. Splitting never suppresses a diagnostic:
failures are reported in constraint order regardless of the order they were
solved in.

Top-level values and functions generalize. Local syntactic functions and
lambdas generalize, while local values remain monomorphic so their strict,
evaluate-once semantics are not changed by lambda lifting. `main` is ground and
never generalized. Integer expressions elaborate through `Num.fromInt`;
decimal literals and `/` remain `Float`. Unresolved variables default to `Int`
only when their default-eligibility requirements include standard `Num` and contain no classes
outside standard `Num`, `Eq`, `Ord`, and `Show`. Other unresolved constrained
variables are ambiguous; unconstrained runtime metavariables default to Unit.

`Scheme.Preds` carries nominal, single-parameter class obligations.
Instantiation substitutes predicate types alongside the body. An instance head
is either a bare variable (a blanket instance) or a named constructor applied
to arbitrary argument types — variables (possibly repeated), ground types,
or nested applications. Direct function heads and open effect rows are rejected.
Blanket heads can match any value type, including functions. Unifiable heads
must be comparable by directional matching. Equivalent heads may coexist with
distinct contexts; equal head/context pairs and incomparable-unifiable heads
are rejected at declaration. Context identity is a predicate set aligned by
head variables, independent of variable names, repetition, and predicate order.

Concrete resolution first chooses the most-specific equivalent head group,
then checks each candidate's context. Missing evidence makes a candidate
inapplicable; cycles, nesting-limit failures, and ambiguity remain errors.
Among applicable candidates, strict context supersets dominate subsets without
inferring implication through other instances. Among the remaining maxima,
the latest declaration per module wins. Multiple remaining modules report
`AMBIGUOUS INSTANCE`, never a winner based on module load order.

Resolution is directional and selects instances only for concrete predicates.
Every predicate containing a metavariable or quantified variable is deferred,
including structural arguments such as `Show (Box a)`. Generalization retains
the whole predicate as a dictionary parameter, so a caller supplies evidence
for its specialization. Explicit given evidence takes precedence over lookup.
An annotation must supply the actual required predicate: `Show a` does not
discharge `Inspect a` merely because a blanket instance relates the classes.
Constraints whose variables do not occur in the annotated type are ambiguous.

Numeric default eligibility may inspect general instance contexts without
choosing evidence, considering alternative contexts independently. In particular, `Num a, Inspect a` can qualify through a
`Show a => Inspect a` blanket. The original predicates are resolved again
after defaulting, preserving concrete specializations. Custom bare constraints
without an eligible blanket still prevent numeric defaulting.

Named instances permit structural contexts using only variables of the head,
without open effect rows or a decreasing-size requirement. Blanket contexts
must constrain their single head variable. Their class-dependency graph must
be acyclic across all loaded modules; edges include every alternative context,
and declarations closing a cycle are rejected.
Structured context cycles are checked at use sites: repeated active predicates
report their resolution chain, and growing chains hit a fixed nesting limit.
Inference, evidence availability probes, and elaboration all bound recursion;
repeated sibling requirements are not cycles. Instance selection never falls
back to a less specific head when the selected context fails.

Classes and instances are checked in source order. Typed declarations and
instance factories retain an instance-environment cutoff, replayed during
elaboration and scalar specialization, so later declarations cannot alter
earlier concrete evidence. Polymorphic calls still receive caller evidence.
All instances in the loaded
graph participate in overlap checking, including orphan instances, while
resolution uses only the defining module and its transitive dependencies — a
module that cannot see a more specific instance resolves through the general
one. Instances are not selected by import exposing lists. Canonical class
symbols and generation-stable type identities determine coherence, not
display names.
Instance symbols include canonical head and context identities and retain the
distinct spelling of builtin `()` rather than collapsing it into the
user-definable name `Unit`. Each declaration emits its workers and dictionary
factory only in its defining module; blanket instances do not generate copies
for every matching type or importing module.

Explicit `deriving (C)` runs C's *deriver* — a compile-time generator written
in ordinary fango — and installs the instance it produces. Generated methods
are checked and elaborated through the same path as handwritten ones, so
nothing downstream knows a method was derived. The compiler owns the instance
head, so derived instances go through the same whole-graph overlap check as
handwritten ones; it owns the method parameters, which it hands to the deriver
as `Code`; and it owns the traversal skeleton, so a deriver never invents a
binder and exhaustiveness is structural. The deriver owns only the bodies.

Instance contexts are use-driven rather than syntactic. A first pass checks
the generated methods with no declared context and records the predicates they
leave residual; a second pass declares exactly those and is otherwise an
ordinary `InstanceDecl`. A field the generator never touched, and a phantom
parameter that appears in no field at all, therefore demand no evidence.
Direct recursive fields reuse the instance being checked, because the probe
runs with the instance already installed. Other cyclic context chains follow
the ordinary use-site rejection rule. There is no automatic structural
equality or display for a source ADT without an instance.

The bundled `Derive` module supplies the derivers for `Eq`, `Ord`, and
`Show`, written the same way a library would write its own. It sits above
`Basics` and `Meta` and below everything else: a file that writes `deriving`
gets a loader edge to it, exactly as a file that writes `quote` gets one to
`Meta`. That edge is also what makes the deriver visible under the ordinary
instance-visibility rule.
Nominal records use the same type identity, schemes, and deriving machinery as
single-constructor ADTs. Field projection and update are deferred until the
receiver has unified to a known record type, then checked against the resolved
visible schema. A literal or pattern that omits its type name defers on the
same mechanism, with its own type as the receiver: the expected type decides
which record it is, and the schema check that a named form performs eagerly
happens once that type is known. A label is still never a structural
constraint — candidates only filter what this module may see, they never select
a type — so an inferred form no context reaches is ambiguous rather than
guessed. Deferred accesses resolve to a fixed point rather than in one
pass, because one access's receiver is often another's result: `ctor.fields`
decides the element type a later `field.index` reads, and an inferred literal
nested in another's field is decided by the pass that resolves its parent. Only
obligations that survive a pass learning nothing are genuinely ambiguous, and
only those raised by the binding being closed: a block-local binding solves
early, and an enclosing declaration's obligation may still be waiting on code
the checker has not reached. Every checker that
generalizes a body — top-level values, prompt expressions, local function
bindings, instance methods, and deriver methods — resolves them before
reducing predicate obligations, so a constraint on a field's type names a type
rather than an unsolved variable.

A declaration's annotation is normally reconciled with its body after that
fixed point, which is too late to decide an inferred form the annotation is the
only context for. When one is actually waiting, the annotation is unified
first, and the effect-row comparison is taken against the pre-unification zonk
so an annotation still cannot claim effects its body never performs. Doing this
through the substitution rather than by threading an expected type down the
syntax is what makes every position work alike — a list element, a branch, a
nested field, an argument in a curried spine — and the gate keeps declarations
that use no inferred form on exactly the path they were on before.
Elaboration lowers literals, projections, and functional
updates to the existing constructor, `Let`, and exhaustive one-constructor
`Case` Core forms. This keeps Core and both backends free of a second record
representation while preserving single evaluation and source-order effects.
Record patterns follow the same lowering: omitted fields become wildcards and
provided patterns are reordered into schema order. Hidden record constructors
never appear in source diagnostics.

Top-level destructuring is represented by one private monomorphic subject
definition and one projection definition per binder. All projections reference
that subject, so the RHS is evaluated once and the binders become visible as a
group. Local destructuring uses the same subject-plus-decision-tree shape in a
strict `Let`/`Case` chain.

## Compile-time metaprogramming

Splices are expanded during inference, before Core exists, so both backends
see identical generated code and every existing gate applies to it unchanged.

Reflected types are compiler-owned `Meta.TypeRepr` values. They retain nominal
identities and the complete structural type, so equality includes type
arguments and function effects without consulting display names. `typeOf` is
an explicit compile-time Core operation; the Core linter rejects one that
survives into runtime code. Scalar lifting creates compiler-built AST
fragments alongside quote templates, and expansion copies either form.

A `TypeRepr` also carries the schemas its reflection site was allowed to read
and a narrow interface back to the checker's declaration table. That is what
makes `Meta.info` answer `Opaque` for a type imported as `T` and `Visible` for
the same type imported as `T(..)`: reflection introduces no new visibility
rule, it reuses the one that already governs constructors and record fields.
Walking into a type argument or a constructor field carries the same
visibility along, so an abstract type stays abstract at compile time exactly
as it does at run time. There is deliberately no `reify`: a splice cannot ask
the compiler about a name it did not itself name, and cannot ask whether an
instance exists. A generator emits a method call and lets ordinary instance
resolution answer.

The reflection and code-building primitives are pure natives that never
construct a fango value: they answer counts, indices, names, and opaque
handles, and `Meta`'s own fango code assembles the records and lists a deriver
walks. That is what keeps `internal/natives` below the interpreter in the
package graph. `Meta` therefore carries its own small `Items` list rather than
importing `List`, which derives its instances and so depends on `Derive`,
which depends on `Meta`.

There is one representation, and it is deliberately not a fango-level mirror
of `internal/ast`: a quote compiles to a compiler-side template — the quoting
module's own resolved AST, retaining its original spans — plus an ordered list
of hole expressions. `internal/meta` owns the template table and the opaque
`Code` value the compile-time evaluator produces. Retaining the quoting
module's spans is what lets a type error in generated code point at the line
the generator's author wrote.

Hygiene is close to free, and it is the good kind: name resolution rewrites
every module-level declaration and imported reference to a canonical
`Module.name` symbol before inference, so a quote already holds fully-resolved
identities and resolves in the quoting module's scope, never the splice
site's.

Two positions are tracked rather than one signed depth, because they answer
different questions. Quote depth decides whether a `$(…)` is a hole; stage
depth decides whether code runs when the program runs or while the compiler
runs. Each reaches exactly one. Local binders belong to the position they were
introduced at and may be used only there; top-level definitions are
stage-polymorphic, which is the one piece of cross-stage persistence the
design keeps.

A quote is a Core node rather than a call to a bundled primitive. Only the
interpreter ever executes one, and only while a splice is running: the Core
linter rejects a quote outright, which turns "compile-time values do not reach
generated Go" into a checked invariant rather than a convention.

That invariant rests on the compile-time-only type rule: a definition whose
type mentions `Meta.Code` is not emitted, and no expression in any other
definition may have such a type. The rule is load-bearing rather than tidy,
because `internal/codegen` has no dead-code elimination — every `Prog.Def` is
emitted so that adding a downstream consumer cannot change a dependency's
generated package. It is purely type-directed, so it needs no reachability
analysis, and it does double duty by keeping code values out of the running
program. The type itself is not emitted either, for the same reason `Bool` is
absent from `ADTOrder`: no Go type corresponds to it.

Running a splice needs elaboration and the interpreter, which both sit above
inference in the package graph, while the splice must be expanded during
inference. `internal/staging` fills that seam: the checker holds a hook, and
both the batch pipeline and the REPL install the same evaluator. It
elaborates the already-inferred prefix on demand — the same per-declaration
path `internal/repl` uses at every prompt — so a program with no splices
elaborates nothing twice.

fango needs no equivalent of Template Haskell's stage restriction. Top-level
declarations are scoped in source order and forward references are rejected,
so a splice can only name declarations that are already checked, and
`(*Checker).InstanceDecl` appends in source order. Declaration cutoffs preserve
the same concrete evidence in prefix and final elaboration, while splice
operands use the instances visible at the splice site. A splice at depth 0 is
expanded before Core exists, so prefix elaboration cannot re-enter the
evaluator and needs no reentrancy guard.

Reproducibility is enforced rather than assumed. Compile-time code must type
with an empty residual effect row; a locally handled abort-only effect is
therefore allowed, while unhandled effects remain prohibited. Thus "no IO
during compilation" follows from the effect system. Beyond purity, the
interpreter runs splices in a restricted mode: natives carry a compile-time-safe
flag. Seeded `Random` transitions are
pure over handler-local state and are safe at compile time; system entropy is
not. User Go sidecars are unavailable for the reason they always were: the
interpreter cannot load Go. A step budget bounds evaluation. Together these
keep generated Go byte-identical across builds.

The bundled `Meta` module is a dependency only of files that use the syntax:
the parser records whether it built a quote, splice, or `typeOf`, and the
loader adds the edge from that; a `deriving` clause adds the same kind of edge
to `Derive`. `Meta` imports only `Basics`, which imports nothing, so neither
edge can close a cycle. Their compile-time-only types, class dictionaries,
instances, and helpers are excluded transitively from runtime Core; `Derive`
emits nothing at all, and what survives of `Meta` is its `Items` list and the
folds over it, which are ordinary polymorphic code.

The bundled `Json` module exercises that boundary without declaration
generation. Its `Encode` deriver is ordinary fango over `Meta.TypeInfo` and
quoted expressions; only JSON string escaping, finite-float formatting, and
leading-string-token validation cross the native boundary. Derived records
emit fields in schema order, and derived unions use one tagged representation,
so emitted text is deterministic. Decoding remains schema-specific fango code
in the Todo example rather than a compiler facility.

A failed expansion rolls back. `(*Checker).Checkpoint` restores the checked
prefix and installed capture summaries along with the rest of the declaration
environment, and tells the
compile-time evaluator to discard an environment that no longer describes it —
which is why a REPL `deriving` clause whose deriver fails leaves no type
behind.

## Core and evidence invariants

Core is the compiler/interpreter contract. Every definition and expression is
explicitly typed; generic definitions declare type parameters and uses carry
explicit type arguments. Application nodes record whether their callee is a
worker, constructor, primitive, operation, or indirect function. Matches are
decision trees rather than surface branch lists. One reusable pattern-matrix
compiler handles `case`, function equations, lambdas, handler groups, and
destructuring. It accepts multiple argument columns, preserves source order for
overloaded literals and pins, reports witnesses and redundant rows before Core
is emitted, and lowers record views into constructor columns. Function and
lambda workers bind deterministic hidden parameters and enter the tree only
after the final syntactic application, preserving partial-application and
effect timing. A definition of one identifier-only row is the exception: it
keeps its source parameter names and needs no tree, so existing Core output
and the optimizations that require plain forwarding parameters are unchanged.

Class dictionaries are compiler-internal single-constructor ADTs with typed
method-function fields. Qualified workers receive leading dictionary
parameters, and conditional instance factories receive their context's
dictionaries. Projection is an ordinary typed Core case; construction and
calls are validated by the existing constructor and application linter rules.
Known instances call their method workers directly. Exact scalar native
forwarders and identity implementations bypass the wrapper, preserving native
arithmetic and constant folding without a compiler-owned numeric capability.
Effect evidence precedes ordinary parameters in the Go ABI; dictionary
parameters then precede source arguments. Effects on a method arrow execute
when that arrow is applied, not when its dictionary is constructed.
Evidence arguments additionally carry erased capture metadata. A definition's
effect parameters bind symbolic captures; a handler body receives the concrete
scope capture; and callback adaptation substitutes a concrete capture before
erasing an open-row ABI. Core lint compares the complete lexical evidence
stack, so two active instances of the same nominal effect are not
interchangeable.
An instance implementation without syntactic parameters is checked for a pure
construction effect row; producing a function cannot conceal eager IO.
Instance methods carry their owning instance identity through the typed AST.
Its head is available as self evidence while checking the method; elaboration
can call that instance's workers or factory with its existing context without
performing polymorphic lookup or adding a public self constraint. Other
polymorphic method calls use given dictionary projections, including partial
applications and lifted locals. Scalar specialization never replaces them
with newly selected instance evidence.

After evidence elaboration, a bounded Core specialization pass emits Int and
Float variants of effect-free source workers with one numeric type parameter
and only standard scalar constraints on that parameter. Workers with internal
handlers also stay generic to preserve lexical evidence capture. Both variants are emitted
by the defining module independently of downstream uses. The pass substitutes
typed dictionaries in already-resolved Core, simplifies known projections and
native forwarding, and redirects only calls passing those exact dictionaries.
It never reruns instance selection or replaces arbitrary supplied evidence.
Generic workers remain available for custom instances and other uses. Strict
Let bindings preserve evaluation order and prevent argument duplication during
beta reduction; the final specialized program passes the same Core linter.

Integer patterns require `Num` and `Eq`. Int/Float patterns retain literal
decision trees; generic and custom numeric patterns use ordered guards calling
`fromInt` and `eq`. These preserve first-match semantics even when distinct
integer literals compare equal in a custom instance.
Their redundancy check uses the full preceding pattern matrix, conservatively
treating distinct overloaded literal tests as opaque; constructor coverage may
collectively make a branch redundant.
Pinned named values use the same ordered-guard path, require `Eq`, and never
claim coverage; identical pins are stable repeated tests and can establish
redundancy.

Saturated pure primitives are `NativeCall` nodes keyed by canonical declaration
name. `Prog.Natives` holds their schemes, arities, templates, modules, and
effect ownership; the linter checks arity and declaration instantiation rather
than switching on operator spelling. Effect primitives remain `Perform`, so a
lexical handler can intercept them before their native boundary default.

Effectful Core uses `Perform`, `Handle`, `Resume`, `Seq`, and `Bracket`. Open source row
tails are erased after evidence requirements have been derived; concrete labels
remain on first-class arrows as their indirect-call evidence ABI.
Hidden evidence parameters precede ordinary worker parameters in deterministic
effect-identity order, and calls supply matching lexical evidence. The Core
linter rejects unsolved metavariables, malformed generic applications,
callee/evidence disagreements, invalid handler coverage or types, and residual
open rows before either backend runs.

`Bracket` is the cleanup-scope node: a scope identity, a resource binder, and
acquire, release, and body expressions, each of which may produce control.
Elaboration is its only producer, and it appears only as the body of the
`Scope.bracket` intrinsic, whose checker-declared signature it replaces; Core
lint re-establishes that, along with the node's scope uniqueness, its release
being Unit-typed, and its control being the join of its three children, since
a scope forwards every exit it intercepts rather than consuming any. Both
backends erase the scope identity.

A compiler intrinsic is a bundled `native` declaration the compiler implements
as a Core node. It is recognized by resolved canonical name, is absent from the
native table so nothing can lower it to a `NativeCall` or look for a Go
sidecar, and its annotation is resolved in the ordinary annotation scope
because its parameters are fango functions over an open row rather than scalars
crossing a Go ABI. The checker also gives a saturated intrinsic application a
bespoke rule where source row syntax falls short: each callback's effects are
required to be *available* where the scope runs rather than equal to the
scope's own row, which is what lets one scope acquire with `IO` and fail in its
body. An unsaturated application keeps the ordinary, stricter rule.

Core also retains the control convention on every executable boundary.
Transport-polymorphic operations and calls are ANF-hoisted whenever they occur
in an operand, argument, guard, record/constructor field, handler prefix, or
return transformation. The Exit emitter therefore tests an `Outcome` before
the next source expression. Core lint independently checks the convention on
callee and evidence slots, validates private `ControlExit` producers against
their operation descriptor and lexical target scope, rejects Machine Core, and
checks that no control-producing expression remains in an unhandled expression
slot. Core dumps print non-Direct conventions so ABI choices are reviewable.

An effect-polymorphic higher-order worker has its open callback row erased from
the runtime ABI. Passing a concrete callback therefore adapts it to that ABI;
local function references are eta-expanded so their binding keeps its concrete
type while the wrapper retains the callback's execution, evidence, captures,
and per-arrow control behavior.

## Go backend and runtime

The compiler emits formatted Go source and invokes the supported `go build`
interface. Direct integration with Go runtime internals is intentionally
avoided: its ABI, stack maps, barriers, and scheduler metadata are compiler
implementation details.

Representations are type-directed rather than uniformly boxed: `Int` is
`int64`, `Float` is `float64`, `String` is a valid UTF-8 `string`, `Char` is a
Unicode-scalar Go `rune`, and `Bool` is `bool`.
The bundled `List` is the one declared type with a representation the backends
know: `fangort.List`, a spine of fixed-size inline arrays filled downward
behind a two-word value. It is recognized once, by canonical symbol and
validated shape, when its constructors are resolved, and everything below that
compares nominal identity; a user-declared cons type is a different type and
keeps the ordinary lowering. Its module emits no marker interface and no
constructor structs, construction is a runtime call, and its decision-tree node
is an emptiness test with head and tail projections rather than a type switch.
It needs no `Exit` family member, because its own constructor fields cannot be
controlled and the Direct/Exit distinction rides entirely on the element type
argument. Its compiler-derived eq and show keep the exported names and generic
signatures an emitted pair would have and delegate to the runtime, so
element-operation synthesis at call sites is representation-blind.
A chunk's watermark records how far it has been filled and only ever decreases,
and a cons claims the slot below it only when the list it extends still owns
that frontier; every value's offset is at or above its chunk's watermark when
created, so a cons is invisible to every value that already existed. Persistence
therefore costs two comparisons rather than a copy, and no cons is ever worse
than a cons cell. The interpreter uses the same runtime type, so the two
backends agree by construction rather than by two implementations matching.
Concrete Unit parameters and results at direct worker and operation boundaries
are implicit in generated Go: the parameter is omitted and the result is a
void result. Unit remains a represented, runtime zero-sized value at
first-class-function, polymorphic, ADT, and handler-closure boundaries, where
Go's type system requires a value — a handler's return transformation yields
the singleton rather than the void return its enclosing worker would use;
`fangort.Unit` and `fangort.UnitValue` give every generated
package the same nominal representation instead of repeating anonymous
composite literals.
Erasing a Unit argument never erases its evaluation: expression lowering keeps
strict left-to-right order, materializing the singleton only when a value is
required. Functions are typed Go functions, and ADTs use typed interfaces and
constructor structs. Parameterized definitions map to Go generics with
explicit instantiation; row-kinded ADT parameters are omitted from those
runtime generics and their arguments are represented by Unit. Class
dictionaries, instance factories, and generated
deriving methods use the same typed, exported internal ABI as ordinary ADTs
and workers. Definitions belong to their source module, so adding a downstream
consumer does not change the dependency's generated package.

Direct workers and operation evidence retain the plain-result ABI, including
implicit Unit results. Exit workers and evidence return
`fangort.Outcome[A]`, whose nil request denotes `Normal(A)` and whose request
contains a dynamically unique lexical target, checked effect/operation
descriptor identities, and an erased payload checked before boxing.
Propagation constructs an Outcome at
the caller's result type; it never drops an exit, converts it to a panic, or
uses a failed host type assertion as a language type check. The interpreter
implements the same outcome propagation with a dedicated language-level
`ExitRequest` value kept separate from evaluator errors. Abort-only source
operations are the checked producer of this protocol. When Exit code calls
Direct evidence, code generation builds a typed eta record whose operation
fields wrap normal results; conversion in the other direction is forbidden.

A cleanup scope emits as straight-line Go. Its Direct family member acquires,
runs the body, discards the release result, and returns the body value with no
`Outcome` plumbing at all — the roadmap's "plain result variant" falls out of
the existing ABI families rather than needing a flag. Its Exit family member
tests each slot's `Outcome`: a failed acquisition propagates without releasing,
and once the body has run, a release that also failed is joined onto the body's
request through `fangort.Suppress`, which copies rather than editing an exit
the scope is only forwarding. Go `defer` is deliberately unused: a release must
be ordered against the body's result, not against a function literal returning.
No scope allocates a continuation, starts a goroutine, or tests a
consumed-state flag.

The backend emits one Go package per Fango module beneath a single generated Go
module. The entry module is the root `package main`; local and bundled
dependencies use their logical layout below `modules/`. Cross-package values,
workers, ADTs, constructors, effect evidence, and derived operations use a
typed compiler-internal exported ABI. Direct source imports remain Go import
edges even when unused, and generated types may add an import of a transitive
type owner. Package aliases and batch lambda-lifted names are deterministic and
independent of graph-wide identity allocation, so unchanged source units emit
byte-identical Go. Module separators and operator characters are the only
characters a canonical symbol can hold that a Go identifier cannot, so symbol
mangling spells each as a word: `Basics.++` emits as `v_Basics_dot__plus__plus_`.
Words rather than a hash because generated Go is meant to stay readable, and
the substitution cannot collide with an ordinary name because identifier and
operator characters are disjoint sets and a fango name may not begin with `_`. Project emission is the backend's only generation path;
tests inspect the same package files used by `build`, `run`, and `--emit-go`.

A self-recursive tail call of a top-level worker compiles to a loop: when the
shared predicate in `internal/core` accepts a definition, its body emits as
one `for` statement whose rewritable self calls become a simultaneous tuple
reassignment of the changed parameters plus `continue`, giving the
loop-by-recursion idiom constant stack. Eligibility is a pure function of the
definition and call node — no Core marker exists: the call must be reached
exclusively through the tail skeleton (Let bodies, If branches, Case leaf
bodies, Seq tails; never lambda bodies, handles, right-hand sides, scrutinees,
or guard conditions), name the worker itself at the identity type
instantiation (polymorphic recursion is unsound to reuse frames for), and pass
the worker's own evidence parameters through unchanged — structurally
guaranteed because the skeleton never crosses a Handle, so evidence needs no
per-iteration work. The soundness core is capture exclusion: generated Go
closures capture locals by reference, so a definition is ineligible when any
lambda body or handler clause/return mentions a parameter the recursion
mutates; parameters passed through unchanged stay capturable, which keeps
eta-expansion wrappers around threaded callbacks from disqualifying driver
loops. The loop body comes from a dedicated walker that mirrors the ordinary
return-position emitter — ineligible leaves and non-tail subtrees emit
identically, and a `continue` can never leak into a func literal — while
handling `Seq` tails directly (no IIFE inside loops) and erased Unit
arguments through the same ordered temporary prelude as saturated calls, so
argument evaluation order survives the rewrite. Trivial arguments (a
parameter passed to itself, including passed-through dictionaries) elide from
the jump; a fully-trivial jump is a bare `continue`, and a `for` with no
`break` is a terminating Go statement, so infinite loops need no unreachable
trailing return.

The build driver materializes the package tree and embedded `fangort` beneath a
persistent `.fango/build` directory, writing only changed files and removing
only stale package-source paths recorded in its generated-file manifest. It
also writes `sources.json`, a deterministic dependency-first manifest of
logical names, local root-relative or `<stdlib>/...` paths, and SHA-256 content
hashes. Every source edit and graph change triggers a Go build even if
generated Go is unchanged. Go's package cache then reuses unchanged compilation
units. `build` copies the resulting executable; `run` reuses it while inputs
are unchanged.
`fangort` owns genuinely shared runtime facilities: represented Unit,
Direct/Exit outcomes, formatting and the generic native-host contract.
Module-specific native logic lives in the owning stdlib sidecar instead. In
particular, `IO.native.go` owns IO operations and line semantics, while
`Random.native.go` supplies entropy acquisition. The pure PRNG transition and
range functions are ordinary Fango code, and the changing deterministic seed
belongs to each Fango handler activation rather than to a native process
global.

During ordinary interpretation, every call-form sidecar runs in one persistent
native worker per sidecar set. Its protocol and execution loop are ordinary Go
packages shared with the interpreter; generation supplies only the sidecar
imports, function registry, and assignments to each package's `FangoHost`.
Those support sources are embedded and materialized into the worker's private
Go module, with their repository imports rewritten through the Go AST. The
worker cache hashes the sorted destination paths and exact bytes of that whole
module, so a change to fixed support code invalidates it just like a sidecar
change. A framed scalar protocol carries calls and reverse `FangoHost` requests
over a dedicated loopback connection, leaving process stdio outside the control
channel. One process preserves package-global state across calls. The active
interpreter IO context answers host requests, so the REPL's prompt and native IO
share one buffered reader. The same mechanism accepts bundled and user
sidecars; bundled sidecars deliberately use it in the REPL. Native code is
trusted and retains the user's OS privileges—the worker is lifecycle isolation,
not a security sandbox. It recovers a panic only to report and reproduce it in
the interpreter; a host exit becomes the interpreter's exit error.

The in-process native registry is limited to inline templates, compiler-only
representations, and explicitly safe bundled behavior needed during splice
evaluation. Compile-time evaluation still rejects user sidecars, effects, and
process-state-observing natives.

## Interpreter and REPL

`internal/eval` executes Core, not the surface AST. Values have a uniform Go
representation in the interpreter, while environments distinguish typed
workers from lazy memoized top-level cells and eager block frames. `EvalIO` and
`ForceIO` are the explicit IO entry points. The interpreter and generated Go
share observable formatting rules. They also share the runtime types `fangort`
owns — represented Unit and the bundled `List` — so a representation the
backends know is implemented once rather than mirrored; the interpreter's value
switch and generated Go's representations must agree, and the differential
suite is the referee. Structural equality tests for a list before its scalar
fallback, because that fallback is Go `==` and a list value is deliberately not
comparable.

The interpreter shares the compiled backend's tail-call predicate: applying a
worker the predicate accepts runs a frame-reuse loop instead of recursing
through Go `eval` frames. A tail-skeleton walker either produces the
iteration's final value or the next iteration's parameter frame, built by
evaluating a rewritable call's arguments in the current frame; the decision
tree evaluator takes a leaf callback so both paths share dispatch. Fresh
per-iteration frames would make interpreter closures safe without the capture
exclusion, but the shared predicate keeps both backends optimizing the same
set of definitions — the set the reference documents — with the differential
suite as referee. The loop polls cancellation each iteration, since a
fully-trivial spin (`f x = f x`) would otherwise never reach the every-N-evals
poll. Eligibility is cached per definition pointer, so REPL redefinition
invalidates naturally.

`print` is an ordinary `Show`-constrained function implemented using `show` and
the native `IO.write` operation. Tooling observes values through the same Show
evidence, evaluating the observed expression once. Values with no resolvable
Show instance have an opaque typed placeholder; functions show `<function>`.
Strings and Chars are displayed raw through `Show`, including inside explicitly
derived ADT displays; tooling uses quoted source-literal forms.

The REPL retains one checker, type/name supply, evaluator environment, and IO
reader/writer across inputs. Prompt definitions become lazy memo cells;
functions become workers. Redefinition installs a new generation, and existing
memoized values and closures keep their old bindings. Multiline input is driven
by parser incompleteness and layout. Effectful ordinary prompt declarations
are rejected, while effectful expressions run directly. Function definitions
are installed without executing their bodies and run only when applied.
Class and instance declarations are supported. Failed instance or deriving
declarations roll back the persistent environment, without reusing allocated
identities. Class redefinition is rejected; type redefinition creates a new
generation with independently installed instances.

Loading, reloading, cancellation, and interactive line history are not yet
implemented; see [REPL hardening](roadmap.md#repl-hardening).

## Testing and performance

Lexer, parser, inference, elaboration, and REPL behavior use unit tests and
goldens. Every runnable fixture is evaluated through Core and, outside short
mode, compiled through the real CLI; output is compared byte-for-byte with its
expected file and between backends. A fixture or example may carry a `.stdin`
transcript beside its source; both backends receive it as scripted standard
input. Stateful command examples run a sequence against isolated working
directories, with the interpreter's explicit argument/directory context
matching the compiled process's argv and working directory. Invalid fixtures
pin diagnostic substrings.
Focused inference and elaboration harnesses install the actual embedded
prelude and its transitive bundled dependencies rather than a parallel
test-only environment.
Generated Go is checked for deterministic, gofmt-idempotent output. The Core
linter runs in every batch compilation.

The runnable fixtures' compiled legs share one generated Go project: each
fixture's entry module is emitted as its own package, the bundled and
dependency packages they emit are written once and asserted byte-identical
across fixtures (a package's generated code must not depend on its consumer),
and a single Go build produces every fixture binary. The build starts in the
background so it overlaps the interpreter legs, and the cases then run in
parallel with no per-case build work. The examples and the multi-module
fixtures instead compile through the real CLI, each in a private build
directory, so `fango run` itself stays covered end to end. Interpreter legs
are serialized because native workers and host contexts are process-level
test infrastructure. Handler-local State, Writer, and seeded Random
activations themselves do not share mutable process state.

Compile-latency benchmarks track cold and warm paths against recorded,
machine-specific baselines. Runtime benchmarks compare representative scalar,
match, string, list, tree, and repeated handler-state operations with
handwritten Go and use per-case ratio ceilings. The State baseline uses the
same one-cell/two-closure setup so its timed loop isolates per-operation
overhead from handler setup. These measurements arbitrate representation or
optimization work. The three list cases use the bundled `List` rather than a
private cons type, so they measure its array-backed representation; one of them
is a branching workload, because that is the shape where an array-backed list
pays rather than wins, and `tree` keeps a pointer-ADT case so the suite still
witnesses that not every data structure became a slice.

Both gates assert on elapsed time, so neither is a correctness gate: a busy
host fails them without anything having regressed. They live in their own
package, `make test-perf` runs them deliberately, and nothing automated —
`make test`, `make ci`, or continuous integration — depends on them. Neither
is reproducible enough to change that yet. The latency gate compares against
absolute milliseconds recorded on one machine, so it only means anything
there. The runtime-ratio gate builds its handwritten Go baseline on whichever
host runs it, which removes the machine dependence but not the load
dependence: the ratio moves with whatever else is competing for the CPU.

## Known limitations

The implementation has a deliberately narrow scalar Go sidecar FFI but no
package manager, transparent aliases, formatter, or LSP. Type classes have one
parameter, no superclasses, higher kinds, default methods, ambiguous overlapping heads,
or method-local polymorphism. A constraint on a parameterized type is not
simplified to constraints on its arguments, so `Eq a => List a -> Bool` is
rejected in favor of `Eq (List a) => …`. There are no source-path
flags, external library version selection, or package resolution. The implicit
prelude is fixed to hidden `Basics` plus ambient `IO`, with `Meta` added only
for files that use the staging syntax and `Tuple` always, since tuple syntax
must resolve wherever it parses; the bundled standard
library is intentionally small and experimental.
Compile-time metaprogramming has quotes, splices, type reflection, and
derivers, but no declaration splices, so generation that must introduce a
top-level name is not expressible. Reflection reads a schema and compares type
identities; it cannot ask whether an instance exists. A stdlib parameter name
that collides with an entry file's effect operation is rejected as shadowing,
because effect operations are declared before any module's values.
Integer values are signed 64-bit; broader numeric semantics are not settled.
Self tail calls run in constant stack, but mutual recursion and monomorphic
local recursive closures do not. Custom handlers have the
restrictions described above, and ambient IO cannot be re-handled yet. REPL
loading/reloading and cancellation remain unfinished.
