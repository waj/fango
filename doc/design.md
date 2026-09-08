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
coverage, representation, and evaluation rules as explicit constructor code.

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

A syntactic multi-parameter worker executes its body only after its final
parameter, so inferred effects belong to the final arrow and earlier partial
applications are pure. A one-parameter function whose body returns a lambda is
different: effects before constructing the lambda belong to the outer arrow,
while effects in the lambda body belong to the returned arrow.

Effect rows contain distinct, nominal effect labels and an optional open tail.
Row solving supports inclusion and union for nested and higher-order calls.
Effect declarations may be parameterized, but runtime operation-local/result
polymorphism is not yet supported. A saturated operation application performs;
a partial application is a pure closure.

Implemented custom handlers are complete, one-shot, and tail-resumptive. Every
operation clause must end in exactly one tail `resume` on every reachable path;
aborting clauses and escaping or general continuations are rejected. The
optional `return` clause transforms normal completion. Handling builtin `IO`
is currently rejected. These restrictions let both backends implement handlers
with stack-local evidence and direct returns, without goroutines, channels,
panic sentinels, or continuation objects.

The shared runtime also provides a dormant general-handler engine for generated
code in a later compiler increment. `RunGeneral` runs a handled body in one
goroutine. `Perform` parks that goroutine on private channels and presents the
handler with an opaque operation payload and continuation. Resuming transfers
control back to the body and drives later operations or completion; the normal
return transformation runs exactly once. A continuation is concurrency-safe
and strictly one-shot: its single terminal action is either `Resume` or
`Discard`. `Discard` unwinds the parked body and waits for its deferred cleanup,
leaving the operation clause responsible for the handled result.

A pending general continuation may outlive the handler callback that received
it and remains live until explicitly resumed or discarded. Handler failure
expires pending work and waits for body cleanup. Stable runtime errors
distinguish consumed continuations, expired continuations, and operations on a
closed handler. Panics in bodies, handler clauses, and return clauses are
recovered at runtime boundaries and reported as structured errors; private
unwind values do not escape as user-visible panics. Termination is centralized
in the parked-body protocol so cancellation can later share its shutdown path.
The compiler and interpreter do not yet emit or call this general engine.

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
tail lowers to `List.Nil`. AST and diagnostic dump formats are stable
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
use; inference never treats a label as a structural type constraint.

Inference and elaboration are separate because code generation is
type-directed. Elaboration resolves defaulting,
derives evidence requirements, collapses application spines, chooses direct or
indirect calls, lambda-lifts polymorphic locals, compiles matches to decision
trees, and puts expression-shaped control flow into ANF where Go needs
statements.

## Type inference

Inference is Hindley-Milner with parameterized ADTs, qualified schemes, explicit
annotations, effect rows, and two variable kinds: general and row. It generates
reason-tagged equality/inclusion constraints, solves them by unification with
an occurs check, and generalizes at binding boundaries. A label-free open row
normalizes to its tail during unification, so an annotation's rigid row
variable unifies with the fresh row a call site mints — this is what lets a
handler wrapper carry an explicit open-tail annotation. Annotation variables
are rigid skolems, preventing an annotation from claiming more polymorphism
than its body supplies. Variable spelling never grants numeric or other
capabilities.

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
visible schema. Deferred accesses resolve to a fixed point rather than in one
pass, because one access's receiver is often another's result: `ctor.fields`
decides the element type a later `field.index` reads. Only obligations that
survive a pass learning nothing are genuinely ambiguous. Every checker that
generalizes a body — top-level values, prompt expressions, local function
bindings, instance methods, and deriver methods — resolves them before
reducing predicate obligations, so a constraint on a field's type names a type
rather than an unsolved variable.
Elaboration lowers literals, projections, and functional
updates to the existing constructor, `Let`, and exhaustive one-constructor
`Case` Core forms. This keeps Core and both backends free of a second record
representation while preserving single evaluation and source-order effects.
Record patterns follow the same lowering: omitted fields become wildcards and
provided patterns are reordered into schema order. Hidden record constructors
never appear in source diagnostics.

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
with an empty effect row, so "no IO during compilation" follows from the
effect system. Beyond purity, the interpreter runs splices in a restricted
mode: natives carry a compile-time-safe flag, and `Random` is the concrete
exclusion — its draws are pure once `runSeeded` handles the effect away, but
they advance its bundled sidecar's process-global PRNG cell, which the compiler
shares with the program it is compiling. User Go sidecars are unavailable for
the reason they always were: the interpreter cannot load Go. A step budget
bounds evaluation. Together these keep generated Go byte-identical across
builds.

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
prefix along with the rest of the declaration environment, and tells the
compile-time evaluator to discard an environment that no longer describes it —
which is why a REPL `deriving` clause whose deriver fails leaves no type
behind.

