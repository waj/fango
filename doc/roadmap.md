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
  them. `IO.readFile`, `IO.writeFile`, and `IO.exit` are legacy shapes kept for
  the todo and csv examples; the `File` module reports failures as values and
  is where new file operations go. Deprecating the legacy trio, and moving the
  examples over, is deferred until there is a deprecation mechanism to do it
  with.
- Strings use valid UTF-8 storage and Unicode-scalar `Char`, indexing, and
  length. Normalization, grapheme segmentation, and Unicode-aware word or case
  operations remain deferred until an example requires them.
- Prefer fango implementations; use declared bundled natives only for semantics
  source code cannot express or when benchmark evidence demands it.
- Keep adding differential, diagnostic, documentation, and performance
  coverage with each library increment.

`Dict` ships with no `filter`, `union`, `intersect`, `partition`, `Ord`
instance, or `Json.Encode` instance; the Markov generator and the Lisp
interpreter are the next consumers likely to force them. `modBy` still has no
floored division to pair with it — `quotientBy` pairs with `remainderBy` —
and Game of Life's grid wrap is the likely forcer. `Tuple` has no
`mapFirst`/`mapSecond`, and no accessors for `Triple`.

`Dict`'s balance invariant has no automated gate. The type is exported
abstractly, so no fango test can observe tree height, and a degenerate tree
would pass every fixture in `testdata/run/stdlib_dict_balance.fango` — those
prove correctness under every insertion order, not balance. The invariant was
checked by hand at the time of writing, by temporarily exposing a `height`
function and asserting the standard bound up to two thousand keys in three
insertion orders and after deletion. A benchmark case is the durable fix: an
unbalanced tree turns an `n log n` case quadratic and blows the ratio ceiling.
That gate would live in `benchmarks/`, which is manual rather than part of
`make ci`.

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
`Meta.infoOf`, and the classes the derivers name — and
native validation cross-checks every bundled `native` declaration against the
interpreter registry, so a stdlib one version away from its binary is an
internal error rather than a behavioral difference. Embedding makes that skew
unrepresentable; anything else has to make it *detectable*, which means a
version stamp and a real diagnostic. The `RESERVED MODULE` rule, which today
rejects a local file named after a bundled module — `Prelude` included — needs
rethinking at the same time: it exists to enforce the same invariant from the
other side.

`Prelude` sharpens the question rather than answering it. The default scope is
now editable in fango, which is most of what a project would want from it, but
only by editing the compiler's own copy: there is no per-project prelude, and
the reserved-name rule is precisely what forbids one. Whether a source root
should let a project supply its own is the same decision as the source-root
bullet below, and should be settled with it rather than separately.

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
- Implement `:reload`, re-reading the modules a session imported after they
  change on disk. `import` already gives the prompt a persistent module graph
  and resolver scope, so `:load` is not needed: a named module is imported,
  and the working directory (or the directory given to `fango repl`) is the
  source root. The intended shape: the graph re-reads every non-bundled node,
  compares content hashes, and re-resolves the changed modules plus their
  reverse dependents in a staging map committed only on success; the checker
  gains a `Retract(owners)` that deletes the canonical-keyed entries of those
  modules (types, classes, effects, constructors, values, workers, methods,
  operations, natives, capture summaries, derivers by owner) and marks their
  instances retracted rather than removing them, because instance limits and
  the compile-time evaluator treat the instance and checked-declaration lists
  as positional prefixes, so retraction must append-and-shadow; the operator
  table is rebuilt from the current nodes plus the prompt's own fixity
  declarations; the prompt's import list is re-applied against the new
  interfaces and names that vanished are reported; old memo cells and
  closures keep their old bindings, as they do under prompt redefinition.
- Decide whether the prompt should be able to see a module's private
  top-level scope, the way GHCi's `:load` puts the prompt inside a module.
  `import` shows only the public interface, which is consistent with every
  other module; debugging a private helper currently means exposing it.
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
- Add transcript coverage for reload, cross-generation errors,
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

## Developer tooling: formatter and editor support

[roadmap-tooling.md](roadmap-tooling.md) owns what is left of two items that
share prerequisites: a formatter for the layout syntax and a language server.
`fango fmt` formats the module header and the import block today and copies
everything below them verbatim, so the remaining formatter work is the printers
for declarations, expressions, and the layout constructs. The language server is
unstarted; its largest prerequisite, a check entry point that accumulates
diagnostics across stages instead of stopping at the first, is worth doing on
its own merits.

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
syntax. The bundled `Fail` effect, typed `IO.Error` values, and the scoped
`File` resource API are built on them, with `File.Handle` a compiler-known
capability. All of this shipped without suspension.

The same roadmap owns the open decisions for operation polymorphism, builtin
IO handling, fallible natives beyond `File`, and a resource escaping through an
outer handler's operation. Owned iterators, scoped non-tail resumption,
structured async, and cancellation are later milestones, gated by a concrete
consumer and static ownership checks.

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

## Constraint simplification for parameterized types

