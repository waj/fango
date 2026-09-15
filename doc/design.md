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
declaration, not because anything in the compiler counts. `Tuple` is a syntax
root — always resolved, never in scope — since syntax that always parses must
always resolve, including in the REPL.

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

Scopes carry their own `ScopeID`, and a capture-capable acquired resource
binds it. The same non-escape proof covers borrowed resources and scoped
handler activations. A scalar resource, including the Unit resource of
`Scope.finally`, adds no capture to its borrowed value.

Capture capability follows type shape: functions and type variables can carry
captures, scalars cannot, and algebraic data can when a field can. A nominal
type marked `{-# resource #-}` always can, independently of its fields.
The marker belongs to the following type declaration and survives resolution
as nominal ADT metadata. Modules must export such types opaquely; constructors,
record fields, and reflected schemas remain private. The defining module and
native implementation own representation correctness. The marker itself does
not acquire or release anything. `File.Handle` and its private directory handle
use this declaration mechanism, with no canonical resource-name registry.

Resource wrappers infer contracts from their implementation over `Scope.bracket`.
A wrapper exports the callback's lifetime obligations instead of requiring
a capture-free result type or receiving a compiler exemption. A returned
closure is accepted when it does not capture the acquired resource. Stores
through outer handlers are checked even when the computation returns Unit;
an inner owner may retain an outer resource only within that resource's lifetime.
An outer resumptive handler may borrow synchronously without retaining its
argument. An abort payload cannot carry a resource across its cleanup boundary,
since the destination clause executes after cleanup.

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
effect remain distinct. The generated envelope records the canonical effect
name rather than a compiler-local numeric identity, keeping module output
stable across import graphs; dispatch uses the activation target.

Capture contracts are separate from effect rows. Alongside symbolic result
capture sets, each definition exports a finite capture-flow graph that erases
scalar computation and preserves calls, callback invocation, constructor fields,
handler interpretations, state updates, and scope obligations. Abstract callback
and evidence requirements remain in this graph until callers supply their
interpretations. Definitions, schemes, module increments, and REPL checkpoints
retain the contracts; source annotations do not erase them.

Contract checking substitutes actual callbacks and evidence and joins branches.
An allocation-site abstract heap tracks closures and constructor fields; closures
retain free values and definition-site evidence, excluding their own binders.
Recursive calls join enclosing contexts at a repeated target and lexical call
site and iterate to a fixed point, without
an iteration-limit success fallback. Separate acyclic call paths distinguish
nested owners. Folded recursive activations cannot establish that two dynamic
owners are identical, so retention requiring that equality is rejected
conservatively. Concrete scalar results cannot carry captures. Pattern matching,
partial applications, dictionaries, lifted locals, and row adapters preserve
the same flow obligations.

Source-declared stateless effects remain durable by default, preserving
existing Reader closures. A parameterized handler is scoped regardless of its
nominal effect's default policy. Compiler-owned state/resource APIs may also
mark evidence scoped, mark an
operation result as borrowing its evidence, or mark an operation as retaining
arguments. A scoped handler rejects a result that transitively retains its
activation, including a closure hidden in an ADT or one passed through another
worker. A retaining operation also rejects storing an inner scoped capture in
longer-lived evidence. Clause evaluation propagates actual operation payloads,
resumed results, state snapshots, and abort answers. Core lint independently
reconstructs capture-flow graphs, rejects missing or stale checked contracts,
recomputes result summaries, checks
scope introduction and exact evidence-stack availability, and repeats the
non-escape proof after ANF, lifting, callback adaptation, and specialization.
The bundled State, Writer, seeded-Random, and resource runners use these same
contracts; there are no trusted runner or forwarding-name exemptions.

