# fango roadmap

This document owns priorities, unresolved decisions, and possible future work.
Implemented architecture belongs in [the design](design.md), and functionality
available to users belongs in [the reference](reference.md). Completed work is
removed after durable results are promoted to those documents; Git history is
the archive.

## Standard library expansion

Continue selecting APIs from concrete programs rather than attempting broad
coverage.

- Expand `List`, text, numeric, and IO operations as subsequent examples require
  them. Structured IO failures are coordinated in the
  [effects roadmap](roadmap-effects.md).
- Strings use valid UTF-8 storage and Unicode-scalar `Char`, indexing, and
  length. Normalization, grapheme segmentation, and Unicode-aware word or case
  operations remain deferred until an example requires them.
- Prefer fango implementations; use declared bundled natives only for semantics
  source code cannot express or when benchmark evidence demands it.
- Keep adding differential, diagnostic, documentation, and performance
  coverage with each library increment.

Independent library versioning, package distribution, and dependency fetching
remain deferred; configurable source roots are entangled with the question
below.

## List representation

The bundled `List` is array-backed, and the mechanism for a stdlib-declared
type whose representation the backends know is settled; both are described in
the design. [roadmap-list.md](roadmap-list.md) owns what is still open there:
retuning the chunk size against the new branching benchmark, whether chunks
should grow along a spine, whether cheap length and indexing should reach the
surface, and the native-acceleration path for the combinators.

## Unembedding the bundled sources

`stdlib/*.fango`, `stdlib/*.native.go`, the native support sources, and
`runtime/*/*.go` packages materialized into generated modules are compiled into
the binary with `go:embed`. Bundled Fango sources are read back through
`modules.BundledProvider`; Go support sources are copied into compiled projects
or interpreter workers. Editing a bundled module therefore has no effect until
the compiler is rebuilt. `go test` rebuilds from source and never sees it, so
the friction lands entirely on manual iteration — and now that `Derive` is a
bundled module an author has reason to open, that is a routine cost rather than
a rare one. Embedding source should go.

Two properties currently rest on it and need somewhere else to live. The
compiler hard-codes canonical stdlib symbols — `Meta.Code`, `Meta.TypeInfo`,
`Meta.infoOf`, `Basics.Eq`/`Ord`/`Show`/`Num`, `IO.print`/`readLine` — and
native validation cross-checks every bundled `native` declaration against the
interpreter registry, so a stdlib one version away from its binary is an
internal error rather than a behavioral difference. Embedding makes that skew
unrepresentable; anything else has to make it *detectable*, which means a
version stamp and a real diagnostic. The `RESERVED MODULE` rule, which today
rejects a local file named after a bundled module, needs rethinking at the
same time: it exists to enforce the same invariant from the other side.

There is a second cost worth collecting while the mechanism is open. Embedded
source is still *source*: every invocation re-lexes, re-parses, re-resolves,
re-infers, and re-elaborates the whole bundled prelude, and since P2 it also
runs the derivers for every bundled type that derives. That work is identical
on every run and is the floor under cold compile latency.

The open decisions:

- Whether bundled sources move to files beside the binary — restoring the
  edit-and-run loop directly — or to a precompiled artifact of serialized
  interfaces and Core that is loaded instead of re-checked, which also removes
  the per-invocation re-check. The two are not exclusive: source on disk for
  development, precompiled for distribution, is a third shape.
- Whether the Go runtime support follows the same rule. It is a different
  case: generated programs and interpreter workers import it, so the compiler
  must be able to materialize its source into arbitrary build directories,
  which is an argument for keeping it embedded whatever happens to the stdlib.
- How a source root is spelled, and whether it is a development-only escape
  hatch or the same mechanism the deferred package work will need. Answering
  it as a product feature is more work; answering it as a debug flag risks
  building the wrong thing twice.
- What replaces the lockstep invariant: a version stamp checked at load, a
  hash of the bundled tree recorded in `sources.json` alongside the per-file
  hashes already there, or something stricter.

## Compile-time metaprogramming

The compile-time stage, type reflection, and derivers exist: quotes, splices,
`typeOf`, schema reflection bounded by ordinary export visibility, and
`deriver` declarations that open `deriving` to any class are implemented and
documented in the design and the reference. The standard `Eq`, `Ord`, and
`Show` derivers are ordinary fango in the bundled `Derive` module.

