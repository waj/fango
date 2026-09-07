# Roadmap: compile-time metaprogramming

This is a [roadmap](roadmap.md) proposal under iteration; nothing here is
implemented. When accepted and built, durable results move to
[the design](design.md) and [the reference](reference.md) and this file is
removed.

## Problem

fango has exactly one code-generation mechanism, and it is closed. `deriving`
is implemented by `(*Checker).DeriveDecl` in `internal/infer/derive.go`, a Go
function that synthesizes `*ast.InstanceDecl` nodes with local `ref`/`call`/
`str`/`pattern` combinators and routes them through `ck.InstanceDecl`. Its
architecture is right — generated code is checked and elaborated through
exactly the same path as handwritten methods — but its class list is a literal
`cl.Name != "Basics.Eq" && cl.Name != "Basics.Show"`, and everything else gets
`CANNOT DERIVE`.

The cost is already visible in the tree:

- `Ord` has four independent methods (`lt`, `gt`, `le`, `ge`; there is no
  `compare` and no `Ordering`), so every custom ordering is four near-duplicate
  hand-written methods — and `Ord` cannot be derived at all.
- `stdlib/Basics.fango` writes out the scalar instances of the standard classes
  longhand, one method at a time, for every scalar type.
- None of the types in `examples/markdown.fango` derive anything, so they have
  no equality and no display. That is a reasonable local choice precisely
  because deriving is not extensible: there is nothing to derive them *into*.
- `doc/roadmap-examples.md` puts a serialize/parse round trip in the Tier-2
  Todo CLI. There is no JSON module, and no way to generate a codec per type
  without writing it out by hand for each one.

The general shape of the missing capability: **inspect a type at compile time
and emit specialized code for it**, without a runtime reflection mechanism and
without breaking modularity.

## Why not a `Generic`-style structural representation

Haskell's `Generic` maps a type to a sum-of-products representation and writes
one generic function over that representation. Two independent reasons rule it
out here.

**It is not expressible.** A structural representation needs classes over type
constructors — `Rep` is an associated type of kind `* -> *`, and the generic
combinators are instances over `:+:` and `:*:`. fango's classes have exactly
one parameter and no higher kinds (`doc/design.md`, "Known limitations"), so
there is nothing to write the representation in.

**It would move a correctness-shaped burden onto an optimizer.** Erasing a
`Rep` back into direct field access depends on aggressive inlining. fango's
only partial evaluator is `specializeScalars` in
`internal/elaborate/specialize.go`, deliberately bounded to effect-free workers
with one numeric type parameter, and `doc/design.md` is explicit that
`(*elab).fold` exists as a correctness requirement rather than an optimization.
Making generic-representation erasure load-bearing for performance would invert
that stance.

The alternative direction — emit the specialized code directly, at compile
time — needs no type-system extension and no optimizer.

## Why fango can afford staged code generation cheaply

Template Haskell is the reference point for "emit code at compile time", and
most of what makes it complicated is already solved here by existing
architecture rather than by new machinery.

1. **Name resolution produces canonical symbols before inference.** The
   resolver in `internal/modules/modules.go` (`(*resolver).canon`) rewrites
   every module-level declaration and imported reference to an opaque
   `Module.name` symbol, and merges all modules into one declaration list in
   graph order. A quote compiled in module `J` therefore already holds
   fully-resolved identities. Splicing it into module `M` cannot capture `M`'s
   names and cannot be captured by them. **Hygiene is close to free**, and it
   is the good kind: a quote sees the *quoting* module's scope, never the
   splice site's.

2. **Compilation is whole-graph, dependency-first, and source-ordered.**
   Top-level declarations are scoped in source order and forward references are
   rejected (`doc/design.md`, "Language semantics"). That is already a stage
   discipline: anything a splice can name has already been checked. There is no
   need for Template Haskell's rule that a splice's function must live in
   another module, no cross-stage persistence problem, and no
   recompilation-avoidance problem — the graph is re-checked on every batch
   build and `sources.json` already hashes every input.