Execution transport is compiler metadata distinct from both effect rows and
operation discipline. Each Core arrow, definition, lambda, application, and
effect-evidence slot records a `Direct`, `Exit`, or reserved `Machine` lower
bound, plus whether the mode is selected from its enclosing control context.
An open row or abstract custom-effect evidence is transport-polymorphic even
when the source type has no ordinary type variables: a handler interpretation
may perform a non-local exit before returning the operation's apparent result.
The metadata is inferred from solved rows and supplied evidence, not by
scanning a function body for a particular operation name. It belongs to each
curried arrow independently, preserving early-effect and partial-application
timing. `Machine` is represented in the contract. Ordinary Core lint and the
ordinary backends reject it; the private selective-machine entry point admits
it only after the dedicated pre-machine checks described below.

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

A statically Direct call that constructs an Exit-family value selects a
transport-polymorphic worker's Exit member, widening any Direct evidence.
`RequireNormal` projects the result under the Core Direct contract and rejects
an unexpected exit as a compiler invariant violation. This preserves the
joined module-owned ABI without discarding a real exit or adding a variant
for each combination of callback and result representations.

## Compiler pipeline

The batch pipeline is:

```text
source -> lexer -> parser -> AST -> inference -> typed AST
       -> elaboration -> Core -> Core lint
       -> optional selective machine lowering -> Go AST -> go build
```

Selective machine lowering is activated only when the resolved bundled
`Generator.withIterator` intrinsic is in the program. The ordinary path skips
it entirely. The REPL performs the same lowering over the exact displayed Core
expression before evaluation, so its Machine-lambda identity table matches the
expression the interpreter receives.

Splice evaluation lowers the exact operand and its reachable definition closure
through a stage-specific semantic-Core lint entry and the same Machine IR.
That entry admits checked `Quote` and reflected constants; the emission lint
entry still rejects them. Reachability matters while deriving: a dictionary
whose methods are still being expanded is not a finished executable definition.
The stage environment retains elaborated definitions for capture substitution
and lowering, installs newly imported intrinsics incrementally, and rebuilds
that state after rollback discards an installed declaration prefix.

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

The default scope is itself a bundled module. `Prelude` holds nothing but
imports — the loader rejects a declaration in it — and a module that does not
carry `{-# no-prelude #-}` resolves as though that import list stood at the
top of its own file. They are ordinary imports, qualifiers included, so the
file says exactly what it means. Keeping the list in fango is what stops the
batch resolver and the REPL's scope from drifting: both read it, neither
restates it. The prelude declares nothing, so it emits no Go package; it stays
in the manifest, where its hash invalidates a build when the default scope
changes.

The bundled standard library sits below the prelude and carries the pragma,
which is also what keeps it out of a cycle: `Prelude` imports `IO`, which
imports `Basics`, so an implicit edge back into those would close one. Those
modules therefore write the imports they need.

The embedded prelude used by the REPL and focused checker tests follows the
same bundled dependency closure instead of maintaining a parallel module list.
Its roots are `Prelude` and the modules surface syntax desugars into — `Meta`,
`Derive`, `List`, and `Tuple` — since a later prompt can quote, derive, or
write `[1]` or `(a, b)`, and syntax that always parses must always resolve.
Rooting those puts none of their names in view. Ordinary imports and the same
syntax-driven edges recursively add their dependencies. The checker retains
that resolved owner set so fixture projections can omit the whole prelude
while still elaborating and linting it. Bundled modules therefore use the
public standard-library types rather than private substitutes.

Module discovery, validation, interface building, ordering, and resolution
live in one persistent graph (`modules.Graph`). A batch build fills a fresh
graph from the entry file in one step; the REPL keeps its graph for the whole
session, seeds it with the prelude closure, and grows it import by import.
Every node the graph holds is already resolved, so a new module — or a new
prompt — resolves against the committed nodes without touching them, and an
import that fails anywhere commits nothing: the operator table is extended on
a copy and the pending nodes are dropped. The checker tests that have no
resolver still bind the prelude's exposed surface names directly into the
checker tables; the REPL does not, because its resolver canonicalizes every
prompt and a surface spelling in the tables would let an unresolved name slip
past the scope the resolver enforces.

