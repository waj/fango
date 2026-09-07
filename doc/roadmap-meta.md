# Roadmap: compile-time metaprogramming

This is a [roadmap](roadmap.md) proposal under iteration. The compile-time
stage, type reflection, and derivers are implemented and documented in
[the design](design.md, "Compile-time metaprogramming") and
[the reference](reference.md, "Compile-time metaprogramming"). What remains
here is declaration splices and the consumer that forces them.

## Problem

`deriving` is open and reflection exists, so a library can generate one
*instance* per type. It still cannot generate a *definition*. The Tier-2 Todo
CLI in [roadmap-examples.md](roadmap-examples.md) wants a serialize/parse round
trip, which needs a bundled `Json` module with a derivable `Encode` — that much
is expressible today — but a codec built around named accessors, or any
generator whose output is a top-level name rather than an instance method, is
not.

## What P1 and P2 settled

Five design choices changed while reflection and derivers were built, and each
one is worth recording because declaration splices inherit it.

**Natives never construct a fango value.** `internal/natives` sits below the
interpreter in the package graph, so it cannot build the `CtorVal` a list or
record would need. The reflection primitives therefore answer counts, indices,
names, and opaque handles, and `Meta`'s own fango code assembles the records a
deriver walks. This turned out to be a better boundary than the alternative —
every structural decision is visible in `stdlib/Meta.fango` rather than in Go.

**`Meta` carries its own list.** `List` derives its instances, so it depends on
the module that supplies the derivers, which depends on `Meta`. `Meta.Items` is
the small list that avoids that cycle. `Meta` imports only `Basics`.

**The standard derivers are a bundled module, not `Basics`.** The original
plan put `Eq`/`Show`/`Ord` derivers in `Basics`, which every module already
loads. That closes the same cycle from the other side: `Basics` would import
`Meta`, and `Meta` needs arithmetic. `Derive` sits above both and below
everything else, and a file that writes `deriving` gets a loader edge to it —
the same mechanism that gives a file that writes `quote` an edge to `Meta`.

**Instance contexts are inferred by probing.** Adopting residual predicates
inside `InstanceDecl` was invasive: the instance's symbol embeds a hash of its
context, and the instance is installed before its methods are checked. A probe
pass under `(*Checker).Checkpoint` — check with no context, collect what stays
residual, roll back, then declare exactly those — needed one new flag on the
checker and no change to instance identity.

**Binder names come from the scrutinee.** A generated `case` needs binders that
do not collide when two traversals nest, and fango forbids shadowing. Deriving
the name from a hash of the scrutinee's own rendering is pure, deterministic,
and needs no counter, which would have made expansion order-dependent and put
the determinism gate at risk.

## Declaration splices

An expression splice generates an expression. A declaration splice generates a
*group* of definitions. **There is no `splice` keyword**: `$(...)` stays the
only escape, and a declaration splice is a `$(...)` in the body position of a
top-level definition. Here modularity needs one real rule, because generated
top-level names are the only thing that can escape a splice:

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

The threats P0, P1, and P2 answered are recorded in the design. The one the
remaining phase must answer:

| Threat | Answer |
| --- | --- |
| A reader cannot tell what names a module defines | Declaration splices bind names the author wrote, to the left of `=`. Exporting still requires an `exposing` entry. |

## Diagnostics

`STAGE ERROR`, `COMPILE-TIME EFFECT`, `COMPILE-TIME NATIVE`,
`COMPILE-TIME LIMIT`, `COMPILE-TIME FAILURE`, `CANNOT DERIVE`,
`DUPLICATE DERIVER`, and `REFLECTION ERROR` exist. Still to add:

- `SPLICE ARITY` — a declaration splice produced a different number of
  definitions than the binder list names.
- `INVALID SPLICE DECLARATION` — generated a `module`, `import`, `infix`, or
  `native` declaration.

## Phases

**P0 — quote and splice plumbing. Done.**

**P1 — reflection. Done.** `typeOf`, `TypeRepr`, `TypeInfo`, `Meta.info` and
its visibility rule, and `Meta.lift` as a real `class Lift`. The
`testdata/modules/reflection/` fixture pins `Opaque` for a type exposed as
`Type` and `Visible` for the same type exposed as `Type(..)`.

**P2 — derivers. Done.** `deriver` declarations, `Meta.match` and
`Meta.construct`, and use-driven instance contexts. `Eq` and `Show` moved off
`internal/infer/derive.go` onto fango-level derivers in the bundled `Derive`
module with the `classes_derive` goldens unchanged, and `Ord` joined them.

**P3 — declaration splices.** `Meta.Decls`, declaration quotes, the
comma-separated binder list and its arity check. First fixture: a
`testdata/parse/` golden for the new declaration shape, plus a `SPLICE ARITY`
case. P3 adds surface syntax and therefore carries the
`editors/vscode/syntaxes/fango.tmLanguage.json` obligation in the same change,
per the repository instructions.

**P4 — the driving consumer.** A bundled `Json` module with a derivable
`Encode`, used by the Tier-2 Todo CLI in `doc/roadmap-examples.md`. `Decode` is
deliberately deferred: it needs a failure story, which means `Result` or the
gated aborting handlers, so the Todo CLI's parse half stays hand-written for
now and becomes a second forcing case for `Result`.

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
mechanism now that P1 and P2 exist.

## Open questions

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
  a module's `exposing` list. It is about emission only today, which is what
  lets `Meta` expose `Code`; the interaction with orphan derivers is
  unexamined.
- `Meta.fail` is the deriver's only error channel and reports
  `COMPILE-TIME FAILURE` at the splice site. It is a stopgap for the absence of
  `Result`; revisit once `Result` lands.
- The compile-time evaluator re-elaborates the prefix on demand rather than
  reusing the main pass's Core. Re-elaboration is deterministic and therefore
  safe, and a program with no splices pays nothing, but every program now
  derives something, so the prefix through `Derive` is elaborated twice in
  essentially every build. Reuse requires interleaving elaboration with
  inference, which is a larger change to `cmd/fango/pipeline.go`; measure
  before doing it.
- A stdlib parameter name that collides with an entry file's *effect
  operation* name is rejected as shadowing, because effect operations are
  declared before any module's values while ordinary values are declared in
  source order. The inference-level shadowing check is module-blind; the
  resolver's is not. Deciding whether the inference check should consult
  module visibility, or whether it is redundant in batch mode, is unfinished.

## Verification

The existing gates cover this, which is a deliberate property of generating
surface AST rather than Core:

- Generated instances go through `ck.InstanceDecl`, ordinary inference, and
  ordinary elaboration, so `core.Lint` remains the safety net for every
  expansion.
- Because expansion happens during inference — before Core — both backends see
  identical generated code, so the interpreter/compiler differential suite
  applies unchanged and is the referee for derived behavior.
- `TestEmitDeterministicAndFormatted` in `cmd/fango/e2e_test.go` covers the
  determinism claim: pure, bounded, native-restricted compile-time evaluation
  must keep generated Go byte-identical.
- The REPL keeps one `Checker` and one `eval.Env`, so derivers and splices work
  at the prompt with no extra mechanism; `testdata/repl/staging.in` covers
  quotes, splices, a deriver definition followed by a `deriving` use, and
  rollback after a failed expansion.
- Diagnostic fixtures pin each new title.
- Compile-latency benchmarks are the honest risk here: compile-time evaluation
  moves work into the compiler, and `Meta` plus `Derive` are now in every
  program's graph. The cold and warm latency cases should be read before and
  after P3, with the caveat already recorded in the roadmap that neither
  performance gate is reproducible enough to run unattended.