3. **Purity is checkable, not conventional.** Compile-time code is ordinary
   fango and must type with an empty effect row. "No IO during compilation" is
   enforced by the effect system, not by a `Q`-monad honor system, so
   reproducible builds follow from the type checker rather than from
   discipline.

There is also direct precedent for running the evaluator inside the compiler:
`(*elab).fold` in `internal/elaborate/elaborate.go` dispatches `NativeCall`
through `natives.Lookup` and calls `spec.Eval` during compilation, and
`internal/repl` runs the full check → `elaborate.Decl` → `eval` loop
incrementally against a persistent `Checker` and `eval.Env`.

## Proposal

Add a compile-time stage to the existing pipeline, with three pieces:

- an opaque `Code` type whose only constructor is a **quote**;
- **type reflection** bounded by ordinary export visibility;
- **derivers**, which open `deriving` to user classes, plus general splices.

Compile-time code is ordinary fango, evaluated by `internal/eval` during
inference.

### One representation: opaque `Code`

`Meta.Code` is abstract. There are no constructors, and deliberately no ADT
mirroring `internal/ast`. The only way to build a `Code` is a quote containing
real fango syntax, with `$(...)` holes:

```fango
quote (Json.push $(acc) (encode $(f.value)))
```

Internally a quote compiles to a compiler-side template — a resolved
`ast.Expr` retaining its original `source.Span`s — plus an ordered list of hole
expressions. The interpreter value is opaque (`Value = any` already permits
this), so nothing about the AST leaks into the language and the AST can evolve
without a matching fango-level ADT to keep in lockstep.

Because templates keep the quoting module's spans, a type error in generated
code can point at **the deriver author's own source line** rather than at a
synthesized position.

Quotes are not type-checked where they are written — holes have no known type
yet. They are checked when spliced, reported at the splice site with a
"while expanding" frame naming the deriver and the type. This is the main
honest cost of the design, and the span retention is what keeps it tolerable.

A quote needs **no new Core node**. It lowers to
`Meta.makeCode : Int -> List Code -> Code` — a compiler-side template index
plus the evaluated holes — as a saturated `NativeCall`, whose arity and
declaration instantiation `core.Lint` already checks against `Prog.Natives`.
Because the language is strict, holes evaluate eagerly and in source order.

`Code` is one of four **compile-time-only types**, alongside `Decls`,
`TypeInfo`, and `TypeRepr`:

> A definition whose type mentions a compile-time-only type is not emitted, and
> no runtime-reachable expression may have such a type.

This rule is load-bearing rather than tidy. `internal/codegen` has no
dead-code elimination — every `Prog.Def` is emitted, deliberately, so that
adding a downstream consumer does not change a dependency's generated package
— so without it a deriver body would reach Go codegen and `Meta.makeCode` would
need a runtime implementation that has no meaning. The rule does double duty by
also keeping `Code` values from leaking into the running program, and it is a
purely type-directed test, so it needs no reachability analysis.

### Two stage operators, one inequality

`quote` and `$(...)` are inverses, and together they are the entire staging
surface:

- `quote (e)` goes **up** a stage. It does not evaluate `e`; it builds a `Code`
  describing it.
- `$(e)` goes **down** a stage. It evaluates `e` now and pastes the resulting
  `Code` where it stands.

These are one operation at two depths, not two rules. Track a quote depth:
ordinary program text is depth 0, `quote` is +1, `$` is −1, and **depth −1 is
compile time**. So `$(...)` inside a quote fills a hole, while `$(...)` in
ordinary program text runs during compilation and pastes generated code into
the program being compiled. The first increment permits depths −1 and 0 only:
depth 1 is a nested quote and depth −2 is a second compile-time stage, both
rejected.

One inequality then subsumes several rules that would otherwise be ad hoc, but
it applies to **local** binders only: **a local binder introduced at depth −1 is
usable only at depth −1, and one introduced at depth 0 only at depth 0.** Local
binders are lambda parameters, block bindings, and case binders.