An import increment is elaborated under the rules a program gets: a compiler
intrinsic the increment declares — `Scope.bracket` when `Scope` or a module
over it is imported — has its synthesized definition installed with the
increment, the increment's native metadata reaches the evaluator so a sidecar
call applies its boundary shapes, and the capture analysis of every prompt
input, declaration or expression, runs with the session's installed
definitions as context. That last point is what makes the call-site rules for
`State.run` and `File.withFile` fire at the prompt exactly as they do in a
file; without it a prompt could hand a handle out of its scope.

Primitives are declarations rather than a compiler catalog. The prelude names
the bundled `Basics` module, whose native values define the scalar
implementations of class methods, and the bundled `IO` module declares the
ambient IO effect and its operations. Basics also declares the standard operators and
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
through the same scope, and `Prelude` exposes the ones `Basics` declares so
arithmetic needs no import. A module's scope is built by applying the
prelude's imports and then its own, through one code path, so an implicit
import differs from a written one in nothing but where it is written; a
repeated binding at an identical canonical name is accepted, which leaves a
module free to import a prelude module again for more names. The resolved
modules are merged in graph order and checked
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

Applications separate callee-shape equality from directional argument
compatibility. Callback effects are included in the permitted parameter row;
results are covariant and function inputs contravariant. A fresh expected type
gets its own row view rather than borrowing a named value's closed row. Shared
arguments, collection elements, and branch results can therefore accumulate
their combined effects without retagging the original binding. Shape constraints
are solved before the resulting row bounds; unresolved flexible relationships
use the existing shared-row schemes rather than introducing row inequalities
into the source language. Class-constrained types remain invariant.

Nominal variance is the least fixed point of positive and negative parameter
occurrences through constructor and record fields. Mixed occurrences and effect
label arguments are invariant. The checker has module schemas even when their
constructors are private, so abstract imports obey the same variance proof.
Row kind alone never implies covariance. Definition annotations still compare
known arrow effects exactly before annotation equality can populate inferred
rows; local definitions use the same check.

A pure handler runner that consumes controlled callbacks retains a polymorphic
transport contract independently of its source effect row. The checker derives
that requirement from controlled parameter types and executed call contracts,
including calls inside handlers, while excluding latent lambda bodies. Handling
an effect does not convert a caller's Exit-family callback to a Direct value.

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
crossing a Go ABI. Intrinsic callbacks use the same directional argument
checking as ordinary calls, including partial applications. Their special rules
concern ownership and Core lowering, rather than callback row equality.

Core also retains the control convention on every executable boundary.
Transport-polymorphic operations and calls are ANF-hoisted whenever they occur
in an operand, argument, guard, record/constructor field, handler prefix, or
return transformation. The Exit emitter therefore tests an `Outcome` before
the next source expression. Core lint independently checks the convention on
callee and evidence slots, validates private `ControlExit` producers against
their operation descriptor and lexical target scope, and checks that no
control-producing expression remains in an unhandled expression slot. The
ordinary lint entry rejects Machine Core unless the resolved
`Generator.withIterator` intrinsic activates the private owner boundary. A
separate pre-machine lint entry admits compiler-only `Suspend` nodes and
Machine transport while retaining the other semantic Core invariants. Only
the resolved bundled `Generator.yield` operation can produce `Suspend` during
source elaboration. Core dumps print non-Direct conventions so ABI choices are
reviewable.

Selective machine lowering has its own typed execution IR in
`internal/machine`, below semantic Core rather than mixed into it. It selects
only concrete Machine roots and the transport-polymorphic workers they reach in
Machine context. Direct and Exit definitions are absent from the result. The
lowerer splits ANF `Let`, `Seq`, and `If` computations at compiler-only
suspensions and known Machine worker calls, represents shared branch
continuations once, and marks calls whose continuation is only the caller's
return as tail transfers. Every block has one explicit terminator and typed
result binding.

Backwards fixed-point liveness materializes `LiveIn` and `LiveOut` sets on each
machine block. A worker frame contains the union of locals live across a
suspension or non-tail Machine call; the result supplied by that transition is
defined on the outgoing edge and is therefore not spuriously saved. The
machine linter independently recomputes those sets and the frame layout, checks
block reachability and successor validity, proves a single cleanup depth at
every normal CFG join and zero pending worker-owned cleanups at return, and
validates local, call, result, and control types.

