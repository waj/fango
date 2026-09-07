# Roadmap: compile-time metaprogramming

This is a [roadmap](roadmap.md) proposal under iteration. The compile-time
stage itself — quotes, splices, the depth rules, and the compile-time
evaluator — is implemented and documented in [the design](design.md,
"Compile-time metaprogramming") and [the reference](reference.md,
"Compile-time metaprogramming"). What remains here is everything built on top
of it: reflection, derivers, and declaration splices.

## Problem

`deriving` is still closed. It is implemented by `(*Checker).DeriveDecl` in
`internal/infer/derive.go`, a Go function that synthesizes `*ast.InstanceDecl`
nodes with local `ref`/`call`/`str`/`pattern` combinators and routes them
through `ck.InstanceDecl`. Its architecture is right — generated code is
checked and elaborated through exactly the same path as handwritten methods —
but its class list is a literal `cl.Name != "Basics.Eq" && cl.Name !=
"Basics.Show"`, and everything else gets `CANNOT DERIVE`.

The cost is visible in the tree:

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

The missing capability is now narrow: **inspect a type at compile time** and
hand the resulting description to a generator that already knows how to emit
code.

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

Emitting the specialized code directly, at compile time, needs no type-system
extension and no optimizer.

## What P0 settled

Three design choices changed while the stage was built, and each is worth
recording because the later phases inherit them.

**A quote is a Core node, not a bundled primitive.** The original plan lowered
a quote to `Meta.makeCode : Int -> List Code -> Code` as a saturated
`NativeCall`, on the theory that reusing the linter's native checks was
cheaper than a new node. It is not: `Meta` must import nothing, because
derivers will live in `Basics` and every module already loads `Basics`, so a
`List Code` argument would close an import cycle. The remaining shape — a
bundled native with a Go template that must never be emitted — was worse than
the node it avoided. `core.Quote` also buys a real invariant: the linter
rejects one outright, so "no compile-time value reaches generated Go" is
checked rather than argued.

**Two counters, not one signed depth.** Quote depth decides whether `$(…)` is
a hole; stage depth decides whether code runs at run time or compile time.
Collapsing them into a single depth made the rule for a quote inside a splice
operand ambiguous. Tracking them separately makes every case fall out.

**The native restriction is dynamic.** The plan called for a Core reachability
walk against a compile-time-safe flag on `natives.Spec`. The flag exists, but
the check happens in the interpreter's compile-time mode instead: it is
equally deterministic, has no false positives on paths a splice never takes,
and needs no walk over the reachable definition set.

## Type reflection

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

`TypeRepr` and `TypeInfo` join `Code` as compile-time-only types, so the
emission rule already implemented covers them without change.

## Derivers

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
piece of real type-system work left in the proposal.

Two ordering consequences are worth stating outright, because they are the
cases a user will hit first:

- **A `deriver` must precede, in source order, any `deriving` clause that uses
  it** — including on a type declared earlier in the same file.
  `(*Checker).Module` declares type *headers* and constructor fields in early
  loops, so types may be mutually recursive regardless of order, but it
  processes `ClassDecl`, `deriving`, `InstanceDecl`, and value declarations in
  one source-order loop. A `deriving` clause is reached at its type's position
  in that loop, not before the file's values.
- `x = $(f x)` is rejected by the existing no-self-reference rule, not by a new
  staging check.

## Declaration splices

An expression splice already generates an expression. A declaration splice
generates a *group* of definitions. **There is no `splice` keyword**: `$(...)`
stays the only escape, and a declaration splice is a `$(...)` in the body
position of a top-level definition. Here modularity needs one real rule,
because generated top-level names are the only thing that can escape a splice:

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

The threats P0 answered are recorded in the design. The ones the remaining
phases must answer:

| Threat | Answer |
| --- | --- |
| A splice queries the compiler about names it has no import edge to | No `reify`. Reflection starts from `typeOf T` at a site that could have written `T` by hand. |
| A splice sees through an abstract type | `Meta.info` returns `Nothing` unless the schema is visible, following the existing `Type` versus `Type(..)` rule. |
| A deriver dispatches on a type it does not own | Comparison is `TCon.Unique` identity against a `typeOf` the deriver's module can name — never a name string. |
| Derived instances weaken coherence | The instance head stays compiler-owned, so derived instances go through the same whole-graph overlap check as handwritten ones. |
| A reader cannot tell what names a module defines | Declaration splices bind names the author wrote, to the left of `=`. Exporting still requires an `exposing` entry. |