**Top-level definitions are stage-polymorphic** — usable at either depth. That
is what lets a splice operand name ordinary definitions at all, and it is the
one piece of cross-stage persistence the design keeps from Template Haskell.
So `helper = $(precompute 10)` is an ordinary runtime value that a later
deriver may also consult at depth −1.

The local-binder restriction is why a splice cannot see the splice site's
locals — they live at depth 0 while the operand runs at depth −1 — so staged
code that needs runtime values is applied as a function:

```fango
addThree = $(mkAdder 3)
```

It is also what rejects a deriver leaking a compile-time value into generated
code without `Meta.lift`.

### Type reflection, bounded by visibility

One new expression form, `typeOf T`, yields a `Meta.TypeRepr`: an opaque
nominal identity backed by `types.TCon.Unique` — never a name string — plus its
arguments. On top of it:

- `Meta.sameType`, `Meta.head`, `Meta.args`, `Meta.isVar`
- `Meta.typeName`, for display only
- `Meta.info : TypeRepr -> Maybe TypeInfo`

`Meta.info` returns `Just` **only when the type's schema is visible where
`typeOf` was written**. This is the whole modularity story for reflection, and
it introduces no new rule: `exposing (Type)` reflects as an opaque `TypeRepr`,
`exposing (Type(..))` reflects as a full `TypeInfo`, exactly as those forms
already govern constructors and record field schemas
(`(*iface).selection` in `internal/modules/modules.go`). An abstract type stays
abstract at compile time precisely as it does at runtime.

`TypeInfo` exposes `{ name, module, params, shape }` with
`Shape = Union (List Ctor) | Record (List Field)`, read out of
`types.ADTInfo`, `types.CtorInfo`, and `types.RecordFieldInfo`. Because records
are already single-constructor ADTs sharing one type identity and one deriving
path, reflection needs no second representation either.

Identity-based comparison is what makes specialization modular. A deriver that
wants a fast path for `Int` writes `Meta.sameType f.ty (typeOf Int)` — naming a
type its own module already depends on — rather than matching a name string it
does not own.

**There is deliberately no `reify`.** A splice cannot ask the compiler about a
name it did not itself name, and cannot ask whether some instance exists. That
query is what breaks modularity in Template Haskell, because its answer depends
on what happens to be in scope where the splice runs. A deriver instead *emits
a method call* and lets ordinary instance resolution handle it, which is what
`DeriveDecl` already does today when it emits `Basics.show` on a field.

### Derivers

A `deriver` declaration opens `deriving` to user classes:

```fango
class Encode a
    encode : a -> Json

deriver Encode
    encode info value =
        Meta.match info value (\bound ->
            List.foldl
                (\acc f -> quote (Json.push $(acc) (encode $(f.value))))
                (quote (Json.start $(Meta.lift bound.ctor.name)))
                bound.fields)
```

`type Point = { x : Int, y : Int } deriving (Encode)` then runs it.

Two rules keep this small.

**The deriver signature is mechanical.** For a class method with `n` arrows,
its deriver method takes a `TypeInfo` plus `n` `Code` arguments and returns
`Code`:

| method | deriver method |
| --- | --- |
| `encode : a -> Json` | `TypeInfo -> Code -> Code` |
| `show : a -> String` | `TypeInfo -> Code -> Code` |
| `eq : a -> a -> Bool` | `TypeInfo -> Code -> Code -> Code` |
| `fromInt : Int -> a` | `TypeInfo -> Code -> Code` |

There is no type erasure story and no new type-system machinery; a deriver is
an ordinary fango function checked by ordinary inference.

**The compiler owns the traversal skeleton.** `Meta.match info scrutinee f`
builds the exhaustive case and binds the fields, handing the callback a
`Bound { ctor, fields }` whose fields carry `{ name, index, ty, value : Code }`.
`Meta.construct ctor codes` builds the other direction, for methods that
produce an `a`.