Machine frame fields and factory parameters use the same Machine value
representation as their step bodies, including polymorphic callbacks. An open
callback row in an otherwise unselected definition does not itself create a
Machine root; concrete Machine closures and calls from selected workers do.
Importing a producer elsewhere therefore does not add speculative closure
frames to unrelated Direct/Exit dependencies.

Two private backends consume that IR. The in-process evaluator owns an explicit
slice of machine frames and uses the recursive Core evaluator only for a
non-Machine expression that finishes before the next transition. The Go
backend emits a module-owned typed frame with a PC, parameters, and precisely
the computed cross-transition locals. Its `Step` method runs local blocks in an
inner loop and returns only to request suspension, push/replace a frame, return,
or exit; the shared `fangort.Machine` dispatcher alone invokes steps. A tail
Machine call replaces the active frame. A non-tail call pushes one, and a
return passes its value through one erased runtime register before the typed
caller stores it. Generated module boundaries use exported frame constructors,
so the runtime imports no generated package and the source-module DAG remains
intact. Tail-call constructor arguments, captures, and evidence are evaluated
before the dispatcher clears and replaces the active frame, preserving source
snapshots. The runtime frame slice contains interfaces pointing to separately
allocated typed frames, and the interpreter slice contains pointers to
separately allocated frames; handler boundaries retain integer depths. Neither
backend retains pointers to slice slots, so append growth may relocate the
buffer safely. Machine statistics expose maximum live depth and frame-buffer
capacity in addition to cleanup and state high-water marks.

Both consumers clear completed frames. At suspension and non-tail call
boundaries the evaluator deletes locals outside `LiveOut`, while generated code
zeros the typed frame before copying back only live fields. The runtime also
owns a LIFO stack of synchronous cleanup closures: suspension leaves it intact,
normal completion drains it, and an exit drains it while retaining that exit as
primary and appending cleanup failures in inner-to-outer order through the same
`Suppress` operation used by synchronous `Bracket`.

Producer machines in the interpreter share their caller's execution policy and
step counter. Core evaluation, tail loops, and Machine dispatch all charge that
counter, including computations that loop without yielding. Starting or reopening
a producer cannot reset a compile-time budget or enable a stage-forbidden native.
Each dispatch restores the caller's evidence after suspension or completion.
Evaluator errors terminate production and drain all registered synchronous
cleanups, retaining cleanup errors while continuing outer release attempts.
Terminal paths clear frames, states, handlers, and suspension storage; a cursor
whose production failed remains exhausted. Protocol errors in the generated
runtime likewise abandon and clear the producer before returning an error to
its internal driver.

The implemented private lowering covers strict bindings and sequencing,
conditionals, constructor/literal decision trees with edge-specific field
bindings, generic evidence-bearing known-worker calls, indirect callbacks,
direct/Exit Core expressions, and the compiler-only suspension point. Machine
handler bodies and resumptive or abort clauses are separate typed workers;
their lexical Machine evidence is carried in frame fields. Stateful clauses
receive an opaque runtime state-cell token, and abort routing unwinds only to
the exact handler target before invoking its clause. `Bracket` scopes whose
acquire and release are non-suspending Direct or Exit expressions register a
synchronous release closure before entering their body, preserve it across
suspension, and pop it exactly once on normal completion. Exits retain the
primary/suppressed ordering while partially unwinding to an inner handler.

Capture-flow contracts also export acquisition and release non-suspension
obligations. They inspect actual callback bodies through helpers, stored values,
and definition-site evidence, independently of a widened callback row. Recursive
call summaries retain outward suspension obligations. A completed pull handles
its own producer's suspension, so synchronous traversal within acquisition or
release is allowed. Resumptive clauses remain part of the calling callback;
an abort clause outside it runs after unwinding and is checked outside that
callback's obligation. Core lint reconstructs these checks from executable Core
and rejects stale contracts.