The bundled `Json` module now uses an ordinary fango deriver for `Encode`, and
the persistent Tier-2 Todo CLI consumes it while keeping its decoder
hand-written. That example did not need declaration splices: deriving an
instance is already supported. Generating declaration groups remains deferred
until a consumer forces it; [roadmap-meta.md](roadmap-meta.md) records the
intended named-declaration surface.

It deliberately avoids a `Generic`-style structural representation, which
one-parameter classes without higher kinds cannot express, and avoids Template
Haskell's ambient reification, which is what breaks modularity there.

## REPL hardening

- Add a grouped-input mechanism for multiple top-level function equations;
  today the prompt accepts only one exhaustive equation per input.
- Implement `:load` and `:reload` for complete source files.
- Reconcile values, custom types, constructors, and effects by generation so
  unchanged declarations retain identity while changed generative declarations
  cannot be confused with old values or closures.
- Decide dependency invalidation and whether removed declarations remain
  addressable by existing closures only.
- Connect Ctrl-C to the cleanup and cancellation protocol in
  [effects E9](roadmap-effects.md#e9-structured-async-cancellation-and-repl-integration)
  without corrupting the session or consuming input intended for `readLine`.
  Basic prompt cancellation may ship earlier once its active execution path
  has the corresponding cleanup guarantees.
- Add transcript coverage for load/reload, cross-generation errors,
  cancellation, handler interaction, and recovery after failures.

## Product polish

- Improve diagnostic specificity and source presentation, especially for row
  inclusion.
- Add interactive editing and persistent history to the REPL.
- Expand introductory and task-oriented documentation without duplicating the
  normative reference.
- Make benchmark baselines easier to reproduce and less sensitive to machine
  load while retaining meaningful regression gates. Neither performance gate
  runs unattended today, because neither survives a loaded host: the
  runtime-ratio gate's `mapfilter` case has swung by several multiples on one
  machine depending on whether the rest of the suite is running alongside it,
  so calibrating against a same-host Go baseline is not on its own enough.
  Until that is fixed the gates stay manual, and compile latency additionally
  needs per-host baselines or a host-independent formulation.
- Decide whether the runtime-ratio gate should time work rather than
  processes. It times whole runs, and for the short cases most of the
  baseline's wall clock is process spawn and collection, which both sides pay
  equally — so the published ratios understate how far apart the compute is,
  by a wide margin on the list cases. That makes the gate sound as a
  regression alarm and misleading as a target.

## Calling conventions and recursion shapes

[roadmap-calls.md](roadmap-calls.md) owns two measured, unstarted items: passing
callbacks to worker parameters uncurried, which today costs an allocation per
element at every higher-order call, and compiling list-building recursion to a
loop, which today costs a Go frame per element in `map`, `foldr`, and every
user-written function of the same shape. Both fix the standard library and user
code together, which is why neither is answered by native list combinators —
that document records the measurements ruling that out.

## Tail calls beyond the self-call loop

Self tail calls of top-level workers compile to loops in both backends (see
the design and reference). Recursion that builds a list is not tail recursion
but is tail recursion modulo a constructor; it is owned by
[roadmap-calls.md](roadmap-calls.md). Deliberately deferred, each awaiting a
concrete program that needs it:

- **Mutual recursion** (`f` → `g` → `f`): needs fused dispatch loops or a
  trampoline, changes the emitted shape of several defs at once, and
  cross-module workers live in different Go packages that cannot share a
  loop.
- **Monomorphic local recursive closures**: emitted as declare-then-assign Go
  closures with indirect curried calls — a different transform over captured
  mutable locals with no worker ABI to anchor it. Workaround exists: annotate
  so the local generalizes and lambda lifting hoists it, or write it
  top-level.
- **Capture-excluded workers**: definitions rejected only because a closure
  captures a mutated parameter could be re-enabled by copying mutated params
  into per-iteration locals inside the loop; not worth the extra output shape
  until a real program is excluded.
- A diagnostic (or LSP hint) when a loop-shaped function narrowly misses
  eligibility — e.g. via the capture exclusion — is open tooling territory.

## Effects, state, and resource scopes

[roadmap-effects.md](roadmap-effects.md) owns the detailed milestones, proposed
examples, compiler representations, static checks, and delivery gates. Two
requirements govern that work: effects compile to ordinary calls or explicit
state machines without goroutine-based continuations or stack-copy capture;
resume discipline is enforced at compile time.

The tail-resumptive discipline and parameterized State handlers are proved in
source checking and Core, scoped capture metadata is checked during elaboration
and again in Core, and control-aware Direct/Exit calling conventions are
implemented for workers, callbacks, evidence, ADTs, dictionaries, and both
backends. Abort-only effects, `Result`, and generic cleanup scopes are
implemented: `Scope.bracket` and `Scope.finally` are ordinary function calls
with compiler-supported lifetimes, so resource management needs no new cleanup
syntax. Next come concrete resource APIs and structured IO failures on top of
them. These increments ship without suspension.

The same roadmap owns structured IO failures, resource/native ABI work, and the
open decisions for operation polymorphism and builtin IO handling.
Owned iterators, scoped non-tail resumption, structured async, and cancellation
are later milestones, gated by a concrete consumer and static ownership checks.

## Effect-row subsumption for higher-order arguments

A function taking several callbacks over one shared row variable can only be
applied to arguments whose rows agree, because an argument's type is unified
with the parameter's rather than required to be included in it. Passing a named
worker pins the row; passing an eta-expanded lambda does not, because a
lambda's row is inferred and accumulates inclusion constraints. So today
`bracket open close body` is written with each callback wrapped, or it is
rejected as soon as the body performs something `open` does not.

`Scope.bracket` does not have that problem, because it is a compiler intrinsic
whose saturated application gets a bespoke rule: each callback's effects are
required to be available where the scope runs rather than equal to the scope's
row. Every ordinary higher-order function still has it, and `File.withFile`
and its neighbours will meet it as soon as they exist.

The general fix is effect-row subsumption on function arguments: a callback
performing fewer effects should be usable where more are allowed, which is
already true at run time — elaboration eta-expands and re-tags such callbacks
for the erased-row ABI. Making it true in the checker means using inclusion
rather than unification for an argument's own row, and the open questions are
where that widening is sound to apply, what it does to inference order and
generalization, and how the resulting diagnostics read when a callback really
is wrong.

## Longer-term candidates

These are directions, not commitments or an ordering after the work above.

- Extend the deliberately narrow Go sidecar FFI only from concrete needs:
  richer safe boundary types and richer panic/error translation remain open.
- Add a CLI path for loading and reloading module graphs in `fango repl`; the
  interpreter's native worker already accepts user sidecars, but the current
  REPL still starts from bundled modules only.
- Transparent aliases, including whether aliases can abbreviate effect rows.
- Extend nominal records to inline record payloads on variant constructors
  when an example needs named fields on one alternative; the surface syntax,
  construction, matching, and field visibility remain open together.
- Numeric semantics beyond the current `Int`/`Float` model: overflow, integer
  division, conversions, and possible arbitrary precision.
- Extend type classes only from concrete needs: superclasses, method-local
  polymorphism, higher kinds, and default methods remain deferred. Mutually
  recursive deriving groups and richer precedence-aware display are also open.
  Deriving is user-extensible now, so richer display is a change to the `Show`
  deriver in the bundled `Derive` module rather than to the compiler.
- Allow compiler-bundled instances to implement a method with an inline native
  template, eliminating private forwarders such as `Basics.intAdd` while
  preserving direct `NativeCall` lowering. This needs a stable native identity
  per instance method, an ABI derived from the class-specialized method type,
  load-time agreement with the interpreter registry, and a syntax decision
  between `(+) = native "$1 + $2"` and a parameter-bearing form.
- Broaden the bounded scalar worker specialization only when benchmarks justify
  it; multiple numeric parameters, effectful workers, and custom dictionaries
  currently retain the generic evidence-passing path.
- A canonical formatter for the layout syntax and an LSP for editor support.
- Operator surface beyond declaration and fixity: module-scoped fixity, so two
  libraries could give the same spelling different precedences; sections
  (`(+ 1)`, `(1 +)`); operators bound inside a function body, which today have
  nowhere to put a fixity; qualified infix use (`a Mod.<+> b`); and a way for
  a module to hide a prelude operator so it can declare its own.
- Widen the operator character class if a concrete need appears. `.` is
  excluded because a dot in a name means "module separator" everywhere in
  name resolution, `$` because `$(` opens a splice, and an operator may not
  begin with `--`, which costs `(.)`, `($)`, `(<$>)`, and `(-->)`.
