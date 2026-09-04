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
framework dependencies, and a single-module source model. Laziness,
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

## Computations and effects

The internal function type is `TFun{Arg, Eff, Ret}`. Surface `A ->{e} B` and
`A -> {e} B` normalize to the same form. A computation `{e} T` normalizes to a
Unit-argument `TFun`; it introduces no separate Core or runtime representation.

Computation definitions and uses follow these invariants:

- a zero-parameter binding annotated with `{e} T` defines a delayed
  computation and does not execute its body;
- an unannotated local binding executes a computation-valued RHS and binds its
  result;
- an annotated local computation binding stores the delayed computation;
- elaboration inserts a force when `{e} T` appears where `T` is expected and
  the ambient row admits `e`;
- computations are second-class: they may be definition, parameter, or return
  types, but may not instantiate an unconstrained type variable or be stored in
  an ADT.

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

Inference and elaboration are separate because code generation is
type-directed. Elaboration resolves defaulting, inserts computation forces,
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

`Scheme.Preds` is a reserved typeclass seam and remains empty. Consequently,
operations requiring typeclass evidence are deliberately limited: structural
equality and display are synthesized only when the concrete type supports
them, and equality at an unconstrained general type variable is rejected.

## Core and evidence invariants

Core is the compiler/interpreter contract. Every definition and expression is
explicitly typed; generic definitions declare type parameters and uses carry
explicit type arguments. Application nodes record whether their callee is a
worker, constructor, primitive, operation, or indirect function. Matches are
decision trees rather than surface branch lists.

Effectful Core uses `Perform`, `Handle`, `Resume`, and `Seq`. Source rows and row
variables are erased only after evidence requirements have been derived.
Hidden evidence parameters precede ordinary worker parameters in deterministic
effect-identity order, and calls supply matching lexical evidence. The Core
linter rejects unsolved metavariables, malformed generic applications,
callee/evidence disagreements, invalid handler coverage or types, and residual
surface effect information before either backend runs.

## Go backend and runtime

The compiler emits formatted Go source and invokes the supported `go build`
interface. Direct integration with Go runtime internals is intentionally
avoided: its ABI, stack maps, barriers, and scheduler metadata are compiler
implementation details.

Representations are type-directed rather than uniformly boxed: `Int` is
`int64`, `Float` is `float64`, `String` is `string`, `Bool` is `bool`, Unit is a
runtime zero-sized value, functions are typed Go functions, and ADTs use typed
interfaces and constructor structs. Parameterized definitions map to Go
generics with explicit instantiation. Generated derived equality and display
functions receive typed element operations where required.

The build driver materializes an embedded `fangort` package and generated
`main.go` beneath a persistent `.fango/build` directory, writing only changed
files. `build` copies the resulting executable; `run` reuses it while inputs
are unchanged. `fangort` owns shared formatting and IO behavior used by the
compiled and interpreted backends.

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
are rejected, while effectful expressions run directly and annotated
computation definitions may be installed and forced per mention.

Loading, reloading, cancellation, and interactive line history are not yet
implemented; see [REPL hardening](roadmap.md#repl-hardening).

## Testing and performance

Lexer, parser, inference, elaboration, and REPL behavior use unit tests and
goldens. Every runnable fixture is evaluated through Core and, outside short
mode, compiled through the real CLI; output is compared byte-for-byte with its
expected file and between backends. Invalid fixtures pin diagnostic substrings.
Generated Go is checked for deterministic, gofmt-idempotent output. The Core
linter runs in every batch compilation.

Compile-latency benchmarks track cold and warm paths against recorded,
machine-specific baselines. Runtime benchmarks compare representative scalar,
match, string, list, and tree programs with handwritten Go and use per-case
ratio ceilings. These measurements arbitrate representation or optimization
work. Cons-list allocation remains the main known structural performance cost.

## Known limitations

The implementation currently has one source module, no imports, FFI, package
manager, standard library, records, aliases, typeclasses, formatter, or LSP.
Integer values are signed 64-bit; broader numeric semantics are not settled.
There is no tail-call optimization guarantee. Computations are second-class,
custom handlers have the restrictions described above, and builtin IO cannot
be re-handled. REPL loading/reloading and cancellation remain unfinished.
