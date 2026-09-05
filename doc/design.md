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

Custom types are nominal and identified internally by a generation-stable
integer `Unique`, not their printed name. Constructors inhabit a separate
namespace. Pattern matching is exhaustive, and redundant branches are rejected.
`Bool` behaves as the predefined `True | False` ADT to the checker while using
native Go booleans in generated code.

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
rule from token columns and uses precedence climbing for operators. AST and
diagnostic dump formats are stable golden-test interfaces.

Batch compilation first discovers the complete module graph. The entry
directory provides local modules, where `Foo.Bar` maps to `Foo/Bar.fango`, and
an embedded provider supplies standard-library modules. Bundled names are
reserved rather than silently shadowed by local files. Modules from both
providers are parsed and their declared public interfaces validated before a
deterministic dependency-first topological order is chosen, with lexical
tie-breaking.

Primitives are declarations rather than a compiler catalog. Every ordinary
module implicitly loads the hidden `Basics` module, whose native values define
the fixed operator tokens, and the bundled `IO` module declares the ambient IO
effect and its operations. `infix` declarations bind the parser's closed set of
operator tokens to native values; only bundled modules may contain them.
`native "..."` templates are likewise bundled-only. The loader validates each
template as a Go expression, requires every positional argument exactly once,
and permits only placeholders, compiler intrinsics, Go predeclared names, and
the `fangort` qualifier. Code generation reparses and position-scrubs the
template, then substitutes typed Go AST expressions with precedence preserved.

An ordinary module can instead declare a pure call-form native and place a
`<Module>.native.go` sidecar beside its source. The loader validates the
sidecar's `package native`, standard-library-only imports, bidirectional
declaration/function correspondence, and closed scalar, Unit-erased ABI. Its
content hash joins `sources.json`; the build synchronizer materializes it as a
separate package below `native/`, so edits and removal invalidate the build and
prune stale generated files. Go compilation remains the final body/type check.

Name resolution rewrites module-level declarations and imported references to
opaque, collision-free canonical symbols before inference. Local binders keep
their source names. The resolved modules are merged in graph order and checked
with one graph-wide fresh-name supply and one set of builtin identities. This
shares nominal ADT and effect identities safely across module boundaries while
an import can seed only its direct dependency's declared public interface.
Core contains no import syntax, but top-level definitions retain their source
module owner so the Go backend can recover compilation boundaries. `Prog.Entry`
identifies the selected entry definition independently of its printed name.

Inference and elaboration are separate because code generation is
type-directed. Elaboration resolves defaulting,
derives evidence requirements, collapses application spines, chooses direct or
indirect calls, lambda-lifts polymorphic locals, compiles matches to decision
trees, and puts expression-shaped control flow into ANF where Go needs
statements.

## Type inference

Inference is Hindley-Milner with parameterized ADTs, explicit annotations,
effect rows, and three variable kinds: general, numeric, and row. It generates
reason-tagged equality/inclusion constraints, solves them by unification with
an occurs check, and generalizes at binding boundaries. Annotation variables
are rigid skolems, preventing an annotation from claiming more polymorphism
than its body supplies.

Top-level values and functions generalize. Local syntactic functions and
lambdas generalize, while local values remain monomorphic so their strict,
evaluate-once semantics are not changed by lambda lifting. `main` is ground and
never generalized. Numeric variables range over `Int` and `Float`; unresolved
numeric types default to `Int`, while `/` is always `Float`.

`Scheme.Preds` carries compiler-owned `Eq`, `Ord`, and `Show` obligations for
native declarations. Annotation variable prefixes `equatable`, `comparable`,
and `printable` create those predicates only in native declarations; they do
not add capability kinds or become general user constraints. Instantiation
substitutes predicate types alongside the declared type. The current compiler
discharges only concrete scalar, numeric, and function-free ADT obligations;
residual general predicates are rejected until typeclasses provide evidence
passing.

## Core and evidence invariants

Core is the compiler/interpreter contract. Every definition and expression is
explicitly typed; generic definitions declare type parameters and uses carry
explicit type arguments. Application nodes record whether their callee is a
worker, constructor, primitive, operation, or indirect function. Matches are
decision trees rather than surface branch lists.

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
`int64`, `Float` is `float64`, `String` is `string`, and `Bool` is `bool`.
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
explicit instantiation. Each structurally eligible ADT's owning package exports
compiler-internal derived equality and display functions; they receive typed
element operations where required. Emitting these independently of downstream
uses keeps a module package stable when only a consumer changes.