A deriver therefore **never invents a binder**. That removes the other half of
Template Haskell's complexity — `newName`/`mkName`, and the capture questions
that follow from them — and it makes exhaustiveness structural rather than
checked after the fact. Nesting two `Meta.match` calls produces exactly the
nested-case shape `derive.go` builds for `Eq` today, so no `match2` primitive
is needed.

The instance head stays compiler-owned (`C (T a b ...)`), so derived instances
land in the existing whole-graph overlap check unchanged and coherence is not
affected.

Instance **contexts become use-driven**. Today `DeriveDecl` scans every
constructor field and adds a predicate for each polymorphic one — a syntactic
over-approximation. Under this proposal the residual predicates inference
already produces from the generated body become the instance context, so a
field the deriver never touches and a phantom parameter stop demanding
evidence. The machinery mostly exists: `(*Checker).qualify` already retains
residual predicates, `hasTypeVars` already defers polymorphic ones, and
`ck.checkingInstance` already supplies self-evidence for direct recursion. What
is new is a mode in which `InstanceDecl` accepts an undeclared context and
adopts the residuals instead of reporting `MISSING CONSTRAINT`. This is the one
piece of real type-system work in the proposal.

### Splices

An expression splice is a `$(...)` at depth 0. It evaluates its operand at
compile time and checks the resulting `Code` in place — a compile-time computed
table, a matcher specialized to a literal pattern, a codec chosen from a
`TypeInfo`.

A declaration splice generates a *group* of definitions. **There is no `splice`
keyword**: `$(...)` is the only escape, and a declaration splice is a `$(...)`
in the body position of a top-level definition. Here modularity needs one real
rule, because generated top-level names are the only thing that can escape a
splice:

> **Every name a module defines appears literally in that module's source.**

So declaration quotes have *anonymous* top-level binders, and the definition
site names them positionally with an arity check. A single generated definition
needs no new syntax at all; a group extends the binder to a comma-separated
list:

```fango
getX = $(Meta.accessor (typeOf Point) "x")          -- no new form
getX, getY = $(Meta.accessors (typeOf Point))       -- one new form
```

That is one new declaration shape — a comma-separated binder list, legal only
when the body is a splice — and zero new keywords, and it puts generated names
exactly where every other fango definition puts them: to the left of `=`. The
invariant above becomes visually obvious rather than a rule to remember.

Generated declarations then behave exactly like handwritten ones from that
point in the file: the same source-order scoping, the same no-shadowing rule,
and exporting one still requires listing it in `module ... exposing (...)`,
which `buildInterface` already validates. `grep` and a future LSP stay honest.

Variadic generation — one definition per field, count unknown — is
deliberately *not* served by this form. That is what derivers are for, and the
split is the point: **variadic generation goes through instances; named
generation goes through names the author wrote.**

A declaration splice may generate `ValueDecl`, `TypeDecl`, `ClassDecl`, and
`InstanceDecl` only. `module`, `import`, `infix`, and `native` are rejected:
those four are precisely the declarations that change graph or global
structure, and `infix` and `native` are bundled-only besides.

### Declaration quotes

A declaration quote is a `quote` followed by an indented block, delimited by
the offside rule like every other fango construct:

```fango
accessors : Meta.TypeInfo -> Meta.Decls
accessors info =
    quote
        _ : $(Meta.typeCode info) -> Int
        _ r = $(Meta.projection info "x")
```

Its top-level binders are anonymous, per the rule above. The exact spelling of
anonymous binders and of type holes inside a declaration quote is unsettled;
see the open questions.

## Modularity threat model

Each threat, and the existing mechanism that answers it.