## Core and evidence invariants

Core is the compiler/interpreter contract. Every definition and expression is
explicitly typed; generic definitions declare type parameters and uses carry
explicit type arguments. Application nodes record whether their callee is a
worker, constructor, primitive, operation, or indirect function. Matches are
decision trees rather than surface branch lists.

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

Effectful Core uses `Perform`, `Handle`, `Resume`, and `Seq`. Open source row
tails are erased after evidence requirements have been derived; concrete labels
remain on first-class arrows as their indirect-call evidence ABI.
Hidden evidence parameters precede ordinary worker parameters in deterministic
effect-identity order, and calls supply matching lexical evidence. The Core
linter rejects unsolved metavariables, malformed generic applications,
callee/evidence disagreements, invalid handler coverage or types, and residual
open rows before either backend runs.

An effect-polymorphic higher-order worker has its open callback row erased from
the runtime ABI. Passing a concrete callback therefore adapts it to that ABI;
local function references are eta-expanded so their binding keeps its concrete
type while the wrapper retains the callback's execution and evidence behavior.

## Go backend and runtime

The compiler emits formatted Go source and invokes the supported `go build`
interface. Direct integration with Go runtime internals is intentionally
avoided: its ABI, stack maps, barriers, and scheduler metadata are compiler
implementation details.

Representations are type-directed rather than uniformly boxed: `Int` is
`int64`, `Float` is `float64`, `String` is a valid UTF-8 `string`, `Char` is a
Unicode-scalar Go `rune`, and `Bool` is `bool`.
Concrete Unit parameters and results at direct worker and operation boundaries
are implicit in generated Go: the parameter is omitted and the result is a
void result. Unit remains a represented, runtime zero-sized value at
first-class-function, polymorphic, and ADT boundaries, where Go's type system
requires a value; `fangort.Unit` and `fangort.UnitValue` give every generated
package the same nominal representation instead of repeating anonymous
composite literals.
Erasing a Unit argument never erases its evaluation: expression lowering keeps
strict left-to-right order, materializing the singleton only when a value is
required. Functions are typed Go functions, and ADTs use typed interfaces and
constructor structs. Parameterized definitions map to Go generics with
explicit instantiation. Class dictionaries, instance factories, and generated
deriving methods use the same typed, exported internal ABI as ordinary ADTs
and workers. Definitions belong to their source module, so adding a downstream
consumer does not change the dependency's generated package.

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
formatting, the general-handler engine, and the generic native-host contract.
Module-specific native logic lives in the owning stdlib sidecar instead. In
particular, `IO.native.go` owns IO operations and line semantics, while
`Random.native.go` owns its process-global PRNG cell; the interpreter worker
and each compiled program therefore have the same single-cell, per-process
behavior and execute the same implementation.

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
share observable formatting rules.

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

The differential cases run in parallel, since each compiles into its own
build directory and the compiled leg is subprocess work. Their interpreter
legs are serialized against each other: hosting many programs in one process
is the test harness's privilege, not a language capability, and the
interpreter shares the same process-global worker state that a compiled program
owns outright — the PRNG cell behind Random in particular.

Compile-latency benchmarks track cold and warm paths against recorded,
machine-specific baselines. Runtime benchmarks compare representative scalar,
match, string, list, and tree programs with handwritten Go and use per-case
ratio ceilings. These measurements arbitrate representation or optimization
work. Cons-list allocation remains the main known structural performance cost.

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
or method-local polymorphism. There are no source-path
flags, external library version selection, or package resolution. The implicit
prelude is fixed to hidden `Basics` plus ambient `IO`, with `Meta` added only
for files that use the staging syntax; the bundled standard
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