Explicit abandonment consumes an unfinished private machine, runs all pending
cleanups, and clears its frames, handler activations, state cells, pending
result, and suspension marker. Cleanup failure becomes the abandonment
completion rather than being discarded. Source-level handlers retain their
existing tail-resumptive semantics. Ordinary source compilation selects the
private Machine backend only when the resolved bundled iterator owner is
present; no command or general source annotation selects it directly.

The pull-iterator runtime layer wraps that private machine in a pull owner. Each
`Next` drives the producer to one suspension and resumes a prior yield with
Unit; normal return ends iteration, while a tagged exit remains distinct.
`Close` abandons unfinished production and is safe to defer after normal
exhaustion. Generated code and the interpreter use equivalent owners. These Go
owners represent the abstract `Iterator.Iterator` resource. Ordinary Fango
helpers may pass them within their lifetime; only compiler-owned advancement
inspects their runtime state.

`IteratorScope` is the typed Core owner boundary and is produced only as the
body of the resolved `Generator.withIterator` intrinsic. It stores the
Machine-transport producer callback, the consumer callback, a fresh `ScopeID`,
the Yield evidence it supplies to production, and the opaque `Iterator.Iterator`
cursor type. Its visible control is the consumer's
residual control: the producer's latent Machine transport terminates at the
owner instead of infecting the caller. Selective lowering roots Machine
lambdas found inside Direct or Exit definitions as typed frame factories while
leaving those enclosing definitions out of the machine-worker island. The Go
backend and interpreter both start the resulting frame under a private pull
owner, pass that owner only to the consumer, and close it on scope exit. The
interpreter installs the lowering beside semantic Core and keys frame factories
by the original lambda identity, matching generated code's closure table.

An interpreter closure can retain both an ordinary body and a checked Machine
factory. The call chooses its execution protocol; storing or passing that
closure does not erase either representation. A closure created over mutable
Machine locals snapshots only the locals its body references, so pruning or
clearing the frame cannot invalidate the closure or retain unrelated locals.

The reserved `Generator.Generator` effect is the typed marker for this private
suspension path. It selects Machine transport and carries a lexical owner token
in an ordinary hidden evidence slot. Each cursor scope allocates a fresh token
and supplies it when invoking its producer. Workers, callback factories, and
captured evidence preserve that identity across suspension; no ambient current
cursor determines where a yield belongs. A canonical `Generator.yield` operation
elaborates directly to `Suspend`, whose owner evidence determines its destination,
whose request is the yielded element, and whose resumed result is Unit.
The bundled declaration and owning runner remain the activation boundary;
an unrelated effect or operation spelling does not acquire this lowering.
Source checking rejects ordinary handlers for this compiler-owned effect, and
Core lint independently rejects such handler nodes.

Core verifies source-yield evidence availability, scope ownership, and element
types. Capture contracts retain Yield identities through callbacks and evidence
substitution. Machine IR preserves the owner explicitly on each suspension and
independently checks its evidence binding, capture metadata, and request type.
Both dispatchers return the owner with the suspended request. The generated-Go
runtime also has an explicit cursor-advancement transition. Its dispatcher owns
an iterative stack of producer/caller transfers. A yield to an enclosing owner
parks unfinished inner advancements with that owner; their exclusive borrows
remain active until those advancements complete. Closing the parked traversal
drains inner producers before their callers and clears all transfer storage.
This runtime transition is not yet emitted by source lowering or implemented by
the interpreter. The source pull driver still accepts only its own owner's
requests. Host-driven Machine fixtures without a declared suspension
effect may still use ownerless requests with arbitrary resumed types.

Cursor ownership is part of the inferred capture-flow contract. `Iterator` is
an opaque declared resource; the owner supplies its fresh capability to the
consumer. Named consumers, aliases, constructor fields, stored callbacks,
dictionaries, and helper calls retain the same obligations. Returning the
cursor or a value that captures it, or retaining it in an outer handler, fails
the ordinary resource escape proof. Independent nested owner call sites stay
distinct even when they invoke the same bundled wrapper.