The backend emits one Go package per Fango module beneath a single generated Go
module. The entry module is the root `package main`; local and bundled
dependencies use their logical layout below `modules/`. Cross-package values,
workers, ADTs, constructors, effect evidence, and derived operations use a
typed compiler-internal exported ABI. Direct source imports remain Go import
edges even when unused, and generated types may add an import of a transitive
type owner. Package aliases and batch lambda-lifted names are deterministic and
independent of graph-wide identity allocation, so unchanged source units emit
byte-identical Go. Project emission is the backend's only generation path;
tests inspect the same package files used by `build`, `run`, and `--emit-go`.

The build driver materializes the package tree and embedded `fangort` beneath a
persistent `.fango/build` directory, writing only changed files and removing
only stale package-source paths recorded in its generated-file manifest. It
also writes `sources.json`, a deterministic dependency-first manifest of
logical names, local root-relative or `<stdlib>/...` paths, and SHA-256 content
hashes. Every source edit and graph change triggers a Go build even if
generated Go is unchanged. Go's package cache then reuses unchanged compilation
units. `build` copies the resulting executable; `run` reuses it while inputs
are unchanged.
`fangort` owns shared representations, formatting, and IO behavior, including
newline-free string writes, used by the compiled and interpreted backends.

Bundled native interpreter behavior lives in one registry backed by the
stdlib's Go sidecars. Pure `NativeCall`, unhandled native `Perform`, and the
constant folder dispatch through that registry; only integer and floating
arithmetic retain foldable status. User sidecars are deliberately compiled-only
because dynamically loading Go would break the persistent REPL model. The
interpreter reports that limitation instead of attempting execution. Native Go
panics currently propagate unchanged.

## Interpreter and REPL

`internal/eval` executes Core, not the surface AST. Values have a uniform Go
representation in the interpreter, while environments distinguish typed
workers from lazy memoized top-level cells and eager block frames. `EvalIO` and
`ForceIO` are the explicit IO entry points. The interpreter and generated Go
share observable formatting rules.

The REPL retains one checker, type/name supply, evaluator environment, and IO
reader/writer across inputs. Prompt definitions become lazy memo cells;
functions become workers. Redefinition installs a new generation, and existing
memoized values and closures keep their old bindings. Multiline input is driven
by parser incompleteness and layout. Effectful ordinary prompt declarations
are rejected, while effectful expressions run directly. Function definitions
are installed without executing their bodies and run only when applied.

Loading, reloading, cancellation, and interactive line history are not yet
implemented; see [REPL hardening](roadmap.md#repl-hardening).

## Testing and performance

Lexer, parser, inference, elaboration, and REPL behavior use unit tests and
goldens. Every runnable fixture is evaluated through Core and, outside short
mode, compiled through the real CLI; output is compared byte-for-byte with its
expected file and between backends. Invalid fixtures pin diagnostic substrings.
Focused inference and elaboration harnesses install the actual embedded
`Basics` and `IO` declarations rather than a parallel test-only environment.
Generated Go is checked for deterministic, gofmt-idempotent output. The Core
linter runs in every batch compilation.

Compile-latency benchmarks track cold and warm paths against recorded,
machine-specific baselines. Runtime benchmarks compare representative scalar,
match, string, list, and tree programs with handwritten Go and use per-case
ratio ceilings. These measurements arbitrate representation or optimization
work. They run separately from the default correctness test loop because they
are comparatively slow and sensitive to host load; `make test-perf` runs them
explicitly, while `make ci` retains them as verification gates. Cons-list
allocation remains the main known structural performance cost.

## Known limitations

The implementation has a deliberately narrow, pure Go sidecar FFI but no
package manager, records, aliases, typeclasses, formatter, or LSP. There are no source-path
flags, external library version selection, or package resolution. The implicit
prelude is fixed to hidden `Basics` plus ambient `IO`; the bundled standard
library is intentionally small and experimental.
Integer values are signed 64-bit; broader numeric semantics are not settled.
There is no tail-call optimization guarantee. Custom handlers have the
restrictions described above, and ambient IO cannot be re-handled yet. REPL
loading/reloading and cancellation remain unfinished.