| Threat | Answer |
| --- | --- |
| Generated code captures, or is captured by, splice-site names | Quotes hold canonical symbols produced by `(*resolver).canon` before inference. A quote resolves in its own module, always. |
| A splice queries the compiler about names it has no import edge to | No `reify`. Reflection starts from `typeOf T` at a site that could have written `T` by hand. |
| A splice sees through an abstract type | `Meta.info` returns `Nothing` unless the schema is visible, following the existing `Type` versus `Type(..)` rule. |
| A deriver dispatches on a type it does not own | Comparison is `TCon.Unique` identity against a `typeOf` the deriver's module can name — never a name string. |
| Compile-time code observes the world, so builds stop being reproducible | Compile-time code must type with an empty effect row; the effect system rejects IO. |
| Compile-time code observes process-global state | Reachable natives must be marked compile-time-safe. `Random` is the concrete exclusion: `runSeeded` handles its effect away to a pure row but advances fangort's process-global PRNG cell, which the compiler shares. |
| Staging cycles: a deriver used before it exists | The existing source-order rule. Forward references are already rejected, so a splice can only name what is already checked. |
| Derived instances weaken coherence | The instance head stays compiler-owned, so derived instances go through the same whole-graph overlap check as handwritten ones. |
| A reader cannot tell what names a module defines | Declaration splices bind names the author wrote, to the left of `=`. Exporting still requires an `exposing` entry. |
| Non-termination at compile time | A step budget on the compile-time evaluator, reusing the existing every-N-evals poll. |

## Staging, purity, and reproducibility

- A splice operand may name any top-level definition earlier in graph and
  source order — fango's existing scoping rule reused verbatim. See
  "Same-module staging" below for why no cross-module restriction is needed.
- Compile-time code must type with an empty effect row.
- It may not transitively reach a **user Go sidecar**. The interpreter cannot
  load Go dynamically and already reports that limitation
  (`doc/design.md`, "Go backend and runtime"), so this is a diagnostic rather
  than a new restriction.
- It may not reach a bundled native that touches process-global state. The
  check is a Core reachability walk against a new compile-time-safe flag on
  `natives.Spec`, alongside the existing `Foldable`.
- Evaluation is bounded by a step budget.

Together these keep generated Go byte-identical across builds, preserving the
determinism invariant that unchanged source units emit unchanged Go.

### Same-module staging

Template Haskell's stage restriction forbids a splice from using a function
defined in the same module, because GHC compiles a module as a unit. **fango
has no such restriction**: a function returning `Code` may be defined and
spliced in one module, bounded only by source order. Three existing properties
give this, and none of them are new machinery.

**Source-order scoping is already the stage discipline.** Top-level
declarations are scoped in source order and forward references are rejected,
so a splice can only name declarations that are already checked. Running one
therefore means elaborating the already-inferred prefix on demand, which
`elaborate.Decl` already supports per declaration — it is what
`internal/repl` does on every prompt.

**Instance resolution agrees between the two stages.** This is the load-bearing
property. `(*Checker).InstanceDecl` appends to `ck.Instances` in source order,
`MatchInstance` searches that list, and `reduceObligations` discharges concrete
predicates during inference. An instance declared later in a module is
therefore already invisible to an earlier declaration, so a splice operand and
the final elaboration pass select the same evidence. There is no "this function
behaved differently at compile time than at runtime" hazard to rule out;
fango's existing ordering rule closed it before this proposal existed.

**The evaluator cannot re-enter itself.** A splice is a `$(...)` at depth 0 and
is expanded during inference, so by the time the prefix is elaborated its Core
is already splice-free. A `$(...)` at depth −1 would be depth −2, which the
depth rule rejects. Prefix elaboration therefore terminates without a
reentrancy guard.

Two ordering consequences are worth stating outright, because they are the
cases a user will hit first:

- **A `deriver` must precede, in source order, any `deriving` clause that uses
  it** — including on a type declared earlier in the same file. `(*Checker).Module`
  declares type *headers* and constructor fields in early loops, so types may
  be mutually recursive regardless of order, but it processes `ClassDecl`,
  `deriving`, `InstanceDecl`, and value declarations in one source-order loop.
  A `deriving` clause is reached at its type's position in that loop, not
  before the file's values.
- `x = $(f x)` is rejected by the existing no-self-reference rule, not by a new
  staging check.

## Diagnostics

New titles, following the existing convention:

- `CANNOT DERIVE` — extended: no deriver for this class.
- `COMPILE-TIME EFFECT` — a splice operand or deriver body is not pure.
- `COMPILE-TIME NATIVE` — reaches a user sidecar or a native that is not
  compile-time-safe.
