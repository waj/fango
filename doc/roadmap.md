# Fango roadmap

Priorities, unfinished work, and open decisions. Follow topic links only as
needed; [design](design.md) and [reference](reference.md) own implemented contracts.
Library growth follows [concrete example programs](roadmap-examples.md).
The sections below do not imply a new global ordering; the effects roadmap
has its own committed dependency sequence.

## Standard library expansion

Add APIs when programs need them, preferably in Fango; use bundled natives only
for otherwise unavailable semantics or measured performance needs.

- New file operations belong in File. Deprecating legacy IO.readFile/writeFile/exit
  and migrating examples waits for a deprecation mechanism.
- Dict filter/union/intersect/partition, Ord, and Json.Encode; floored division to
  pair with modBy; Tuple mapFirst/mapSecond and Triple accessors await consumers.
- Unicode normalization, grapheme segmentation, and Unicode-aware word/case APIs
  remain deferred. Add differential, diagnostic, documentation, and appropriate
  performance coverage with each library increment.
- Dict balance needs a durable gate: abstract exports hide height, and existing
  insertion-order fixtures prove correctness rather than balance. A manual
  n-log-n versus quadratic benchmark is the proposed check.

## List representation

[Lists](roadmap-list.md) owns chunk-size/growth experiments, length/indexing
exposure, and chunk-aware native combinators. Callback and recursion costs
belong to [calling conventions](roadmap-calls.md).

## Unembedding the bundled sources

[Modules and distribution](roadmap-modules.md#unembedding-the-bundled-sources)
owns disk versus precompiled sources, source-root configuration, version/skew
checks, per-project Prelude, and runtime-source materialization. Package
fetching and independent library versioning remain deferred.

## Compilation cache decoding cost

[Compilation cache](roadmap-cache.md) owns the artifact framing, the binary
object payload, and deferred stage sections. Warm compilation currently spends
nearly all its time parsing cache artifacts rather than compiling; what is
cached, and every validity rule, stays as it is.

## Compile-time metaprogramming

[Declaration generation](roadmap-meta.md) waits for a concrete consumer and
must keep public names discoverable without executing generators. Expression
staging and deriving are already [reference contracts](reference/metaprogramming.md).

## REPL hardening

[Tooling](roadmap-tooling.md#repl-hardening) owns grouped equations, transactional
reload, declaration generations, private-scope access, cancellation, and history.

## Product polish

- Improve diagnostic specificity/source presentation, particularly row inclusion;
  add introductory and task-oriented docs without repeating the reference.
- Make performance baselines reproducible under host variation/load before making
  timing gates unattended. Same-host ratios still move with contention.
- Record pure and Fail.attempt capture-diamond cold/warm baselines on an idle
  machine with `go test ./benchmarks -update-baselines`.
- Decide whether runtime gates should time computation instead of whole processes;
  spawn/collection currently understates short workloads' compute differences.

## Developer tooling: formatter and editor support

[Tooling](roadmap-tooling.md) owns remaining comment anchors/layout assertions
and the unstarted language server, beginning with a reusable diagnostic-accumulating
check path. [Formatter behavior](reference/commands.md#formatting) is implemented.

## Test framework

[Testing](roadmap-testing.md) proposes Expect, a row-indexed Test tree, Test.run,
and call-site failure positions. A test command and fuzzing remain deferred.

## Calling conventions and recursion shapes

[Calls](roadmap-calls.md) owns uncurried worker callback parameters and loops for
list-building recursion, with measurements and acceptance gates for both.

## Tail calls beyond the self-call loop

Deferred until a consumer needs them:

- Mutual tail calls require fused dispatch/trampolines and a cross-module strategy.
- Monomorphic local recursive closures need a captured-local transform; annotating
  for generalization/lifting or moving the function top-level can avoid this case.
- Capture-excluded self loops could copy changed parameters into per-iteration locals.
- A diagnostic/LSP hint could explain near-miss tail-loop eligibility.

## Effects, state, and resource scopes

[Effects](roadmap-effects.md) owns the committed sequence: cooperative structured
async; suspending cleanup; bounded concurrent streams/events; a bounded parallel
executor; measured optimization; written capture/borrowing contracts. Preserve
compiler-proved resume discipline and ordinary calls/explicit machines, without
host-stack copying or goroutine-based continuations.

## Constraint simplification for parameterized types

Known-head constraints containing rigid arguments remain whole predicates:
`Eq a` does not discharge `Eq (List a)`. Consider context reduction during
generalization, preserving specialization and caller-supplied evidence. Decide
termination for non-decreasing contexts, diagnostics, and displayed inferred
signatures. Dict currently compares entries componentwise to avoid exposing a
List-based equality constraint to callers.

## Opaque native types

[Modules and native values](roadmap-modules.md#opaque-native-types) proposes
co-locating the interpreter with sidecars so opaque Go objects share its heap;
this depends on serialized Core and REPL replay/generation decisions.

## Longer-term candidates

These are directions, not commitments or an ordering:

- Richer safe sidecar types and panic/error translation, driven by concrete APIs.
- Transparent aliases, including effect-row aliases.
- Inline record variant payloads: settle construction, matching, and visibility.
  Also decide whether resolution should disambiguate `Wrap { x = 1 }` as a
  constructor call; today inferred record arguments require parentheses.
- Numeric semantics beyond Int/Float: overflow, division, conversions, arbitrary precision.
- Superclasses, method-local polymorphism, higher kinds, default methods, mutually
  recursive deriving groups, and richer Show-deriver precedence/display.
- Inline native instance methods: stable method identity, class-specialized ABI,
  registry agreement, and a choice of parameter-bearing or template-only syntax.
- Broader scalar specialization (multiple numeric parameters, effects, custom
  dictionaries) only when measurements justify it.
- Module-scoped fixity, operator sections/local bindings/qualified infix use,
  selective Prelude hiding, and widening operator characters when needed. Dot
  conflicts with qualification, dollar with splices, and leading `--` with comments.