## Diagnostics

`STAGE ERROR`, `COMPILE-TIME EFFECT`, `COMPILE-TIME NATIVE`,
`COMPILE-TIME LIMIT`, and `COMPILE-TIME FAILURE` exist. Still to add:

- `CANNOT DERIVE` — extended: no deriver for this class.
- `SPLICE ARITY` — a declaration splice produced a different number of
  definitions than the binder list names.
- `INVALID SPLICE DECLARATION` — generated a `module`, `import`, `infix`, or
  `native` declaration.
- `TYPE NOT VISIBLE` — reserved for the case where a clearer message than
  `Meta.info` returning `Nothing` is warranted.

## Phases

**P0 — quote and splice plumbing. Done.** See the design and reference.

**P1 — reflection.** `typeOf`, `TypeRepr`, `TypeInfo`, `Meta.info` and its
visibility rule, and `Meta.lift` as a real `class Lift` with scalar instances.
First fixture: a `testdata/modules/` case proving `Meta.info` is `Nothing` for
a type exposed as `Type` and `Just` for the same type exposed as `Type(..)`.

**P2 — derivers.** `deriver` declarations, `Meta.match` and `Meta.construct`,
and use-driven instance contexts. Port `Eq` and `Show` off
`internal/infer/derive.go` onto fango-level derivers in `Basics`, then add
`Ord`. First fixture: the existing `testdata/run/classes_derive.fango` goldens
must be **unchanged** by the port — that is the strongest available evidence
the port is faithful — followed by a new `Ord` deriving case. This is the
phase that makes `Basics` import `Meta`, so `Meta` becomes a dependency of
every program and the demand-driven loader edge becomes moot.

**P3 — declaration splices.** `Meta.Decls`, declaration quotes, the
comma-separated binder list and its arity check. First fixture: a
`testdata/parse/` golden for the new declaration shape, plus a `SPLICE ARITY`
case.

**P4 — the driving consumer.** A bundled `Json` module with a derivable
`Encode`, used by the Tier-2 Todo CLI in `doc/roadmap-examples.md`. `Decode` is
deliberately deferred: it needs a failure story, which means `Result` or the
gated aborting handlers, so the Todo CLI's parse half stays hand-written for
now and becomes a second forcing case for `Result`.

P1 and P3 add surface syntax and therefore carry the
`editors/vscode/syntaxes/fango.tmLanguage.json` obligation in the same change,
per the repository instructions.

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
this was available. Rejected because the parser derives structure from token
columns rather than from closing delimiters: every construct is introduced by a
word and delimited by the offside rule, so a multi-line bracketed quote would
require the layout algorithm to exempt a closing `|]`. Declaration quotes make
that mandatory rather than optional — exactly where TH needs a second bracket
flavor, `[d| ... |]`, while a keyword-introduced block needs no delimiter at
all.

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
- Whether a quote should be allowed to introduce its own binders at all.
  P0 permits `quote (\y -> …)` and rejects using `y` from a hole, but P2's
  compiler-owned traversal means no deriver needs to write one.
- `Meta.fail : String -> Code`, turning into a diagnostic at the splice site,
  as the deriver's error channel. This is a stopgap for the absence of
  `Result`; revisit once `Result` lands.
- The compile-time evaluator re-elaborates the prefix on demand rather than
  reusing the main pass's Core. Re-elaboration is deterministic and therefore
  safe, and a program with no splices pays nothing, but a module full of
  derivers elaborates its prefix twice. Reuse requires interleaving elaboration
  with inference, which is a larger change to `cmd/fango/pipeline.go`; measure
  before doing it.
- Whether a failed expansion should roll back through `(*Checker).Checkpoint()`
  the way the REPL's `installInstances` does, or whether batch compilation can
  simply stop. Batch stops today; the REPL rolls back for declarations but a
  failed splice inside one leaves its checked prefix in place.

## Verification

The existing gates cover this, which is a deliberate property of generating
surface AST rather than Core:

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
  at the prompt with no extra mechanism; `testdata/repl/staging.in` covers
  quotes and splices, and P2 should extend it to a deriver definition followed
  by a `deriving` use, and rollback after a failed expansion.
- Diagnostic fixtures pin each new title.
- Compile-latency benchmarks are the honest risk here: compile-time evaluation
  moves work into the compiler. The cold and warm latency cases should be read
  before and after P2, with the caveat already recorded in the roadmap that
  neither performance gate is reproducible enough to run unattended.