Terminal Core nodes carry explicit exclusive-advancement metadata. Contract
checking substitutes actual cursor identities and executes each producer's
contract under that cursor's exclusive borrow. Consumer callbacks execute after
the advancement finishes. Access summaries remain active at recursive joins;
a possible overlap reports `ITERATOR ADVANCEMENT CONFLICT`. Core lint checks
the scope and access metadata, reconstructs the contracts, and repeats the
proof, rejecting missing or stale contracts. These contracts support sequential
uses of a cursor, including reads after exhaustion. Raw `next` and compositional
suspension routing remain unimplemented.

`Iterator.forEach` and `Iterator.fold` have terminal Core implementations.
Their intrinsic bodies carry the callback and opaque cursor; `fold` additionally
carries a typed accumulator. Both backends repeatedly pull one value and invoke
the callback once in yield order. `fold` applies its curried callback as
`combine element accumulator`, matching `List.foldl`, and returns the final
accumulator. A terminal does not close the cursor itself: returning normally or
exceptionally transfers control back to the enclosing `IteratorScope`, which
closes or abandons the producer exactly once and combines cleanup failure with
the consumer exit. The fold contract joins accumulator values to a fixed point,
including callback effects reached through an accumulator from a prior iteration.

An effect-polymorphic higher-order worker has its open callback row erased from
the runtime ABI. Passing a concrete callback therefore adapts it to that ABI;
local function references are eta-expanded so their binding keeps its concrete
type while the wrapper retains the callback's execution, evidence, captures,
and per-arrow control behavior.

Function adapters also convert inputs contravariantly and returned values
covariantly. Nominal values with different stored runtime types are converted
through typed constructor/case reconstruction and recursive local helpers;
row-indexed values with identical erased types need no traversal. Local
references use their binding's actual Core ABI, including partial applications,
and Core lint independently rejects retagged worker parameters, lambda
parameters, and Let-bound values. Pure factories select callback adapters by
their representation family even when their execution protocol remains Direct.

A trailing final lambda is parser sugar for the existing application and lambda
nodes. Its body extends rightward to a layout boundary or enclosing delimiter;
list commas remain element boundaries. `Basics` defines `|>` and `<|` as
ordinary effect-polymorphic functions with precedence zero, exposed by `Prelude`.

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

A perform on a resumptive handler's evidence can resolve to Exit in an
enclosing Exit context — an open-row callback inside `attempt`, say — while
the activation it reaches is a Direct one. The generated call then wraps the
plain result as a normal Outcome, exactly as passing Direct evidence to an
Exit worker builds an eta record; a Unit operation's void call is sequenced
before the Unit outcome. Symmetrically, Core lint admits a call whose control
is polymorphic with an Exit lower bound against a polymorphic Direct
contract: a definition whose own lower bound is Exit emits only its Exit
member, so such a call always resolves to the callee's Exit member.

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
particular, `IO.native.go` owns console IO operations and line semantics,
`File.native.go` owns the open-file table and every fallible file operation,
while
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
process-state-observing natives. The bundled `File` natives have registry
entries only so bundled-native validation can check their arity; the entries
refuse to run, because those natives exist only in the sidecar worker.