A class constraint on a parameterized type is never reduced to constraints on
its arguments, so `Eq a => List a -> List a -> Bool` is rejected: it asks for
`Eq (List a)` instead, even though `List` derives `Eq` and the instance
`Eq a => Eq (List a)` is exactly the one that would discharge it. Concrete
element types are fine, because the constraint is solved outright; only a
rigid variable under a type constructor hits this. Writing the unreduced
constraint works — `Eq (List a) => …` is accepted — so this is missing
simplification, not a missing instance.

It is not academic. `Dict`'s `Eq` instance would naturally compare
`toList left == toList right`, and doing so would force `Eq (List (k, v))`
into the instance context and from there into every caller of `==` on a
dictionary, exposing that equality happens to go through a list. The module
compares the pairs componentwise instead, needing only `Eq k` and `Eq v`; the
workaround is fine but the constraint it works around is not.

The fix is to apply an instance to a constraint whose head is known even when
its arguments are rigid, during generalization, and to report the residual
constraints in terms of what is left. The open questions are the usual ones
for context reduction: termination when an instance context is no smaller than
its head, how the resulting diagnostics read, and whether the simplified or
the written form should appear in an inferred signature.

## Opaque native types

A `GoAny` value — opaque on the fango side, a real Go value on the native
side — is nearly free in the compiled backend and blocked only by the REPL.

The compiled half is small. `goType` in `internal/codegen/gen.go` maps a
fango type to a Go type, and `GoAny` maps to Go `any`. Core lint's
`matchNativeType` never restricted natives to scalars: it checks only that a
Core node instantiates the declared scheme. The one gate is the scalar type
map in `internal/modules/modules.go`. Providing no `Eq` or `Show` instance is
what keeps the type opaque. `Meta` already passes real Go objects this way,
though only at compile time, where they never reach the backend or the worker.

The REPL is the whole difficulty. Call-form sidecars run in a separate
persistent worker process, and `nativewire.Value` carries five scalars over a
gob wire. A Go pointer cannot cross that, which is the same wall
[E6](roadmap-effects.md#e6-resource-apis-native-boundaries-and-useful-io-errors)
hits for file handles.

`plugin.Open` does not solve it. Go has no unload at all, a plugin must be
built against byte-identical package archives as its host — which a
distributed binary cannot promise — and darwin support is second-class.

The direction to take instead is to **invert the wire**: move the Core
interpreter into the child process alongside the sidecars, leave the compiler
front end in the REPL process, and ship serialized Core across. `GoAny` values
then share one heap and one garbage collector with interpreter values, and the
wire never sees them. The dependency closure makes this tractable —
`internal/eval` pulls in `core`, `types`, `meta`, `ast`, `source`, `natives`,
`stdlib`, and `fangort`, with the type checker staying in the front end.
Loading and unloading a native module becomes respawn-and-replay of the pure
declarations the front end still holds, which the generation model under
[REPL hardening](#repl-hardening) already has to accommodate; unloading is free
because it is a process exit, the one thing Go can do that `plugin` cannot.

Serializing Core is the same artifact
[unembedding](#unembedding-the-bundled-sources) already wants, so the two
converge on one mechanism rather than competing.

The smaller adjacent idea is now implemented: a single-constructor,
single-scalar-field type declared in the sidecar's module is erased to its
scalar across the *existing* boundary, which is how `File.Handle` works and is
available to user sidecars (see the design and reference). It is right for a
file or a connection, which are opened a few at a time and explicitly closed,
and wrong for a persistent `Dict`, where every insert would leak a table slot
that nothing ever releases; `GoAny` remains the answer for that.

## Longer-term candidates

These are directions, not commitments or an ordering after the work above.

- Extend the deliberately narrow Go sidecar FFI only from concrete needs:
  richer safe boundary types and richer panic/error translation remain open.
- Transparent aliases, including whether aliases can abbreviate effect rows.
- Extend nominal records to inline record payloads on variant constructors
  when an example needs named fields on one alternative; the surface syntax,
  construction, matching, and field visibility remain open together.
- Let a constructor take an inferred record literal without parentheses.
  `Wrap { x = 1 }` currently reads as a literal of a record named `Wrap`,
  because a capitalized name before `{` always names the record, and the
  parser cannot know whether the name is a record type or a constructor. The
  diagnostic says so and suggests `Wrap ({ x = 1 })`. Resolution knows both
  namespaces and could rewrite the node, but rewriting an application into a
  literal there is a surprising transform for a pass that otherwise only
  renames; whether the convenience is worth it is the open question, and it
  only becomes pressing alongside inline record payloads above.
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
- Operator surface beyond declaration and fixity: module-scoped fixity, so two
  libraries could give the same spelling different precedences; sections
  (`(+ 1)`, `(1 +)`); operators bound inside a function body, which today have
  nowhere to put a fixity; qualified infix use (`a Mod.<+> b`); and a way for
  a module to hide one prelude name so it can declare its own —
  `{-# no-prelude #-}` is all-or-nothing, which is a blunt instrument for
  wanting a different `(+)`.
- Widen the operator character class if a concrete need appears. `.` is
  excluded because a dot in a name means "module separator" everywhere in
  name resolution, `$` because `$(` opens a splice, and an operator may not
  begin with `--`, which costs `(.)`, `($)`, `(<$>)`, and `(-->)`.
