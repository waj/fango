# fango — Design Document

> **Status: draft for iteration.** Nothing here is final. Each major decision lists the alternatives considered so it can be revisited, and [Open Questions](#14-open-questions) collects everything deliberately left unresolved. No compiler code exists yet.

**fango** is a statically-typed, purely functional programming language inspired by Haskell and Elm, compiled to native binaries by way of the Go toolchain.

```elm
module Main exposing (main)

type Shape
    = Circle Float
    | Rect Float Float

area : Shape -> Float
area shape =
    case shape of
        Circle r ->
            3.14159 * r * r

        Rect w h ->
            w * h

main =
    print (area (Circle 2.0))
```

This program is the MVP acceptance test: `fango run main.fango` must print `12.56636`.

---

## 1. Goals and non-goals

### Goals

- A pleasant Elm/Haskell-flavored language: layout syntax, curried functions, algebraic data types, pattern matching, full type inference.
- **Statically compiled**: `fango build` produces a standalone, cross-platform native binary.
- **Performance, from day 0.** This is *why* the Go runtime was chosen, and it cuts both ways:
  - **Fast compilation** — `fango run` on a small program must feel instant (warm budget: well under a second end-to-end, including `go build`). Compile latency is benchmarked and budgeted from the first milestone.
  - **Fast generated code** — fango output should approach handwritten Go, not interpreted-language speed. Concretely: no uniform boxing, no reflection on hot paths, no dynamic dispatch where the type checker already knows the answer. Target: within a small constant factor (~2–3×) of handwritten Go on ADT/list-heavy benchmarks, tracked by a benchmark suite against Go baselines.
- **Reuse the Go runtime** for the hard parts: garbage collection, (eventually) green-thread concurrency, trivial cross-compilation.
- **An interactive REPL, designed in from day 0** — define types, evaluate expressions, load and re-load modules from source, with the dynamism of an interpreted language despite fango being compiled (see [§9](#9-interactive-repl-fango-repl)).
- A small, understandable compiler codebase that one person can hold in their head.
- Excellent error messages, in the Elm tradition.

### Non-goals (for now)

- Laziness (see [§3.1](#31-strict-evaluation)).
- Beating handwritten Go, whole-program optimization, or fusion-style rewrites. Performance means *not leaving obvious factors on the table*, not a research optimizer.
- Self-hosting.
- An ecosystem/package manager. Single-module programs first, module system later.

## 2. The core architectural bet: compile to Go source

**Decision:** the compiler emits Go source code and invokes `go build`.

**Why not link against the Go runtime directly?** This was investigated and is effectively impossible:

- The Go runtime (GC, goroutine scheduler, stack growth) is not a standalone linkable component. The garbage collector requires *stack maps* and *write barriers* that only the `gc` compiler emits; every function prologue carries `morestack` checks; the internal ABI is explicitly unstable across releases.
- The compiler backend (`cmd/compile/internal/ssa`, linker metadata formats) lives under `internal/` packages — unimportable, undocumented, and changing every release. Forking the toolchain to accept our own IR would be a large, permanently fragile project.
- **Go source code is the only supported interface to the Go runtime.**

**What we get by emitting Go:** a production-grade GC for free (fango values are Go heap objects), goroutines available whenever we design a concurrency story, `GOOS`/`GOARCH` cross-compilation, static binaries, and a fast backend (Go compiles quickly, so the extra build step costs little).

**Alternatives considered:**

| Alternative | Why rejected |
|---|---|
| Link generated object code against the runtime | No stable ABI; GC/scheduler require compiler-emitted metadata. Not viable. |
| Fork `cmd/compile` and feed it our AST/IR | Massive, breaks on every Go release. |
| LLVM or C backend | Must build or adopt a GC and runtime ourselves — a far bigger project than the language itself. |
| WASM backend | Same runtime problem, plus a host environment problem. Could be a *second* backend someday. |
| Tree-walking interpreter in Go *as the only backend* | Abandons the "statically compiled" goal. An interpreter does exist — over the typed Core IR, powering `fango repl` and differential testing ([§9](#9-interactive-repl-fango-repl)) — but shipping programs always compile. |

Precedent: Grumpy (Python→Go), GopherJS (Go→JS, inverse direction), Gleam (FP language targeting an existing runtime, Erlang's) all validate the "transpile to the runtime's source language" approach.

## 3. Language design decisions

### 3.1 Strict evaluation

**Decision:** call-by-value, like Elm, OCaml, and Gleam. Laziness (Haskell) was rejected because call-by-need requires thunks everywhere, compiles poorly onto Go's eager runtime, and makes performance hard to reason about. Purity is kept; laziness is not what makes Haskell pure.

### 3.2 Purity

fango is pure: no mutation, no side effects in ordinary functions. The effects story is decided: **algebraic effects with handlers** ([§10](#10-effects-and-io-algebraic-effects-with-handlers)) — effect rows in function types, direct-style code, `main : () ->{IO} ()`. For the MVP milestones before effects land, `print` remains an honest cheat (a builtin with an unsound type, like `Debug.log`).

### 3.3 Type system: Elm-level now, typeclass-ready later

**Decision:** full Hindley-Milner inference with parametric polymorphism, parameterized ADTs, and pattern matching — but **no typeclasses in the MVP**. The inference engine is deliberately built *constraint-based* (see [§7](#7-type-checker)) so typeclasses can be added without a rewrite.

- Elm demonstrates this level is already very pleasant to use.
- Typeclasses roughly double checker complexity (contexts, dictionary passing, coherence) — wrong place to spend the first effort.
- The seam is concrete: a `Pred{Class, Ty}` type exists in the solver's signature from day one, always empty until typeclasses land.

### 3.4 Syntax: Elm-style layout

**Decision:** whitespace-significant, Elm-flavored syntax — `f x y` application, `type Maybe a = Just a | Nothing`, `case … of`, top-level annotations as separate lines (`area : Shape -> Float`). A braces/keywords alternative (Gleam-style) was considered and declined; the classic FP surface is a core part of the goal.

Layout rules are deliberately simpler than Haskell's (see [§5](#5-lexer-and-layout)).

### 3.5 MVP surface

Single module. `Int`, `Float`, `String`, `Bool`; top-level definitions with optional annotations; statement-style declaration bodies with local bindings (§3.6); lambdas (`\x -> …`) and curried application; `if/then/else`; `type` declarations with type parameters (§3.7; parameters parse in S4 and activate in S5); `case` with nested patterns (constructors, literals, variables, `_`); operators `+ - * / == /= < > <= >= ++`; a `print` builtin. Numbers follow Elm: `+ - *` work at `Int` or `Float` (see [§7.3](#73-numbers-without-typeclasses)), `/` is `Float`-only.

### 3.6 Statement-style bodies — the first deliberate divergence from Elm

**Decision (2026-09-02): fango has no `let … in`. A declaration body is either an inline expression, or an indented block of `name = expr` binding lines followed by exactly one result expression.**

```elm
normalize v =
    len = sqrt (dot v v)
    scale (1.0 / len) v
```

Rationale:

1. **Effects are direct-style (§10), and this is their natural syntax.** When effects land, `line = readLine ()` is an ordinary binding whose right-hand side performs an effect — sequencing really is implicit, like the intuition behind Haskell's `do` without the monad. Statement bodies also give Unit-typed expression statements a home (stacking `print`s in `main`, S7); `let/in` handles that only as the self-defeating `let _ = e1 in e2`.
2. **One scoping rule everywhere.** Block bindings follow the same source-order, use-after-define rule as the top level. Elm's `let` is mutually recursive within its block — a second scoping rule fango never has to implement or explain.
3. **The machinery already exists.** The offside rule (§5) gives blocks their statement column, and the `name … =` line classifier is the same lookahead the REPL and top level already use.

Semantics: sequential scoping with no forward references; **no shadowing** — a binding may not rebind any name already in scope (module level or earlier in the block); self-reference is the same undefined-name error as at the top level; bindings evaluate **eagerly in order** in both backends (top-level definitions keep the lazy-memo REPL model, §9.2). Local *function* bindings (`helper x = …` inside a block) arrive with functions (S3).

Where blocks appear: declaration bodies from S2 (including REPL definitions); function and lambda bodies from S3 — where locals referencing parameters make blocks irreplaceable; `case` branches from S4. `if/then/else` branches stay expression-form (it is an expression).

| Alternative | Why not |
|---|---|
| Elm's `let … in` | Expression-form and mutually recursive block scope: a second scoping rule, and no story for direct-style effect sequencing beyond `let _ = …`. |
| `let`-keyword statements (Gleam `let x`, Koka `val x`) | The keyword's only real job is marking intentional shadowing — fango forbids shadowing instead. Precedents for keyword-less: Roc, F# lightweight syntax. |

`let` and `in` remain reserved words (better errors for Elm/Haskell muscle memory, and the door stays open).

### 3.7 `type` declarations

**Decision (2026-09-02): custom types are Elm's, with one spelling rule made explicit — the right-hand side of a `type` declaration is always a list of constructor alternatives being *defined*, never a reference to an existing type.** There is no alias-flavored `type` form.

```elm
type Shape
    = Circle Float
    | Rect Float Float

type Maybe a = Nothing | Just a
type Fn a b = Fn (a -> b)        -- a constructor may share its type's name
```

Grammar sketch: `type UIdent lident* = UIdent atom* ( | UIdent atom* )*`, starting at column 1; alternatives sit inline or on continuation lines under the ordinary offside rule (the next column-1 token terminates the declaration, §5 rule 1). A constructor argument is a type *atom*: a named type or a parenthesized type expression — functions `(a -> b)` and applications `(Maybe a)` included. This requires the surface type-expression grammar to gain application (`Maybe Int`), which S2's annotation subset didn't need.

Rules:

- **Namespaces.** Type names and constructor names live in separate namespaces, so `type Fn a b = Fn (a -> b)` is legal. Within each namespace, names are unique per module — the same no-shadowing posture as values (§3.6). Style note: reusing one name for a type and an *unrelated* constructor of another type is legal but confusing; the docs and future stdlib never do it.
- **Full application.** Every type-constructor reference is applied to exactly its declared arity — `p : Maybe` (under-applied) is an arity error, as is `Int Float`. Checked from S4, where it is trivial (all user arities are 0); load-bearing from S5.
- **Function payloads** are allowed (closures are ordinary values since S3), with the equality consequence recorded in §8.6: `==` at a type whose payload transitively contains a function is a compile-time error.
- **`Bool`** is predefined as an ordinary ADT (§7.2); redeclaring it is the normal duplicate-name error (REPL generational redefinition still applies, §9.3).

Staging: **the grammar is final in S4 — type parameters parse — but the checker rejects parameterized declarations with a staged "type parameters arrive in S5" error**, the same pattern the annotation resolver already uses for type variables. The staging error ships with negative tests; S5 deletes it and activates the already-parsed parameters.

Considered and declined:

| Form | Why not |
|---|---|
| Alias-style `type FnFromInt b = Fn Int b` | Incoherent under the RHS rule: it would *define* a second constructor named `Fn` — colliding with `Fn`'s own — not reference the existing type. Keeping `type` RHS = constructors-only is what keeps the grammar unambiguous. |
| `alias Name params = <type expr>` | Declined for now — monomorphic S4 has nothing worth aliasing, and MVP programs are small. If ever added: transparent (expanded at elaboration, never reaching unification), fully applied at use sites, acyclic; the `alias` keyword is favored over Elm's two-word `type alias`. |
| Inline record payloads (`Circle { center : Point, radius : Float }`) | Records are undesigned (open question #4), and anonymous record payloads would smuggle structural records — and their row-polymorphism machinery — in through the constructor door. Constructor payloads are named types only until records are designed. |

## 4. Compiler overview

The compiler is **written in Go** (one toolchain for compiler and backend; `go build` invocation is natural; trivial distribution). Haskell would be comfier for tree-shaped code but adds a second toolchain; Rust adds borrow-checker friction for compiler ASTs.

Pipeline, as a pure function chain:

```
source ─lexer─▶ tokens ─parser─▶ AST ─infer─▶ typed AST ─elaborate─▶ Core IR ─codegen─▶ go/ast ─build─▶ binary
```

Codegen is **type-directed** (see [§8](#8-compilation-to-go)): the representation of every value and the shape of every call depend on its solved type, so inference must complete before any code is generated. The *elaborate* pass bridges the two worlds — it produces **Core**, a small explicitly-typed IR where all the cleverness (defaulting, lambda-lifting, saturation analysis, decision trees) lives in a unit-testable form, leaving codegen a boring syntax-directed walk.

### Repository layout

```
fango/
├── flake.nix                   # Nix dev environment (Go, gopls, gotools) — see §10
├── go.mod                      # module github.com/waj/fango
├── cmd/fango/main.go            # CLI: build | run | check | repl | clean; --emit-go
├── internal/source/            # File, Pos{Line,Col}, Span, source excerpt rendering
├── internal/token/             # token kinds, Token{Kind, Text, Span}
├── internal/lexer/             # hand-written scanner
├── internal/ast/               # surface AST + S-expression dumper (golden-test format)
├── internal/parser/            # recursive descent; layout.go holds the offside rule
├── internal/types/             # Type, Scheme, pretty-printer
├── internal/infer/             # constraint generation, unification, solving
├── internal/core/              # typed Core IR: explicit type args, S-expr printer, invariant linter
├── internal/elaborate/         # typed AST → Core: zonk/default, lambda-lifting, saturation, match trees
├── internal/codegen/           # syntax-directed Core → go/ast, mangling
├── internal/eval/              # Core interpreter (REPL backend + differential test oracle)
├── internal/repl/              # session state, generational env, command loop, :load/:reload
├── internal/build/             # persistent .fango/build dir, `go build` invocation
├── internal/diag/              # diagnostics rendering
├── runtime/fangort/             # runtime support package, embedded via go:embed
├── benchmarks/                 # compile-latency + fango-vs-handwritten-Go runtime suites
└── testdata/                   # lex/, parse/, infer/, core/, run/ golden and e2e suites
```

CLI uses stdlib `flag` only: `fango build main.fango [-o out]`, `fango run main.fango`, `fango check main.fango` (parse + typecheck only — a free byproduct that is also the test harness's workhorse), and `fango repl` ([§9](#9-interactive-repl-fango-repl)).

## 5. Lexer and layout

**Decision: the lexer is layout-oblivious; the parser enforces the offside rule using token columns.** This is Elm's approach. The alternative — Haskell's layout algorithm with synthesized virtual `{`/`;`/`}` tokens — was rejected: it is notoriously edge-casey and fango has no `where`/`do` blocks that motivate it.

The rules, in full:

1. Top-level declarations start at column 1; any token at column 1 terminates the current declaration.
2. `case … of`: the column of the first pattern after `of` defines branch alignment. A token at exactly that column starts a new branch; left of it ends the `case`; right of it continues the current branch.
3. Block statements (§3.6) use the same alignment rule: when a declaration's body starts on a later line, the first token's column defines the statement column — a token at exactly that column starts the next binding or the result expression, left of it ends the block.

This is ~50 lines in `parser/layout.go`: a stack of indentation contexts plus two predicates (`atBranchCol`, `checkOffside`). Layout bugs are the classic failure mode of such languages, so layout-specific golden tests (misaligned branches, deep nesting, comments inside cases) exist from the earliest milestone that parses declarations.

Lexer details: hand-written byte scanner; every token carries a `Span` with line/column; `--` line comments and nestable `{- -}` block comments; **tabs rejected in indentation** (as in Elm — columns must be unambiguous).

## 6. Parser and AST

**Decision: hand-written recursive descent, with a Pratt (precedence-climbing) loop for operator expressions.** Parser generators handle layout predicates ("is this token at column N?") badly, and hand-written parsers give the best error messages — the approach of the Go, Elm, and Rust compilers themselves. Zero dependencies.

The AST models sum types as sealed Go interfaces with marker methods:

```go
type Expr interface { isExpr(); Span() source.Span }
// IntLit, FloatLit, StringLit, Var, Ctor, App, Lambda, If, Block{Binds, Result}, Case, BinOp
type Pattern interface { isPattern(); Span() source.Span }
// PVar, PWildcard, PInt, PFloat, PString, PCtor{Name, Args []Pattern}
type Decl interface { isDecl() }
// TypeDecl{Name, Params, Ctors}, ValueDecl{Name, Ann *TypeExpr, Params, Body}
```

Notes:
- Every node carries a `Span` — type errors and future exhaustiveness warnings hang off it.
- `f x y` parses as `App(App(f, x), y)` (curried applications).
- `BinOp` stays a distinct node rather than desugaring to `App`: inference special-cases numeric operators, and errors should point at the operator itself.
- The AST prints as indented S-expressions (`(app (var area) (app (ctor Circle) (float 2.0)))`) — the golden-test format, much more diff-friendly than JSON.

## 7. Type checker

### 7.1 Types

```go
type Type interface{ isType() }
type TVar struct { ID int; Kind VarKind }        // meta variable; Kind ∈ {General, Number, RowVar}
type TCon struct { Unique int; Name string; Args []Type }  // Int, Shape, Maybe a
type TFun struct { Arg Type; Eff Row; Ret Type } // a ->{e} b -> c; Eff always empty until S7 (§10.8)
type Scheme struct { NumVars int; Preds []Pred; Body Type }  // ∀-quantified

type Pred struct { Class string; Ty Type }       // the typeclass seam — always empty in MVP
```

**Type identity is the `Unique`, not the name — from day 0.** Unification compares uniques; the environment maps a *name* to its current unique. In batch compilation this is invisible, but it is load-bearing for the REPL (redefining a type mints a new generation, [§9.3](#93-redefinition-semantics)) and pre-pays for module-qualified type identity when the module system arrives. Trivial to build in now, painful to retrofit.

### 7.2 Constraint-based inference, in three phases

This is deliberately *not* naive Algorithm W with inline unification — the explicit constraint list is what buys good errors now and typeclasses later.

1. **Constraint generation** (`constrain.go`): walk the AST with `Env map[string]Scheme`; assign a fresh `TVar` to every node; emit `Constraint{Left, Right, Span, Why}`. `Why` is a reason tag (`IfCondition`, `IfBranches`, `CaseBranches`, `CallArg{N}`, `Annotation`, `PatternType`, …) so every failure can say *why* two types had to match, pointing at the right span.
2. **Solving** (`unify.go`, `solve.go`): unification over a substitution map, with occurs check. Signature: `Solve(cs []Constraint, ps []Pred) (Subst, []Pred, []diag.Error)`. The residual-predicate return is the exact place a typeclass solver slots in — dead weight today, on purpose.
3. **Generalization** (`env.go`): **solve-at-binding hybrid.** At each block binding and top-level definition, generate the RHS's constraints, solve them immediately, then generalize (quantify free vars not free in the environment); instantiate with fresh metas at each use site. Fully deferred solving would need implication constraints — a research-grade rabbit hole only justified once local typeclass evidence exists. fango is pure, so no value restriction is needed for *soundness* — but a **monomorphism restriction applies to block bindings** *(decision 2026-09-02, S5 implementation)*: only syntactic functions and lambda literals generalize locally; value bindings stay monotypes. Rationale: a generalized block binding lambda-lifts and re-evaluates per use (§8.4), while §3.6 pins value bindings to eager evaluate-once-at-their-line semantics — and Number-kinded generalization (§7.3) would otherwise quietly turn *every* numeric-literal local (`k = 10 : number`) into a lifted generic definition. Top-level values still generalize, exactly as §8.4 specifies.

Annotations (`area : Shape -> Float`) are checked by *skolemizing* their variables (fresh rigid constants) and unifying against the inferred type — so an annotation claiming more polymorphism than the body delivers errors correctly. *(Representation pinned 2026-09-02, S5 design pass:)* skolems and scheme-bound variables share one representation — **rigid `TVar`s** (`Rigid` flag). A rigid var is *atomic* in unification: rigid == rigid of the same ID is equal, rigid vs. anything else is a mismatch, and a meta always binds *toward* the rigid var with a kind check (a Number-kinded meta against a General rigid var fails: "the annotation says `a`, but the body needs a number" — never the reverse binding, which would silently solve the skolem away). After a binding's solve, a skolem bound into an outer-scope meta is the annotation-too-polymorphic error. Rigid vars are invisible to substitution, defaulting, and the free-variable checks — load-bearing because rigid IDs are small per-definition indices that numerically collide with global meta IDs.

`type` declarations populate constructor schemes (`Circle : Float -> Shape`; `Just : ∀a. a -> Maybe a`) and a **constructor table** (`typeName → [{Name, Arity, FieldTypes}]`) shared by pattern checking, future exhaustiveness checking, and codegen. The table also records whether any field type transitively contains a function — the input to the `==`-at-function-types compile-time rejection (§8.6). `Bool` is an ordinary ADT (`True | False`) predefined in the environment — no special cases in the checker (codegen maps it to native Go `bool`, [§8.1](#81-value-representation)).

Two performance-motivated checker-side restrictions (both with negative tests): **polymorphic recursion is rejected** (HM can't infer it, and annotations don't enable it under skolemize-and-unify checking), and **non-regular (nested) recursive ADTs are rejected** — recursive occurrences must be applied to exactly the type's own parameters. Both would be useless without the other, and both would break the Go-generics compilation strategy ([§8.4](#84-polymorphism-via-go-generics)).

### 7.3 Numbers without typeclasses

Elm's `number` trick: type variables carry a kind flag; `+ - *` get the scheme `Number a ⇒ a -> a -> a` via a Number-kinded var that only unifies with `Int`, `Float`, or other Number vars. **Decision (2026-09-02, S5 design pass): Number-kinded vars generalize**, exactly like Elm — `double x = x + x` gets `double : number -> number`, usable at both `Int` and `Float`. Quantification is driven by the binding's *type*: Number metas free in the generalized type quantify (kind preserved in the scheme); Number metas interior to a body that never reach the type still **default to Int** in elaboration, as before. Codegen cost of the decision is nil: a `number` quantifier compiles to a Go type parameter constrained by `fangort.Number` (`interface { ~int64 | ~float64 }`), on which `+ - *` and all comparisons (`== /= < …`) are *native Go operators*, and untyped integer constants are assignable — no boxing, no dispatch ([§8.4](#84-polymorphism-via-go-generics)). `/` is `Float -> Float -> Float` (an `//` integer division can come later; it must define divide-by-zero behavior, since Go panics on integer division by zero while float division yields IEEE ±Inf/NaN). When typeclasses arrive, this becomes the `Num` class.

The Number machinery is **checker-only**: by the time Core is built, elaboration has zonked and defaulted every Number var, so each `BinOp` has a ground type and compiles to a *native Go operator* on `int64`/`float64` — no runtime helpers, no dispatch (see [§8.6](#86-numbers-and-operators)).

### 7.4 Error reporting

Every failed constraint renders as: a title derived from `Why` ("Type mismatch in the 2nd branch of this `case`"), a source excerpt with caret underline (from `internal/source`), and expected/actual types pretty-printed with variables normalized to `a, b, c`. Elm-quality errors are a stated goal; the `Why`+`Span` plumbing is the design feature that makes them possible.

## 8. Compilation to Go

The most novel part of the design, and — with performance a day-0 goal — the most opinionated. Guiding principles:

1. **The Go type system *is* the runtime.** Every fango type maps to a real Go type. Monomorphic fango code contains **zero `any`, zero interface assertions on calls, zero reflection**. Dynamic dispatch appears only where semantics require it: ADT constructor discrimination, and indirect calls through function *values*.
2. **HM makes Elm's runtime tricks static.** Elm needs runtime arity dispatch (F2/A2) because its target, JS, is untyped. fango knows the exact type of every application site after inference, so arity and instantiation are resolved 100% at compile time.
3. **Codegen is type-directed**, therefore inference runs before codegen, and a typed Core IR ([§8.7](#87-the-core-ir)) carries solved types and explicit instantiations to the backend.
4. **Boxing exists only as a designed escape hatch** (`TAny` + explicit `Box`/`Unbox` in Core) — unused by MVP programs, reserved for the few futures Go generics can't express (higher-kinded dictionaries, polymorphic recursion).

**Alternative rejected: uniform `any` boxing** (every value `any`, every call through `func(any) any`, reflection-based equality/printing). It is the simplest correct scheme and an earlier draft chose it, but it taxes *every* operation in the language to simplify the compiler — the wrong trade given the goals. **Alternative rejected: compiler-side monomorphization.** Full specialization per instantiation duplicates code, slows compilation, and complicates future separate compilation; Go's own generics achieve most of the benefit with the Go compiler doing the work (GC-shape stenciling: all pointer-shaped instantiations share one compiled copy, so code size and compile time stay bounded).

### 8.1 Value representation

| fango | Go | Notes |
|---|---|---|
| `Int` | `int64` | Not `int`: identical overflow semantics on every GOARCH. |
| `Float` | `float64` | |
| `String` | `string` | |
| `Bool` | `bool` | `True`/`False` → `true`/`false`; `case` on Bool → `if`. Bool remains an ordinary ADT *in the checker*; codegen special-cases it. |
| `a -> b` | `func(A) B` | Compositional and curried — see §8.2. |
| type variable `a` | Go type parameter `A` | |
| ADT `T a…` | generic marker interface + one struct per constructor with **typed fields** | |

ADTs compile to typed structs, constructed as pointers:

```go
// type Shape = Circle Float | Rect Float Float
type T_Shape interface{ isT_Shape() }
type C_Circle struct{ F0 float64 }
func (C_Circle) isT_Shape() {}
type C_Rect struct{ F0, F1 float64 }
func (C_Rect) isT_Shape() {}
```

Construction is `&C_Circle{2.0}` — one heap allocation, one-word interface payload; scrutiny is `case *C_Circle:` (value receivers make the pointer type implement the marker). Pointer construction avoids field copies on construction and match, and makes zero-field constructors (`Nil`) effectively free (a zero-sized allocation is Go's shared `zerobase` — no real allocation).

**Constructor discrimination: type switch, no tag field.** A Go type switch over concrete pointer types compiles to a chain of itab-pointer comparisons — a few ns, and most ADTs have ≤ 4 constructors. An int tag can't beat that cheaply (reading it needs a method call or `unsafe`). Two measured-need-only upgrades are documented but not planned: `unsafe` header-tag jump tables for many-constructor ADTs, and **flattened value structs** for small non-recursive monomorphic ADTs (eliminates the allocation entirely).

### 8.2 Calling convention

The type mapping is **curried and compositional**: `T⟦a -> b⟧ = func(T⟦a⟧) T⟦b⟧`, so `Int -> Int -> Int` *as a first-class value* is `func(int64) func(int64) int64`. This must be the rule — a flattened `func(int64, int64) int64` mapping is non-compositional and breaks the moment `id : a -> a` is instantiated at a function type. Uncurried representations exist for *known* functions:

1. **Worker**: every top-level (or lambda-lifted) definition with n syntactic parameters emits one uncurried Go function: `func v_area(shape T_Shape) float64`, `func v_add(x, y int64) int64`.
2. **Saturated known call** (exactly n args): a direct static call — zero overhead, identical to handwritten Go.
3. **Oversaturated call** (> n args, worker returns a function): one direct call, then typed indirect calls.
4. **Partial application** (< n args): allocates exactly one closure at the site whose body calls the worker: `v_inc := func(y int64) int64 { return v_add(1, y) }`.
5. **First-class use of a known worker** (passed as a value): a per-function **curried wrapper**, generated only on demand. Constructors get the same treatment; saturated constructor applications are struct literals.
6. **Unknown callee** (function is a parameter/local/computed): a plain **typed indirect call** `e(a)` — Go func values are (code ptr, env ptr) pairs; still no `any` anywhere.
7. **Lambdas** are typed func literals; a block-bound lambda used only saturated compiles to a directly-called local.

There is **no runtime arity dispatch and no `fangort.Apply`**. `fangort` shrinks to print helpers, string builders for derived show, and the (future) Box seam.

### 8.3 The reference program, compiled

```go
func v_area(v_shape T_Shape) float64 {
	switch s := v_shape.(type) {
	case *C_Circle:
		v_r := s.F0
		return 3.14159 * v_r * v_r
	case *C_Rect:
		return s.F0 * s.F1
	}
	panic("fango: unreachable") // exhaustiveness-checked (§8.5)
}

func main() {
	fangort.PrintFloat(v_area(&C_Circle{2.0}))
}
```

Within rounding error of handwritten Go.

### 8.4 Polymorphism via Go generics

`∀a b. …` becomes `func f[A, B any](…)`. Codegen **always emits explicit instantiations** (`v_map[int64, string](…)`) read off the Core IR — never relying on Go's own inference.

```go
// type List a = Cons a (List a) | Nil
type T_List[A any] interface{ isT_List() }
type C_Cons[A any] struct { F0 A; F1 T_List[A] }
func (C_Cons[A]) isT_List() {}
type C_Nil[A any] struct{}
func (C_Nil[A]) isT_List() {}

// map : (a -> b) -> List a -> List b
func v_map[A, B any](v_f func(A) B, v_xs T_List[A]) T_List[B] {
	switch m := v_xs.(type) {
	case *C_Cons[A]:
		return &C_Cons[B]{v_f(m.F0), v_map[A, B](v_f, m.F1)}
	default: // *C_Nil[A] — exhaustive
		return &C_Nil[B]{}
	}
}
```

The HM ↔ Go-generics corner cases have been audited:

- **Local binding polymorphism**: Go has no generic func literals, so any block binding whose generalized scheme quantifies a variable is **lambda-lifted** to a top-level generic function (free term variables become leading parameters; the enclosing definition's type params become leading type params). This is the classical equivalence; purity + strictness make capture-by-value trivially sound. Monomorphic locals stay ordinary Go locals — lifting applies only to generalized bindings, keeping output readable.
- **Top-level polymorphic values**: Go has no generic package vars, so `empty : List a` compiles to a **nullary generic function** `func v_empty[A any]() T_List[A]`, instantiated and called per use. Cost rule to note: a polymorphic *value* re-evaluates per use — observationally transparent in a pure language, and trivial cases inline. Monomorphic top-level values remain package `var`s (Go's dependency-ordered init matches pure-value semantics); `main`'s effect is forced inside `func main()`.
- **Polymorphic recursion**: rejected by the checker (HM can't infer it, and fango's skolemize-and-unify annotation checking doesn't enable it, unlike Haskell) — so Go's instantiation-cycle limit is never hit. **Non-regular (nested) recursive ADTs are rejected** for the same reason.
- **Instantiation at enclosing type params**: Go permits `v_id[A](x)` and `case *C_Cons[A]:` where `A` is the enclosing function's type param — which covers every HM case after generalization. **Internal unconstrained variables** (e.g. `print (length Nil)`) are defaulted by the elaborator: Number-kinded → `Int`, general → `Unit` (parametricity guarantees unobservability). After defaulting, Core contains no metavariables — an asserted invariant.
- **Number-kinded quantifiers** (`double : number -> number`, §7.3) compile to type params constrained by `fangort.Number` (`interface { ~int64 | ~float64 }`) instead of `any`. Go's type-set semantics give native `+ - *`, comparisons, and equality on such params, and untyped integer constants are assignable to them (representable by every type in the set) — so literals inside Number-generic bodies emit unchanged. The interpreter-side consequence (an erased `int64` literal meeting a `float64` at run time) is handled by numeric promotion, [§9.5](#95-interpreter-internals-internaleval).
- **Closures capturing type params**: fine in Go; concrete at every instantiated caller.
- **The boxing fallback, defined now and unused in MVP**: Core reserves `TAny` (repr: `any`) with explicit `Box`/`Unbox` coercions. The compiler falls back **only** when a Go instantiation can't be expressed: higher-kinded type-constructor variables (a future `Functor` dictionary), polymorphic recursion or rank-N types if ever admitted. Also noted: Go **methods** can't have type params — all derived operations are free functions.

### 8.5 Pattern matching: decision trees + exhaustiveness (in core scope)

With performance day-0, **Maranget-style decision-tree compilation and exhaustiveness checking are core-plan features, not post-MVP**: the typed Core IR supplies the constructor table and solved scrutinee types anyway; exhaustiveness makes the fallback arm statically unreachable (last constructor becomes `default:`, no panic path); and decision trees examine each scrutinee position once instead of re-testing per branch. Trees compile to type switches for constructors, `switch`/`if` for literals and Bool. Case-in-expression-position is ANF-hoisted by the elaborator into statement context (`var tmp τ; …assign…`) so hot paths avoid closure tricks.

### 8.6 Numbers and operators

By Core time every operator has a ground type, so operators compile to **native Go operators**: `+ - *` on `int64`/`float64`; fango `/` → Go `/` on `float64` (IEEE semantics: ±Inf/NaN, no panic); `++` on String → Go `+`; comparisons → native operators on `int64`/`float64`/`string`.

Equality and show are **generated per type, no reflection**:
- Ground scalars: native `==`, `!=`, `<`…
- Monomorphic ADTs: derived `func eqT_Shape(a, b T_Shape) bool` (type switch, field-wise) and `showT_Shape` (via `strings.Builder`), emitted **on demand** — only if `==`/`print` is used at that type.
- **`==` at a type whose payload transitively contains a function is a compile-time error** ("`==` is not supported at types containing functions") — decidable because eq is derived per ground type, and it stays decidable in S5, where hidden-eq arguments are synthesized from the solved element type. Elm's runtime crash on function equality is thereby avoided.
- Generic ADTs: derived with explicit element operations — `func eqT_List[A any](eqA func(A, A) bool, a, b T_List[A]) bool` and symmetrically `func showT_List[A any](showA func(A, bool) string, v T_List[A], nested bool) string`; codegen synthesizes the element-op arguments from the solved *ground* instantiation at each use site (scalars → tiny shared helpers over `fangort`; nested ADTs → func-lits wrapping the inner derived function, composing to any depth). Synthesis walks the full ground type, so `List (Maybe Int)` pulls in `eqT_Maybe` even though `Maybe` never appears in `List`'s declared fields.
- `==` at a *type variable* (`member : a -> List a -> Bool`) is Elm's `comparable` problem, solved here as a **hidden `eqA func(A, A) bool` parameter** on the enclosing generic function — i.e., single-method dictionary passing, a deliberate dry run of the reserved typeclass machinery. **Staging pinned (Decision 2026-09-02, S5 design pass): S5 ships only the derived generic eq/show above — `==` and `print` work at every *ground* instantiation. `==` (or `<` …) at a type containing a type variable remains a compile-time error ("arrives with typeclasses") until the typeclass question (open question #3) is decided; Number-kinded variables are the exception, since Go compiles their comparisons natively (§7.3).**

### 8.7 The Core IR

The structural cost of type-directed codegen, paid explicitly: **Core** (`internal/core`), a small typed IR between inference and `go/ast`, produced by **elaboration** (`internal/elaborate`):

- Every node carries its solved type; every variable/constructor occurrence carries explicit type arguments.
- Elaboration performs: zonking; Number/Unit defaulting; lambda-lifting of polymorphic block bindings; hoisting polymorphic top-level values to nullary generic functions; collapsing curried application spines into `App{CalleeKind: Worker|Ctor|Value, Args}` with saturation analysis (§8.2); pattern-match compilation to decision trees; exhaustiveness diagnostics; (future) Box/Unbox insertion and eq-dictionary threading.
- Asserted invariants after elaboration: no metavariables, no `TAny` (in MVP), every App consistent with its callee's Go arity. A Core re-typechecking "linter" runs under a debug flag and in tests.
- Core prints as S-expressions → golden tests for defaulting, lifting, and match trees, independent of Go emission.

Codegen then becomes a boring, syntax-directed Core→`go/ast` walk. Net effect vs. the boxed design: one extra package, and complexity relocates from codegen into elaborate, where it is unit-testable.

### 8.8 Emission mechanics

Unchanged in spirit from the earlier draft: construct `go/ast` and print with `go/format.Node` — never string templating — through ~20 small builder helpers, making syntactically invalid output nearly impossible and `--emit-go` gofmt'd and *more* readable than the boxed design (typed structs and real signatures instead of `any` and `Apply` chains). Name mangling: `v_` values, `C_` constructors, `T_` types — disjoint namespaces, greppable; shadowing via numeric suffixes. `//line` directives (Go panics pointing at fango source) remain post-MVP.

### 8.9 Build driver and compile speed

Compile latency is a budgeted feature. **Persistent build directory** instead of temp dirs: `.fango/build/` next to the entry file (`FANGO_BUILD_DIR` override; `~/.cache/fango/<hash>/` fallback for bare files):

```
.fango/build/
  go.mod       # "module fangobuild" — written once; zero deps, so no network, no go.sum, GOPROXY=off
  main.go      # generated; rewritten only when bytes differ
  fangort/      # materialized from go:embed, stamped with the compiler's build hash
  bin/main     # output binary
```

Mechanics that make rebuilds near-no-ops:
- **Deterministic codegen**: sorted emission order, stable mangling counters, `go/format` — identical fango input ⇒ byte-identical `main.go`.
- **Write-if-changed** on every file, so `go build`'s content-based cache hits; unchanged programs rebuild in ~no time. `GOCACHE` is the user's default, shared across projects — `fangort` and the Go stdlib compile once per machine per Go version.
- `fango clean` removes `.fango/build`. **Any `go build` failure on generated code is by definition a fango compiler bug**: an "internal compiler error" banner, build dir preserved.
- `fango run` builds then execs the binary, inheriting stdio and exit code. Clear error naming the Nix dev shell if `go` is missing from `PATH`.
- **Later**: one Go package per fango module turns Go's package build cache into an incremental compiler for free — the module milestone's perf story, reserved now.

**Budgets** (CI-gated medians, >20% regression fails): warm-unchanged hello `fango run` < 200 ms end-to-end; warm-changed < 500 ms; cold < 3 s. The §11 ADT-heavy program (~500 lines, gated from S4) shares those budgets except warm-changed < 1 s — `go build` of its ~1500-line generated main.go dominates. The §11 generics-heavy program (gated from S5) uses the same budgets as the ADT program.

### 8.10 Runtime performance ceiling and staged upgrades

Monomorphic first-order fango ≈ handwritten Go by construction. The known remaining gaps, each with a designed (post-MVP, benchmark-gated) fix:

1. Closure allocation in higher-order code → local uncurrying + Go compiler inlining.
2. Recursion vs. loops on list-heavy code → **self-tail-call → loop transform** in Core→Go (cheap and local; Go grows stacks, so deep non-tail recursion is safe, just slower).
3. One interface allocation per ADT node → flattened value structs for small monomorphic ADTs.
4. Many-constructor dispatch → header-tag jump tables (`unsafe`), only if benchmarks demand.

## 9. Interactive REPL (`fango repl`)

**Decision: a tree-walking interpreter over the *same typed Core IR* the compiler consumes** (`internal/eval`), sharing lexer → parser → inference → elaboration with the compiled backend and diverging only at the last mile. The compiled backend remains how fango programs *ship*; the interpreter is how they are *conversed with* — and, as a structural bonus, a second independent implementation of Core semantics that **differentially tests the compiler** on every e2e program.

### 9.1 Architectures considered

| Alternative | Why rejected |
|---|---|
| **Recompile-per-input through the real backend** (accumulate decls, `go build` + run each input) | Latency floor: every input is a warm-*changed* build (~500 ms class) — fails "feels dynamic" permanently. Worse, the **recomputation problem is fatal**: values can't survive across process runs, so an expensive earlier binding re-runs on *every* subsequent input. Caching can't rescue it: **closures and partial applications cannot be serialized** (Go func values are code pointers, meaningless across binaries), so cacheability would depend invisibly on a binding's type. |
| **Interpret the generated Go with yaegi** | Our codegen leans on Go generics everywhere — exactly yaegi's most bug-ridden corner (10+ open generics issues as of late 2026, e.g. #1610, #1577, #1704; last release April 2024). Also a heavy dependency, and *two* semantic-drift surfaces (ours to Go, yaegi's to Go) instead of one. |
| **Go `plugin` loading** | Unusable: broken/second-class on macOS (our dev platform), plugins can never be unloaded (every `:reload` leaks), exact toolchain-version match required. |
| **Session server** (persistent compiled process; each input compiled fresh, talking to it) | Different address spaces ⇒ every boundary crossing must serialize values ⇒ same closure-serialization wall. No sound variant exists for a higher-order language. |
| **Hybrid: compiled `:load`ed modules + interpreted inputs in one process** | Impossible — compiled fango is statically-linked Go; a running process cannot link new object code into itself. |

The interpreter is small because Core arrives pre-chewed — zonked, defaulted, lambda-lifted, saturation-analyzed, match-compiled: an estimated **1,000–1,500 lines** (a value type, environments, one switch over Core nodes, decision-tree evaluation, ~15 builtins). Tree-walking is 20–100× slower than compiled output, which is irrelevant interactively. And note the irony resolved: **boxed values are correct *here*** — the design's rejection of uniform boxing was about shipped code; the REPL is exactly where boxing belongs.

One free escape hatch: the session keeps every binding's *source*, so a future `:compile` command can synthesize a program from the session and hand it to `fango build` — batch export, not live mixing.

### 9.2 Session model

Session state (`internal/repl`): the checker's typed env (`name → (Scheme, generation)`), a generational type table and constructor table keyed by type `Unique` ([§7.1](#71-types)), a value env of **lazy memo cells** (`name → Cell{src, coreRHS, memo, forced}`), and an ordered source log of interactive inputs (drives `:reload` reconciliation).

| Input | Action |
|---|---|
| expression | infer → elaborate → eval → print `value : Type` |
| value declaration | check + elaborate; install/replace a lazy cell; nothing evaluates yet |
| `type T … = …` | new *generation* of `T`; old generation's values remain valid at their old type |
| `:type e` | infer + generalize, print the scheme; no evaluation |
| `:load Foo.fango` | parse + check the file (all-or-nothing); install bindings as lazy cells |
| `:reload` | re-load remembered files; reconcile interactive bindings (§9.4) |
| `:quit` / Ctrl-D | exit |

The REPL grammar is the module grammar minus the header, plus bare expressions — no new syntax. Multi-line input continues while the layout stack is open (the offside machinery already knows when a declaration is incomplete).

Definitions and `:type` echo **generalized schemes** (`id : a -> a`, `double : number -> number`) — from S5, the REPL is where polymorphism is most visible, so the printer shows quantified variables normalized to `a, b, …` (`number, number2, …` for Number-kinded) rather than the elaborator's defaulted monotypes. Bare-expression echo likewise generalizes for display; evaluation is unaffected.

One session-specific rule *(2026-09-02, S5 implementation)*: **prompt value declarations follow the block-binding monomorphism restriction** — functions and lambdas generalize, plain values stay monotypes (their unconstrained variables default, so `none = Nothing` is `Maybe ()` at the prompt). A prompt value is a lazy memo cell, evaluated once; a *generalized* value is a nullary generic worker re-evaluated per use, which would observably interact with redefinition (`y = x + 1; x = 10` must not change a forced `y`, §9.3). Batch modules are immutable, so top-level values there generalize per §8.4. S6 (REPL hardening) may lift this by capturing definition-time environments.

### 9.3 Redefinition semantics

- **Values**: redefinition installs a new cell; the name resolves to the new generation. Closures created earlier captured the old value — sound in a pure language, and documented: redefinition does not retroactively rewrite old closures (GHCi behaves identically).
- **Types are generative per generation** (the GHCi model), carried by `TCon.Unique`: redefining `type Shape = …` mints `Shape#2`. Old values of `Shape#1` still work with old functions, but mixing generations is a type error. The pretty-printer prints bare `Shape` normally and disambiguates ("`Shape` (defined earlier)") only when two generations collide in one message.

### 9.4 Module load and reload

- **`:load` bindings are lazy with memoization.** Purity makes lazy-memo observationally equal to strict evaluation (modulo divergence/`print` timing, neither of which arises since the REPL never auto-runs `main`), and loading a module with an expensive top-level table costs nothing until touched.
- **`:reload`**: re-parse + re-check the file — **on failure, the old module stays live** (a broken save must not torch the session). On success: drop the module's cells and type generations, then **reconcile interactive bindings** — re-typecheck each one's recorded source in definition order against the new module env; keep those that still check (memo reset so they see reloaded definitions), drop the rest with a one-line notice each. Strictly better UX than GHCi's drop-on-conflict, and nearly free because we keep source, not just values.

### 9.5 Interpreter internals (`internal/eval`)

```go
type Value = any // one of:
//   int64, float64, string, bool                — scalars (Bool native, matching codegen)
//   *CtorVal { Ctor core.CtorID; Args []Value } — CtorID carries the type Unique
//   *Closure { Fn *core.Fn; Env *Frame }
//   *Partial { Fn core.WorkerID; Got []Value }  — partial application of a known worker
```

- **Environments**: `Frame{parent, vars}` — mutable during construction, immutable after; purity means closures can share frames by pointer. (Upgrade path: elaborator-assigned slot indices — not MVP.)
- **Application reuses Core's saturation-analyzed spines directly**: saturated worker call → fresh frame + eval body; under-saturated → `*Partial`; constructor → `*CtorVal`; unknown callee → eval to `*Closure`/`*Partial` and feed args. The same saturation metadata codegen consumes — bugs in it surface in *both* backends' differential diff.
- **Type erasure is clean by construction**: by elaboration time, type arguments affect runtime *only* through eq/show at type variables — and those are already explicit hidden function parameters in Core ([§8.6](#86-numbers-and-operators)). The interpreter ignores type args and follows the dictionary parameters (it must *not* shortcut with universal deep-equality — that would silently diverge from the compiled path). *S5 status:* no dictionaries exist yet (§8.6 staging) — every `==`/`print` site is ground, where the interpreter's structural equality and the compiled derived functions agree by construction. Nullary-generic top-level values re-evaluate per use, exactly matching the compiled cost rule.
- **Numeric promotion** (S5, consequence of Number generalization §7.3): an integer literal inside a Number-generic body stays `int64` under erasure while the compiled backend converts it to the instantiated type — so the interpreter's numeric primitives (arithmetic, comparisons, structural equality's scalar leg, literal-switch dispatch) promote `int64 → float64` when the other operand is a float, with Go's conversion semantics, keeping the backends bit-identical. Residual divergence at |literal| ≥ 2⁵³ is whitelisted in [§9.6](#96-consistency-with-the-compiled-backend).
- **Cancellation**: evaluation runs under a `context.Context`, polled at function entry and case dispatch every ~4096 steps.

### 9.6 Consistency with the compiled backend

- **Differential testing**: every `testdata/run/*.fango` runs through *both* backends in CI, stdout diffed byte-exact, starting the moment Core exists. Plus `testdata/repl/*.txt` transcript goldens (scripted session → expected output) covering redefinition, generational errors, `:load`/`:reload`.
- **One shared formatting implementation, as a hard rule**: `runtime/fangort` is a real importable package; the interpreter (inside the compiler binary) imports the *same* show/format functions the generated code links — float formatting, ADT rendering, all of it, specified once.
- **Legitimate divergences are whitelisted, and the list is exactly two**: (1) top-level binding evaluation *timing* (compiled: strict package init; REPL: lazy memo) — observable only via top-level divergence or top-level printing, both excluded from the differential suite; *once effects land (S7), top-level bindings are checked pure ([§10.5](#105-the-io-story-main-and-top-level-purity)) and this entry dissolves — only pure-divergence timing remains.* (2) *(added 2026-09-02, S5)* an integer literal with |value| ≥ 2⁵³ in a Number-generic body, instantiated at `Float` and stored **unoperated** into an observable position: the interpreter keeps the exact `int64` while the compiled backend rounds through `float64` conversion. Every *operated* case is covered by numeric promotion (§9.5); the unoperated corner is vanishingly rare and honestly listed rather than papered over. Any other behavioral diff is a bug by definition.

### 9.7 The "feels dynamic" bar

Startup **< 50 ms** to first prompt (in-process, no `go build`); per-input latency **< 20 ms** for typical inputs (parse + infer + elaborate + tree-walk). Type errors and runtime panics are recovered per-input — **no input can kill the session**. Ctrl-C cancels the running evaluation via context (prints `Interrupted.`, re-prompts); Ctrl-D quits. Line editing: MVP stays zero-dependency (plain line reading; `rlwrap fango repl` documented), with a maintained pure-Go readline + `~/.fango_history` in the polish milestone.

`fango check` is *not* replaced: it remains the scriptable checker for CI/editors/tests; the REPL is the human fast-feedback tool. They share every line above the last mile.

### 9.8 Illustrative session

```
$ fango repl
fango 0.1 — :help for commands
> type Shape = Circle Float | Rect Float Float
type Shape defined
> area s = case s of
|     Circle r -> 3.14159 * r * r
|     Rect w h -> w * h
area : Shape -> Float
> c = Circle 2.0
c : Shape
> area c
12.56636 : Float
> :load Geometry.fango
loaded Geometry.fango (perimeter, unitCircle)
> type Shape = Circle Float | Rect Float Float | Tri Float Float Float
type Shape redefined (previous definition still referenced by: c, area)
> area (Tri 3.0 4.0 5.0)
-- TYPE MISMATCH -----------------------------------------------
`area` expects the earlier definition of `Shape`, but `Tri`
constructs the current `Shape` (they are distinct types).
> area c        -- old function + old value: still fine
12.56636 : Float
> :reload
reloaded Geometry.fango
kept: c, area (re-checked against reloaded module)
> :quit
```

## 10. Effects and IO: algebraic effects with handlers

**Decision (2026-09-02): fango uses algebraic effects with one-shot handlers** — effect rows in function types, `effect` declarations, `handle` expressions, direct-style code — compiled via **evidence passing** with **goroutine-backed one-shot continuations** for the rare general case. This resolves open question #1.

### 10.1 Alternatives rejected

- **Elm-style managed effects (`Cmd`/`Sub`)**: rejected outright — not suitable for a general-purpose language (user decision).
- **A built-in `IO a` type** (monadic, Elm-`Task`-style: `andThen`/`map`/`succeed`, compiled to `func() A` — essentially free at runtime): simple and familiar, but **permanently** monomorphic-monadic, because the `Monad` class that would redeem it is higher-kinded — the one thing the unboxed Go-generics backend cannot express ([§8.4](#84-polymorphism-via-go-generics)). That means bind pyramids forever, three unrelated `andThen`s for `Maybe`/`Result`/`IO`, no generic `traverse`, and an IO-only `do` sugar pointing at a door the backend has welded shut. Candidate A walks the language toward that dead end; effects route-plan around it.
- **Hybrid staging (ship `IO a`, migrate to effects later)**: a trap. The idioms differ in every type signature and call site (`readLine : IO String` vs `readLine : () ->{IO} String`); migration would rewrite the stdlib and all user code. The honest bridge already exists: the `print` builtin cheat, which touches one function, not the type system.

### 10.2 Surface design

```elm
effect Console
    print    : String -> ()
    readLine : () -> String

main : () ->{IO} ()
main _ =
    a = readLine ()
    b = readLine ()
    case (parseInt a, parseInt b) of
        (Just x, Just y) ->
            print (showInt (x + y))

        _ ->
            print "not numbers"

-- A handler: run Console purely (e.g. in tests).
collect : (() ->{Console, e} a) ->{e} ( a, List String )
collect action =
    handle action () of
        print s ->
            resume ()          -- resume: the (one-shot) continuation, implicitly bound

        readLine () ->
            resume ""

        return x ->
            ( x, gathered )    -- optional return clause wraps the normal result
```

- Function arrows carry an effect row: `readLine : () ->{Console} String`; an empty row means *provably pure*. Effect polymorphism is **inferred** by ordinary HM generalization: `map : (a ->{e} b) -> List a ->{e} List b` — one `map` for pure and effectful functions alike, no annotation needed.
- Handler clauses follow `case` layout ([§5](#5-lexer-and-layout) rules reuse unchanged). `resume` is a keyword-bound one-shot continuation.
- Direct style needs sequencing: statement bodies (§3.6, in the language since S2) already provide it — effectful bindings sequence naturally, and S7 only adds the typing rule that non-final *expression* statements must be `()` (stacked `print`s in `main`).
- **Exceptions**: a `Fail err` effect (`throw : err ->{Fail err} a`) subsumes them; stdlib `try : (() ->{Fail err, e} a) ->{e} Result err a` reflects effects into values. `Result` stays for data, `Fail` for control.

### 10.3 Type system: rows in the constraint solver

`TFun` gains an effect component — **the field exists from day 0** ([§7.1](#71-types)), like `TCon.Unique` and `Pred`:

```go
type TFun struct { Arg Type; Eff Row; Ret Type }
type Row struct {
    Labels []EffLabel  // sorted by effect Unique; each label at most once
    Tail   Type        // nil = closed row; TVar{Kind: RowVar} = open
}
```

- **Simple rows with distinct labels** (Rémy-style, as in Unison), not Koka's scoped labels — duplicates exist to type masking/shadowing and roughly double unification subtlety; nested same-effect handlers still work operationally (innermost evidence wins). Masking can come later.
- **Row unification**: partition labels into common/left-only/right-only; unify type args of common labels (`State Int ~ State a` ⇒ `a := Int`); closed rows reject extras; open tails absorb them (fresh ρ when both open); occurs check extends to row vars. One new case in the existing solver, ~300 lines; `RowVar` joins the existing `VarKind` (precedent: `Number`).
- Curried functions carry a row per arrow; effects sit on the arrow whose call performs them — partial application is pure.
- **Defaulting** (elaboration, beside Number defaulting): residual row vars at monomorphic top levels and on `main` close to their known labels. **Top-level bindings must be pure (empty row)** — see §10.5.
- **Error quality**: empty rows print as plain `->`; a quantified tail appearing nowhere else prints as `{Console}` not `{Console | e}` (Unison's convention); new `Why` tags (`EffectEscapes`, `EffectMismatch`, `HandlerRemoves`) with dedicated negative-test goldens.
- **Honest cost: +40–50% checker complexity** (row type ~100 loc, unification ~300, generalization/defaulting ~200, printing/diagnostics ~200, syntax ~300). Less than typeclasses would cost, and with a payoff elsewhere: Elm-style records (open question #4) want the *same* row engine — one engine, two clients.

### 10.4 Compilation: evidence passing + the goroutine trick

**Handlers are evidence — and fango already does evidence.** Polymorphic `==` compiles to a hidden typed function parameter ([§8.6](#86-numbers-and-operators)); effects generalize this. A `handle` builds an **evidence struct** (one typed function field per operation); `perform` is a typed indirect call through it; evidence threads as hidden parameters to functions whose row names the effect *concretely*. Effect-*polymorphic* functions (`map`, `compose`) need **no evidence parameter at all** — the effectful closure they receive already captured its evidence at creation. `map` compiles to exactly the Go it compiles to today (Effekt-style capability passing, simpler than Koka's full evidence vectors).

**Tail-resumptive handlers — the overwhelmingly common case** (clause body is `resume e`, used once, in tail position: Reader, State get/put, Console, logging, throw): the clause becomes a plain closure in the evidence struct; `perform` costs one typed indirect call (~2–5 ns), unboxed, readable:

```go
type Ev_Console struct {
    Op_print    func(string) struct{}
    Op_readLine func(struct{}) string
}

// greet : () ->{Console} ()  — concrete row ⇒ hidden evidence param, like hidden eqA
func v_greet(ev_Console *Ev_Console, _ struct{}) struct{} {
    return ev_Console.Op_print("Hello, " + ev_Console.Op_readLine(struct{}{}))
}
```

A tail-resumptive State handler gets the classic optimization: it is semantically a dynamically-scoped mutable cell, so `get`/`put` become reads/writes of a Go local shared by the evidence closures — invisible outside the handler, purity preserved at the fango level.

**General handlers** (non-tail `resume`, `resume` unused, or used under other control flow: generators, backtracking, async): need one-shot delimited continuations, and **a parked goroutine *is* a one-shot continuation** — the §2 bet paying out again. `handle` runs its body in a goroutine; a general op's evidence shim sends `(payload, reply-channel)` and parks; the handler clause gets a continuation value `k`; `resume k v` unparks the body where it performed; discarding `k` closes the channel, and the perform site unwinds its goroutine via a private panic sentinel (defers run, nothing leaks). ~150 lines in `fangort` (`RunGeneral`, `Perform`, `K.Resume`/`K.Discard`).

- **One-shot only** — goroutine stacks can't be cloned, so a second `resume` is a runtime error. This is exactly **OCaml 5's design**, the strongest precedent that one-shot effects suffice for real systems.
- **Cost**: ~100–300 ns per performed op (channel round-trip + park/unpark) plus one goroutine (~2 KB) per general `handle` — paid *only* by genuinely control-flow-bending handlers. A resume-free (abort-only) handler is detectable and compilable via panic/recover as a later optimization.
- **Why not the alternatives**: full CPS transform taxes *all* code (allocation per frame, unreadable `--emit-go`, kills near-Go performance); selective CPS duplicates every effect-polymorphic function and is a whole-compiler transform — research-grade complexity; stack copying is impossible in Go.
- **The compiler decides per `handle` site** (static analysis of clauses, as Koka does); `perform` codegen is uniform either way — always an indirect call through the evidence struct — so the strategy never leaks into callee code.

Core IR grows three nodes: `Perform{Effect, Op, Args}`, `Handle{Body, Clauses, Return, TailResumptive bool}`, and a clause-scoped `Resume` binder; elaboration threads evidence (extending the eq-dictionary machinery) and runs the tail-resumptive analysis; the Core linter asserts every `Perform` reaches bound evidence.

### 10.5 The IO story, `main`, and top-level purity

- **MVP: one builtin effect, `IO`** — `print`, `readLine`, later files/net/random/time. `main : () ->{IO} ()`; the compiled entry point wraps `v_main` in the runtime's IO evidence (direct calls into `fangort` — tail-resumptive, so a plain program compiles with **zero goroutines**: hello-world is still [§8.3](#83-the-reference-program-compiled)'s Go). Fine-grained effects (`Console`, `FS`, `Net`) come later, once a stdlib exists to inform the granularity; the row machinery makes splitting `IO` a widening, not a rewrite.
- **Top-level bindings must be pure** (empty row, checked at defaulting). Consequence: strict package init (compiled) vs lazy memo (REPL) becomes unobservable except through pure divergence — **the [§9.6](#96-consistency-with-the-compiled-backend) whitelisted divergence dissolves**, exactly as flagged.
- **REPL**: prompt expressions with row `{IO}` evaluate under a default handler stack (IO → real stdio); pure expressions as before. The interpreter reuses the *same* `fangort` goroutine runtime for general handlers and evidence environments for tail-resumptive ones — so the differential suite exercises the effect runtime itself through both backends.

### 10.6 Synergies

1. **The HKT/boxing cliff is never approached** — no `Monad` class needed, ever; direct style composes effects by row union; everything stays unboxed. Given fango's backend, this is the single strongest argument for effects.
2. **Concurrency (Q2) gets its direction**: an `Async` effect (`fork : (() ->{Async, e} a) ->{Async} Future a`, `await : Future a ->{Async} a`) handled by goroutines, with the `handle` scope as a structured-concurrency nursery. A sketch, not yet a design — but the general-handler runtime *is already* the machinery it needs.
3. **Go FFI (Q6)**: imported Go functions type as `->{IO}` (pure ones with empty rows) — the purity hazard is answered by the type system.
4. **Effects weaken the case for typeclasses** (Q3): the main motivation for `Functor`/`Monad` disappears, strengthening the Elm-restraint option.

### 10.7 Known caveats (stated, not hidden)

- **Evidence escape**: a closure capturing handler evidence can outlive its `handle` (returned, stored, called later). For tail-resumptive IO-ish handlers this is dubious-but-defined; for general handlers it's a dangling continuation → **runtime error, never undefined behavior** (one-shot/liveness checks). The researched fixes (Effekt's second-class capabilities, Koka's scoped labels) are noted as a later tightening. This is the design's largest known soundness caveat.
- **Goroutine hygiene**: the `Discard`/unwind protocol is the most bug-prone part — e2e tests assert `runtime.NumGoroutine` returns to baseline; a handler that panics must `Discard` all live continuations (deferred).
- **Park/unpark cost claims** need the same benchmark treatment as everything else before they harden ([§11](#11-testing-and-benchmarking-strategy)).

### 10.8 Staging

`Eff Row` lands in `TFun` at S0 — always the closed empty row, asserted by the Core linter, unification stubbed to "both empty" — with two disciplines that keep the reserved field from becoming a trap: **(a)** printers and S-expression dumpers omit empty rows from day 0, so S0–S6 goldens don't churn when rows activate; **(b)** no speculative row *logic* early — the field, not the machinery. `print` arrives as a builtin cheat in S1 and stays one through S6. Effects land as **S7** (the interpreter exists from S0, so the effect runtime is differentially testable the day it lands).

## 11. Testing and benchmarking strategy

- **Golden tests**: token dumps (`testdata/lex/`), S-expression ASTs (`testdata/parse/`), and **Core IR dumps** (`testdata/core/` — defaulting, lambda-lifting, decision trees), with an `-update` flag to regenerate; includes layout torture cases.
- **Inference**: table-driven positive tests (`"\\x -> x"` ⇒ `"a -> a"`, with normalized var names) and negative tests asserting the error's `Why`-category and span line; whole-program error goldens via `fango check`; negative tests for polymorphic recursion and non-regular ADTs.
- **End-to-end, differential**: `testdata/run/*.fango` with `.expected` (exact stdout) or `.error` (compile-error substring); a single Go test runs each program through **both backends** — compiled binary *and* the Core interpreter ([§9.6](#96-consistency-with-the-compiled-backend)) — and diffs stdout byte-exact against the expectation *and each other*. Every language feature is checked by two independent implementations from the milestone it lands. `GOCACHE` keeps repeated builds fast; compiled runs gated behind `testing.Short()`.
- **REPL transcript goldens**: `testdata/repl/*.txt` scripted sessions covering redefinition, generational type errors, `:load`/`:reload` reconciliation, and interruption.
- **Benchmarks are first-class and exist from S0** (`benchmarks/`), because performance is a goal, not a hope:
  - *Compile latency*: `fango run` on hello-world, a ~500-line ADT-heavy program, and (from S5) a ~500-line generics-heavy program with many distinct instantiations — the empirical detector for risk #3's Go-generics build blowup — in three modes: cold cache, warm-changed (one function touched), warm-unchanged. CI compares medians against checked-in baselines; >20% regression fails.
  - *Runtime*: paired programs — `perf/<name>.fango` vs. `perf/<name>_baseline.go` (idiomatic handwritten Go): list sum/map/filter chains, binary-tree build+fold (GC pressure), string concat/show, naive fib (call overhead), a pattern-match-dense interpreter loop. The harness reports fango/Go ratios per commit. **Gates are per-case: ≤ 1.2× on scalar/first-order code; ≤ 3.0× on ADT/list-heavy and higher-order code** (the stated 2–3× target, gated at its ceiling). Baselines stay *idiomatic monomorphic* Go — slices and concrete structs, not generic Go — because "what a Go programmer would actually write" is the honest ceiling, which is exactly why list-heavy cases get the looser gate (a cons list races a slice). The ratio table is the arbiter for every "do we need tag headers / value structs / tail-call loops yet?" question — upgrades ship on benchmark evidence, not speculation.
- CI is one command: `go test ./...` (plus the bench gate).

## 12. Development environment

The repo is a **Nix flake** (`flake.nix`); all dependencies come from it — no Homebrew, no global installs. `nix develop` (or direnv via the checked-in `.envrc`) provides Go, `gopls`, and `gotools`. This is the canonical toolchain for building the compiler *and* the backend `go build` step.

## 13. Milestones

**Milestones are vertical slices, not horizontal layers** *(restructured 2026-09-02; the original ladder built one pipeline stage per milestone)*. Every slice runs the **entire** pipeline — lexer → parser → infer → elaborate → Core → codegen → `go build` — *plus* the Core interpreter and REPL, over a growing language subset. The rule that **nothing built is later thrown away** still holds, enforced differently: S0 lands every stage in its *final architecture* (constraint-based inference with the `Pred` seam, `Eff Row` present-but-empty, `TCon.Unique` identity, type-directed codegen through Core) over a one-type language, so later slices only add cases to stages, never reshape them. What slicing buys over layers: the REPL is genuinely day-0 (a stated goal), and the differential harness (interpreter vs. compiled binary) checks every feature from the slice it lands in, not from a mid-project milestone. Each slice ends green and demo-able:

- **S0 — One integer through the whole machine.** Language: `main = <Int arithmetic expr>` — integer literals, `+ - *`, parens; nullary top-level values with use-after-define references (duplicates and cycles rejected — a value cycle would otherwise surface as a Go init-cycle build failure, i.e. an auto-ICE); comments; tab rejection; all future keywords reserved. `+ - *` are typed via **Number-kinded metavariables defaulted to Int in elaborate**, so the kind machinery is real and S1's Float is purely additive. Everything else in the repo layout exists by the end of S0: CLI (`build|run|check|repl|clean`, `--emit-go`), persistent `.fango/build` + embedded `fangort`, **the compile-latency benchmark and its CI gate**, golden suites (lex/parse/core) with `-update`, the Core linter, the differential e2e harness (compiled programs produce no output yet; a test-internal print-main codegen mode observes `main`'s value through the shared `fangort` formatter), interpreter, and REPL. Demo: `fango run` exits 0 in < 200 ms warm; the REPL evaluates `1 + 2 * 3` to `7 : Int`.
- **S1 — Scalars complete, `if`, real output.** `Float`/`String` literals, unary minus, `/` (Float-only), `++`, comparisons at ground types, `Bool` as an ordinary checker ADT (native `bool` in codegen), `if/then/else` — and **the `print` builtin cheat lands here**, the only no-output→output transition. `fangort` float formatting (what `12.56636` depends on) is decided and golden-tested here, shared by both backends.
- **S2 — Statement-style bodies and annotations.** Layout rule 3 (statement-column alignment, §3.6) gets its first real client; blocks in declaration bodies only (function bodies S3, case branches S4); solve-at-binding generalization call structure lands (still monomorphic — the mechanism is final, the quantifier count is zero); annotation checking shaped for S5 skolemization; surface `TypeExpr` grammar. Layout torture goldens begin.
- **S3 — Functions.** Top-level functions, lambdas, curried application, recursion; the full [§8.2](#82-calling-convention) calling convention monomorphically — `App{CalleeKind}` spine collapsing, saturation analysis, workers, direct calls, partial-application closures, on-demand curried wrappers, typed indirect calls. The interpreter gains closures/partials off the same saturation metadata, so spine bugs surface as backend diffs. First runtime-ratio benchmark: naive fib vs. handwritten Go, ≤ 1.2× gate.
- **S4 — ADTs, `case`, decision trees, exhaustiveness.** Monomorphic `type` decls (§3.7 — the full grammar parses, type parameters included; the checker rejects parameterized declarations with a staged "arrives in S5" error + negative tests) → marker interfaces/structs, constructor table (keyed by `Unique`), type/constructor namespace + arity checks, case layout (rule 2), pattern constraints, **Maranget decision trees + exhaustiveness checking**, derived eq/show at ground types (`==` rejected at function-containing types, §8.6). **The reference program prints `12.56636` — MVP met, at native speed.**
- **S5 — Polymorphism end-to-end.** Parameterized ADTs (deleting S4's staging error — the grammar has parsed them since S4); generic workers with explicit instantiation; lambda-lifted polymorphic block bindings; nullary generic top-level values; Number-kinded generalization (`double : number -> number`, §7.3); **derived generic eq/show with element-operation parameters, synthesized at ground instantiations** (`==`/`print` at `List Int` works; `==` at a type variable stays a staged error until typeclasses — §8.6 staging ruling); polymorphic-recursion and non-regular-ADT rejection with negative tests; `map`/`filter`/`foldr` demo; **the full runtime perf suite vs. handwritten baselines lands here** (it needs lists and trees); self-tail-call → loop if benchmarks demand it.
- **S6 — REPL hardening.** The REPL has existed since S0; this is the session model on top: generational redefinition (the `Unique` plumbing has been live since S0), `:load`/`:reload` reconciliation, cancellation UX, transcript goldens ([§9](#9-interactive-repl-fango-repl)).
- **S7 — Effects & IO.** `effect`/`handle`/`->{}`/layout-block syntax; row unification, generalization, defaulting; evidence-passing elaboration + tail-resumptive analysis; `fangort` general-handler runtime; interpreter `handle`/`perform` sharing that runtime; `print` cheat replaced by the `IO` effect; top-level purity check; differential + goroutine-leak tests ([§10](#10-effects-and-io-algebraic-effects-with-handlers)).
- **S8 — Polish.** Error-message quality pass (including row-error goldens), `--emit-go` UX, readline + history for the REPL, README with grammar sketch, bench-gate tuning.

**Honest costs of slicing, accepted with mitigations:** S0 is the largest slice (~3 k source lines; no demo until it completes) — mitigated by an internal build order that reaches `fango run` on a hardcoded Core program mid-slice, recovering the old driver-first checkpoint. Layout machinery ships its full final API in S0 with only the top-level rule exercised (rules 3 and 2 get clients in S2/S4) — mitigated by direct unit tests on the predicates. The golden dump formats (AST/Core S-expressions, diagnostics shape, empty-row/empty-`Pred` printer omissions) must be frozen in S0 or every slice churns goldens — they are S0 design deliverables, not incidentals. Inference is architecturally over-built for `main = 1 + 2` — deliberate, per the seam philosophy; the three-file structure and `Solve` signature are final in S0 so later slices only add cases.

### Top risks

1. **Instantiation plumbing bugs** — wrong or missing type arguments reaching codegen is the new hard part → Core invariant checker (no metavariables, arity-consistent applications) always on in tests; Core goldens for defaulting/lifting; `go build` failures remain auto-kept internal compiler errors.
2. **The Go generics ceiling** (no higher-kinded types, no method type params, no generic vars/literals) blocking typeclasses later → the `TAny`/Box seam and hidden-eq-parameter proto-dictionaries are specified now; dictionaries are structs of element-typed functions, which Go generics handle; only higher-kinded dictionaries ever box.
3. **Generics build-time/code-size blowup** on instantiation-heavy programs → GC-shape stenciling bounds it structurally; the S0 latency gate detects it empirically; worst-case valve is boxing specific instantiations (all-pointer shapes share a stencil).
4. **Higher-order/allocation overhead keeping list code >3× handwritten Go** → saturation analysis + demand-only curried wrappers in the base design; tail-call loops and flattened small-ADT structs queued behind benchmark evidence.
5. **Layout correctness** (the classic failure mode of offside-rule languages) → isolate in `layout.go` with column-tracking predicates only; the predicates are unit-tested from S0 and layout goldens accrete with each rule's first client (S2 blocks, S4 `case`).
6. **Semantic drift between the two backends** — two implementations of Core semantics, forever → the differential suite in CI from S0; one shared `fangort` formatting implementation (hard rule); a totality test asserting the interpreter's switch covers every Core node; divergence whitelisted to exactly the documented cases ([§9.6](#96-consistency-with-the-compiled-backend)).
7. **Generational type identity** (`Unique`-based unification, reload reconciliation) is subtle → `Unique` in `TCon` from day 0 with redefine-then-unify unit tests; transcript goldens exercising cross-generation errors. The elaborator must also tolerate *incremental* (per-input) operation — session-scoped fresh-name supply, tested with polymorphic block bindings and defaulting in transcripts.
8. **Row inference degrading error messages** (the Elm-quality goal) → dedicated `Why` tags for effect failures; empty rows and lone quantified tails hidden by the printer; negative goldens for every row-error shape from S7 day one.
9. **Evidence escape and goroutine hygiene in the effect runtime** ([§10.7](#107-known-caveats-stated-not-hidden)) → one-shot/liveness runtime checks (error, never UB); `NumGoroutine` leak assertions in e2e; the interpreter shares the same runtime, so both backends exercise it differentially.

## 14. Open questions

Deliberately unresolved — the agenda for iterating on this document:

1. ~~**Effects & IO.**~~ *Resolved 2026-09-02: algebraic effects with one-shot handlers ([§10](#10-effects-and-io-algebraic-effects-with-handlers)); Elm-style managed effects and a monadic `IO` type were rejected.*
2. **Concurrency.** The Go runtime's biggest gift is goroutines + channels. Direction set by the effects decision ([§10.6](#106-synergies)): an `Async` effect handled by goroutines with the `handle` scope as a structured-concurrency nursery. Still to design: `Future` semantics, cancellation, channels/select, whether actor-style processes layer on top.
3. **Typeclasses vs. Elm-style restraint.** The seam is reserved (`Pred`, residual predicates), and polymorphic `==` already prototypes dictionary passing as hidden typed function parameters ([§8.6](#86-numbers-and-operators)) — which compile to fast, non-boxed Go. But *should* fango have typeclasses at all, or keep Elm's simplicity with a few kind-flagged var tricks (`number`, `comparable`)? If yes: which core classes (`Eq`, `Ord`, `Show`, `Functor`…), and is coherence enforced? Note: higher-kinded classes (`Functor`, `Monad`) are the one place the Go-generics mapping can't stay unboxed ([§8.4](#84-polymorphism-via-go-generics)) — a real design constraint on how far up the abstraction ladder fango should climb.
4. **Records.** Elm-style structural records with row polymorphism, or nominal records? Note: the effects design already commits the solver to a row-unification engine ([§10.3](#103-type-system-rows-in-the-constraint-solver)) — structural records could reuse it, which materially strengthens that option. *Narrowed 2026-09-02: records are confirmed out of S4, and inline record payloads in constructors are excluded from `type` syntax regardless of the eventual answer (§3.7); if records go nominal, the Go mapping is one struct per declaration with an implicit constructor — cheap and row-free — which is a real point in nominal's favor for a perf-gated Go target. Also declined for now: an `alias` keyword (§3.7).*
5. **Module system.** Multiple modules, `import`/`exposing` semantics, cross-module compilation strategy (one Go package per fango module?), and what `exposing` hides at codegen level.
6. **Go interop / FFI.** Calling Go functions from fango would unlock the whole Go ecosystem. Direction set by the effects decision: foreign imports type as `->{IO}` (empty rows for pure ones), so the purity hazard is answered by the type system. Still to design: the import declaration syntax, type mapping at the boundary, and error/panic translation.
7. **Standard library.** Minimum viable core: `List`, `Maybe`, `Result`, `String`, `Dict`? Implemented in fango itself, in Go under the hood, or a mix?
8. ~~**Exhaustiveness & decision trees.**~~ *Resolved 2026-09-02: moved into core scope (S4) as part of the performance-first codegen design ([§8.5](#85-pattern-matching-decision-trees--exhaustiveness-in-core-scope)).*
9. **Numeric tower.** Is Elm's `Int`/`Float` + `number` var enough? Integer overflow semantics (`int64` wraps)? Arbitrary precision? What are `//` integer-division-by-zero semantics?
10. **Tooling.** Formatter (layout syntax makes this valuable early) and LSP server — which first? *(REPL: resolved 2026-09-02 — designed in from day 0 as a Core interpreter, [§9](#9-interactive-repl-fango-repl).)*
11. **Name & branding.** Resolved 2026-09-02: the language is named **fango** (file extension `.fango`), renamed from the original working name "funk", which was already in use.

---

*Document history: initial draft 2026-09-02, distilled from the architecture-planning session that chose the Go-source backend. Revised same day: performance promoted to a day-0 goal — codegen redesigned from uniform-`any` boxing to type-directed unboxed compilation with Go generics, a typed Core IR, decision-tree matching in core scope, persistent cached builds, and CI-gated compile/runtime benchmarks. Second revision same day: interactive REPL added as a day-0 requirement — a Core-IR interpreter (GHCi model) with generational type identity (`TCon.Unique`), lazy-memoized module loading, `:reload` reconciliation, and differential testing between the two backends. Third revision same day: effects/IO decided — algebraic effects with one-shot handlers (Elm-style and monadic-IO rejected), row-typed functions, evidence-passing compilation with goroutine-backed general handlers, landing as milestone M7 with the `Eff` row field reserved in `TFun` from day 0. Fourth revision same day: milestones restructured from horizontal layers (M0–M8) to vertical slices (S0–S8) — the entire pipeline, interpreter, REPL, and perf gate land in S0 over an Int-only arithmetic subset, and the language grows feature-by-feature end-to-end from there. Fifth revision same day: `let … in` replaced by keyword-less statement-style bodies (§3.6) — the first deliberate divergence from Elm surface syntax; rationale: direct-style effects (S7), one scoping rule shared with the top level, existing layout machinery. Elm's `let/in` and a `let`-keyword statement form were considered and declined; `let`/`in` stay reserved words. Sixth revision same day, from the S4 design pass: `type`-declaration surface rules pinned down (§3.7) — RHS is constructors-only (no alias-flavored `type`), separate type/constructor namespaces with per-module uniqueness, full-application arity checking, function payloads allowed with `==` rejected at compile time for function-containing types (§8.6); type parameters parse in S4 but are checker-rejected until S5; records confirmed out of S4 with inline record payloads excluded from constructor syntax; an `alias` keyword considered and declined for now. Seventh revision same day, from the S5 design pass: eq/show staging pinned — S5 ships derived generic eq/show with element-operation parameters at ground instantiations only, `==` at type variables deferred to the typeclass decision (§8.6); Number-kinded variables generalize (Elm's `number` polymorphism) and compile to Go type params constrained by `fangort.Number` (§7.3, §8.4); skolems represented as rigid type variables with atomic unification rules (§7.2); interpreter numeric promotion plus a second whitelisted divergence for unoperated ≥2⁵³ literals at Float instantiations (§9.5, §9.6); runtime perf gates made per-case (1.2× scalar, 3.0× list-heavy) with idiomatic monomorphic Go baselines, and a generics-heavy compile-latency program added (§11).*