The sidecar boundary admits two shapes beyond plain scalars, both resolved
once by the checker after the module's constructors are declared and recorded
on the native's metadata, so neither backend re-derives them from names. A
*boundary wrapper* is a type the sidecar's own module declares with one
constructor over one boundary scalar: generated Go projects the field
(`v.(*C_T).F0`, a projection on a single-constructor type rather than a type
check) before the call and rebuilds the constructor after it, and the
interpreter unwraps and rewraps the same `CtorVal`. Module validation
recognizes the shape by spelling, which is safe because the type must be
declared in the same file; the checker confirms it on resolved types. A
*fallible result*, `Result IO.Error a` backed by a Go `(T, error)`, is
admitted only to the bundled `File` module's value natives. The Go error is
classified in exactly one place, `fangort.ClassifyIOError`, into a kind code
indexing `IO.Kind`'s constructors, the path, and a stable message for a
recognized kind or the underlying text for `Other`;
generated code emits the branch at the call site as straight-line Go that
builds `Err (IO.Error {...})` or `Ok payload` with the ordinary constructor
emission, and the worker classifies in its dispatch loop and sends the
failure in its own protocol field, separate from infrastructure faults and
native panics, so the interpreter builds the same constructor values. The
`IO.Kind` constructor order is the compiler's contract with the classifier
and is checked when the native is declared. `File.Handle` and `File.Directory`
are boundary wrappers over the sidecar's table of open files and directory
listings; the table lives in the sidecar package's globals, which a compiled
program owns per process and the interpreter's worker keeps per session, so
both backends run the same code with the same lifetime. Ids are never
reused, so a stale id is an ordinary "closed handle" failure rather than an
alias of a newer file — a defensive check accepted fango cannot reach, since
a handle is abstract and its scope closes it exactly once. Every failure is
relabeled with the path the program supplied, because the sidecar joins
relative paths onto the working directory and the absolute form would differ
from run to run. Natives are inlined at their call sites, so the raw handle
operations stay unexposed and only `File`'s own wrappers call them.

## Formatting

`internal/format` is a library; `fango fmt` is a front end over it, and an
editor server would be another. Formatting is a pure function of one file's
bytes, so it holds no project state and needs no module graph: it runs the
lexer and the parser and stops. That is deliberate rather than incidental. It
runs before `internal/fixity`, which could only group operator runs with the
whole graph loaded, so the printer sees a flat run and prints it in the order
it was written rather than reassociating it — and formatting therefore works on
a file that does not resolve, does not typecheck, or has no project around it.

The lexer returns comments on a side channel rather than as tokens. The parser
distinguishes `f()` from `f ()` by testing whether neighbouring token spans are
byte-adjacent, so a comment token would make `f{- c -}()` look adjacent and
invert a documented rule; several other sites index the token slice directly
for lookahead. Tokens, comments, and whitespace together tile a file that lexes
cleanly, which is the invariant a formatter needs to reconstruct text it did
not print itself.

Author line breaks are preserved. The formatter normalizes indentation and
spacing and chooses nothing about where a construct is split, so no width
search is needed and a long line stays long. Whether a construct was written
across lines is read off its span; where a keyword landed is read off the
source directly, because the AST records no span for `then`, for `=`, or for a
declaration name on its definition line as opposed to its annotation line.

The layout constructs are the ones whose meaning is carried by columns, and a
renderer that hands each child an indent deeper than its own is what keeps the
output parsing as the input did: a block's statements align, a `case` or
`handle` aligns its branches, and an `if` anchors its `then` and `else` at its
own column. Delimited composites are the exception: their comma, pipe, and
closing-delimiter tokens align with the opening delimiter, and the parser admits
that punctuation at an enclosing layout anchor because it cannot begin a
sibling construct. A declaration printer that discovers halfway through that it
cannot reproduce what the author wrote rolls its output back, so the verbatim
copy that follows starts from a clean buffer.

Sorting import lines and exposed names is the one exception, and it forces two
consequences. A comment directly above an import has to travel with it, so the
import block is collected and emitted as a unit rather than in source order.
And a sorted list has no author line structure left to preserve, so a broken
one is laid out by kind and wrapped to a width — the only width the formatter
consults. The self-check canonicalizes both orders before comparing trees, so
it still rejects every structural change except the reordering the formatter is
meant to perform, and comments are compared as a multiset for the same reason;
that the ordering itself is right is held by fixtures.