- `COMPILE-TIME LIMIT` — step budget exhausted.
- `STAGE ERROR` — quote depth out of range, or a binder used at the wrong
  depth.
- `SPLICE ARITY` — a declaration splice produced a different number of
  definitions than the binder list names.
- `INVALID SPLICE DECLARATION` — generated a `module`, `import`, `infix`, or
  `native` declaration.
- `TYPE NOT VISIBLE` — reserved for the case where a clearer message than
  `Meta.info` returning `Nothing` is warranted.

## Phases

Sequenced so each is independently verifiable, and each names its first
fixture.

**P0 — quote and splice plumbing.** A bundled `Meta` module with opaque `Code`;
the `quote`/`$(...)` pair with the depth rule and its stage-correctness check;
a compile-time evaluator following the `internal/repl` pattern (`ck.Decl` →
`elaborate.Decl` → `eval.Env.DefineWorker`), driven from the
`ClassDecl`/`DeriveDecl`/`InstanceDecl` dispatch loop in
`(*Checker).Module`. First fixture: `testdata/run/` case whose value comes from
a splice that computes a constant, plus `testdata/check/` cases for
`COMPILE-TIME EFFECT` and `STAGE ERROR`.

**P1 — reflection.** `typeOf`, `TypeRepr`, `TypeInfo`, `Meta.info` and its
visibility rule, and `Meta.lift` as a real `class Lift` with scalar instances.
First fixture: a `testdata/modules/` case proving `Meta.info` is `Nothing` for
a type exposed as `Type` and `Just` for the same type exposed as `Type(..)`.

**P2 — derivers.** `deriver` declarations, `Meta.match` and `Meta.construct`,
and use-driven instance contexts. Port `Eq` and `Show` off
`internal/infer/derive.go` onto fango-level derivers in `Basics`, then add
`Ord`. First fixture: the existing `testdata/run/classes_derive.fango` goldens
must be **unchanged** by the port — that is the strongest available evidence
the port is faithful — followed by a new `Ord` deriving case.

**P3 — declaration splices.** `Meta.Decls`, declaration quotes, the
comma-separated binder list and its arity check. First fixture: a
`testdata/parse/` golden for the new declaration shape, plus a `SPLICE ARITY`
case.

**P4 — the driving consumer.** A bundled `Json` module with a derivable
`Encode`, used by the Tier-2 Todo CLI in `doc/roadmap-examples.md`. `Decode` is
deliberately deferred: it needs a failure story, which means `Result` or the
gated aborting handlers, so the Todo CLI's parse half stays hand-written for
now and becomes a second forcing case for `Result`.

Each of P0 through P3 adds surface syntax and therefore carries the
`editors/vscode/syntaxes/fango.tmLanguage.json` obligation in the same change,
per the repository instructions; `$(` and the quote block also affect bracket
behavior in `editors/vscode/language-configuration.json`.

## Alternatives considered

**A fango-level AST ADT, Template Haskell style.** A `Meta` module mirroring
`internal/ast` as ordinary constructors is more flexible and is what TH does.
Rejected: it is a second copy of the AST to keep in lockstep with the first,
generated code can be structurally ill-formed, and name construction
reintroduces exactly the hygiene problem that compiler-owned binders remove.
The opaque-`Code` surface can be widened later if a real deriver cannot be
written without it.

**Typed quotes, `Code a`.** Indexing `Code` by the type it produces would catch
generation errors where the deriver is compiled rather than where it is
spliced. Rejected for now: a deriver walks a `TypeInfo` that is a runtime value
of the compile-time stage, so the index has nothing static to be indexed by
without a type-level representation of types — dependent-ish machinery on top
of one-parameter classes with no higher kinds. It buys better errors at the
cost of a type-system extension larger than the feature.