Two properties keep it safe. Anything the printer cannot render structurally is
copied verbatim from its source extent. Comments are placed at anchors — above
a statement, a branch, a handler clause or a body, or trailing the line they
were written on — and a comment that reaches no anchor through whitespace alone
sits inside a construct that has none, so the whole declaration is copied
instead. A declaration is therefore counted as printed only when every comment
inside it was placed as well, which is what makes "no comment moves" a property
rather than a hope. And the formatter re-lexes and re-parses its own output and compares
span-free trees and comment text before returning, falling back to the original
bytes on any mismatch: indentation carries meaning here, so a printing bug
would otherwise change a program rather than merely misformat it. A file that
does not lex or parse is refused outright, because recovery drops a failed
declaration and no error node stands in for it.

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

The REPL retains one checker, type/name supply, evaluator environment, module
graph, resolver scope, and IO reader/writer across inputs. Prompt definitions
become lazy memo cells; functions become workers. Redefinition installs a new
generation, and existing memoized values and closures keep their old bindings.
Multiline input is driven by parser incompleteness and layout. Effectful
ordinary prompt declarations are rejected, while effectful expressions run
directly. Function definitions are installed without executing their bodies
and run only when applied. Class and instance declarations are supported.
Failed instance or deriving declarations roll back the persistent environment,
without reusing allocated identities. Class redefinition is rejected; type
redefinition creates a new generation with independently installed instances.

Every prompt input passes through the batch name resolver before inference.
The prompt is a synthetic private module whose scope persists: it starts from
the prelude's imports, and each accepted import or declaration extends it, so
names reach the checker canonical exactly as a module's do and the checker
holds the prelude under canonical names only. The resolver's prompt mode
differs from a file in two ways only: rebinding a prompt-declared name is not
a collision, and importing a module again is cumulative rather than a
duplicate. A prompt `import` loads the module graph increment, checks it with
the same module entry point a build uses (with the prompt's monomorphism rule
switched off, since module values are immutable), elaborates it under the
program rules — stable lifted names, scalar specialization, capture summaries
solved over the increment — lints it against everything the session has
installed, and only then defines it in the evaluator. Visibility of instances
and derivers merges per owner, and the prompt's own visible set grows with
every module loaded. User sidecars rebuild the session's native worker over
the bundled sidecars and all user sidecars imported so far.

Each input is a transaction over three stores — checker tables, resolver
scope, and module graph — that all roll back together on failure; the
checker's checkpoint therefore also covers effects, natives, the operator
table (restored in place, since the graph shares it by identity), and
instance visibility. The evaluator environment is only extended once an input
has been accepted everywhere else.

Reloading, cancellation, and interactive line history are not yet
implemented; see [REPL hardening](roadmap.md#repl-hardening).

## Testing and performance

Lexer, parser, inference, elaboration, and REPL behavior use unit tests and
goldens. Every runnable fixture is evaluated through Core and, outside short
mode, compiled through the real CLI; output is compared byte-for-byte with its
expected file and between backends. A fixture or example may carry a `.stdin`
transcript beside its source; both backends receive it as scripted standard
input. A fixture may also carry `.args` (one program argument per line),
`.status` (the exit status it must end with), and a `.files/` seed directory,
which each leg receives as a fresh copy in its own working directory, so a
fixture can read, write, and fail on real files without touching the
repository or the other backend's run; failures are scripted portably by
opening a directory as a file or naming a missing path, never with `chmod`.
A fixture with a sibling `.native.go` gets its sidecar installed in a private
worker for the interpreter leg, as a user module would. Stateful command
examples run a sequence against isolated working
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
package manager, transparent aliases, or LSP, and `fango fmt` normalizes only
the module header and the import block so far — everything below the imports is
copied as written. Type classes have one
parameter, no superclasses, higher kinds, default methods, ambiguous overlapping heads,
or method-local polymorphism. A constraint on a parameterized type is not
simplified to constraints on its arguments, so `Eq a => List a -> Bool` is
rejected in favor of `Eq (List a) => …`. There are no source-path
flags, external library version selection, or package resolution. The default
scope is whatever the bundled `Prelude` imports, editable in fango but not
replaceable per project: a module chooses between that scope and none, through
`{-# no-prelude #-}`, and nothing in between. The bundled standard library is
intentionally small and experimental.
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