**Bracketed quotes, `[| ... |]`.** fango has neither `[` nor `]` tokens, so
this is available. Rejected because the parser derives structure from token
columns rather than from closing delimiters: every construct is introduced by a
word and delimited by the offside rule, so a multi-line bracketed quote would
require the layout algorithm to exempt a closing `|]`. Declaration quotes make
that mandatory rather than optional — exactly where TH needs a second bracket
flavor, `[d| ... |]`, while a keyword-introduced block needs no delimiter at
all. `$(...)` *is* taken from TH: `$` is unused in the closed operator set, it
is instantly recognizable, and it is terse where density matters, since deriver
bodies are mostly holes. Reserving `quote` as an identifier is inside the
budget the language already spends on `in` and `as`.

**Standalone deriving in another module.** `deriving Encode for Point` written
away from `Point`'s declaration would be no worse than the orphan instances
fango already permits, provided the schema is visible at that site — which
`Meta.info` already decides. Deferred rather than rejected; it adds no new
mechanism once P1 and P2 exist.

## Open questions

- Whether use-driven instance contexts or an explicit `context` clause on the
  deriver is the right default. The clause is the fallback if adopting residual
  predicates in `InstanceDecl` proves invasive.
- The spelling of anonymous binders and type holes inside declaration quotes.
  `_` reads well for the binder but collides with the wildcard pattern; type
  holes need a way to splice a `TypeRepr` into type position, which the
  expression-only `$(...)` does not currently cover.
- Whether `typeOf` can avoid being a keyword. It takes a type rather than a
  value, so it cannot be an ordinary function, but a `quote`-based spelling may
  exist.
- Whether declaration splices may generate `TypeDecl` in the first increment.
  Generating a new nominal identity from a splice is powerful and probably
  fine, but it interacts with `checkRegularity` and with generation-stable
  `Unique` allocation.
- Whether the compile-time-only-type rule should also bar such definitions from
  a module's `exposing` list. P2 needs the opposite for `Basics`, whose
  derivers must be reachable from every module that derives, so the rule is
  probably about emission only — but the interaction with `buildInterface` and
  with orphan derivers is unexamined.
- Whether nested quotes are ever needed, and what a second compile-time stage
  would mean for a language with no cross-stage persistence beyond `Meta.lift`.
- `Meta.fail : String -> Code`, turning into a diagnostic at the splice site,
  as the deriver's error channel. This is a stopgap for the absence of
  `Result`; revisit once `Result` lands.
- Whether the compile-time evaluator should reuse elaborated Core from the main
  pass or re-elaborate on demand. Re-elaboration is deterministic and therefore
  safe, but duplicates work; reuse requires interleaving elaboration with
  inference, which is a larger change to `cmd/fango/pipeline.go`.
- Whether a failed expansion should roll back through `(*Checker).Checkpoint()`
  the way the REPL's `installInstances` does, or whether batch compilation can
  simply stop.

## Verification

The existing gates cover most of this, which is a deliberate property of
generating surface AST rather than Core:

- Generated instances go through `ck.InstanceDecl`, ordinary inference, and
  ordinary elaboration, so `core.Lint` remains the safety net for every
  expansion.
- Because expansion happens during inference — before Core — both backends see
  identical generated code, so the interpreter/compiler differential suite
  applies unchanged and is the referee for derived behavior.
- The `classes_derive` goldens pin the `Eq`/`Show` port in P2.
- `TestEmitDeterministicAndFormatted` in `cmd/fango/e2e_test.go` covers the
  determinism claim: pure, bounded, native-restricted compile-time evaluation
  must keep generated Go byte-identical.
- The REPL keeps one `Checker` and one `eval.Env`, so derivers and splices work
  at the prompt with no extra mechanism; REPL transcripts should cover a
  deriver definition followed by a `deriving` use, and rollback after a failed
  expansion.
- Diagnostic fixtures pin each new title.
- Compile-latency benchmarks are the honest risk here: compile-time evaluation
  moves work into the compiler. The cold and warm latency cases should be read
  before and after P2, with the caveat already recorded in the roadmap that
  neither performance gate is reproducible enough to run unattended.
