# Fango roadmap

Priorities, unfinished work, and open decisions. Follow topic links only as
needed; [design](design.md) and [reference](reference.md) own implemented contracts.
Library growth follows [concrete example programs](roadmap-examples.md).
The sections below do not imply a new global ordering. The coroutine and
Async roadmaps name their stages and cross-document dependencies. Stage IDs and
titles follow the [repository milestone rules](../AGENTS.md).

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

## HTTP and a concurrent server

Byte IO is implemented through files and sockets: [byte
sequences](reference/library-bytes.md) owns `Bytes` and its `Source` and
`Sink`, [buffered readers and writers](reference/library-readers.md) owns
`Reader`, `Writer`, and every stage above them, and [IO and
files](reference/library-io.md) owns the file and socket adapters. [HTTP and a
server](roadmap-io.md) owns what is left. A concurrent server additionally depends
on [Async with native readiness](roadmap-async.md#a3-native-readiness-and-io).

## Addressing a specific handler

The typing rule is implemented, including the row-indexed wrapper shape
[`Reader` and `Writer`](reference/library-readers.md) are built from: inside a
handler's subject, a closure performing the handled effect may be adapted to an
arrow that omits it, which binds it to that activation rather than to whichever
handler is innermost when it is called.
[Effects](reference/effects.md#binding-a-closure-to-a-handler-activation) owns
it. The questions it leaves open are with the other deferred effect work, in
the [effects roadmap](roadmap-effects.md#handler-instances-open-questions).

## List representation

[Lists](roadmap-list.md) owns chunk-size/growth experiments, length/indexing
exposure, and chunk-aware native combinators. Callback and recursion costs
belong to [calling conventions](roadmap-calls.md).

## Distributing the bundled sources

The library is now a tree on disk beside the compiler. [Modules and
distribution](roadmap-modules.md#distributing-the-bundled-sources) owns what is
left: precompiled library artifacts, the one uncovered skew case, and a
project-supplied Prelude. Package fetching and independent library versioning
remain deferred.

## Compilation cache

Artifact framing, the binary object encoding, and deferred stage sections are
[implemented](design/backend.md#module-emission-and-build-cache). What remains:

- Prune superseded namespaces. A namespace is now bounded — one artifact per
  module, replaced in place — but each compiler build still selects a cold one
  and nothing removes the previous one but `clean`, so a developer rebuilding
  the compiler accumulates whole namespaces. An entry program's own slots
  outlive the file when it is renamed or deleted, which is the smaller half of
  the same question — and now so do its generated package, its executable, and
  its entry in the build directory's manifest, which also pins the modules only
  it reached. Deciding when a program is gone would answer both, and would say
  whether `clean` should become per-program rather than per-directory.
- Decode artifacts in parallel. Decoding is pure and the objects are
  independent, but coordination cost is real for small graphs. `fango build
  -vv` now measures what decoding costs a build, so the question is whether a
  given graph spends enough in artifact lookup to pay for the concurrency.
- Shrink stage Core. A module's stage section is its declarations elaborated a
  second time for the compile-time evaluator, and it is the largest part of
  every object. Changing one module forces it for every module it depends on,
  which `-vv` reports as its own stage. That is a staging question, not a cache
  one.

## Compile-time metaprogramming

[Declaration generation](roadmap-meta.md) waits for a concrete consumer and
must keep public names discoverable without executing generators. Expression
staging and deriving are already [reference contracts](reference/metaprogramming.md).

## REPL hardening

[Tooling](roadmap-tooling.md#repl-hardening) owns grouped equations, transactional
reload, declaration generations, private-scope access, and history.

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

[Effects](roadmap-effects.md) owns general handler and language extensions.
[Owned coroutines](roadmap-coroutines.md) details the shared suspension API,
implemented dynamic scope ownership and remaining suspending
cleanup, general native retention/transfer contracts, and execution checkpoints.
Preserve checked ownership and ordinary calls/explicit machines; Stream and Async
names do not become compiler primitives.

## Structured Async and executors

[Async](roadmap-async.md) owns library task/context semantics, cooperative
scheduling, native readiness, parallel and mixed worker-pool executors,
and bounded concurrent streams/events. It builds on the coroutine stages.
Initial cancellation uses explicit checkpoints; [CPU responsiveness](roadmap-async.md#a8-cpu-responsiveness)
adds generated polling later. Both concurrent executors require the general
runtime safety gate. Goroutines drive coroutines rather than represent effect
continuations.

## Shared state and transactional memory

[Transactional memory](roadmap-stm.md) owns the proposed answer to shared
mutable state: transactional variables, atomic transactions with an abort-only
`retry`, and the one native boundary they need. The control layer is ordinary
Fango over the implemented handler rules; scheduling dependencies belong to
[Async](roadmap-async.md#implementation-stages), and a general `TVar a` also needs
the [typed opaque-value boundary](roadmap-coroutines.md#c6a-typed-opaque-values).
Scalar STM already needs checked shared-capability and phantom-wrapper contracts;
cooperative scheduling does not remove those prerequisites.

## Capture-flow analysis cost

[Compile latency](roadmap-compile-latency.md) owns the cold-build cost of the
compiler. The capture-flow analysis is most of it: one module is interpreted
several times over the batch pipeline, and every definition is its own root,
so a helper reached from many definitions is interpreted once per root. The
remaining redundant interpretation, at inference, is ordinary optimization;
[sharing work across roots](roadmap-compile-latency.md#cl5-sharing-interpretation-across-roots)
is a redesign, because object identities, owners, and allocation ancestry are
relative to the root being checked.

## Operator fixity scope

[Modules and distribution](roadmap-modules.md#scoping-operator-fixity-to-its-module)
proposes attaching fixity to the operator's own declaration, so a module's
artifact key names its dependencies' contracts rather than the whole program's
operator table. Nothing forces it while only `Basics` declares operators.

## Directing a type-polymorphic call

A call whose type variable appears only in its result — a decoder, a bound, a
class method with a phantom parameter — can be directed today only by
introducing a named binding and annotating it, because annotations are separate
declarations and there is no inline expression annotation. The same gap leaves
`AMBIGUOUS CONSTRAINT` on an undetermined phantom with no remedy but
restructuring the program.

The proposal is a type witness: an indexed, erasable singleton passed as an
ordinary argument.

```fango
decode : Decodable a => Type a -> String -> Result a

decode @Foo input
decode @(List Int) input
```

`Type a` has one inhabitant, so it carries nothing but its index. Unifying
`Type a` with `Type Foo` fixes the variable and instance resolution then runs
unchanged, which is why this needs no new judgment and no kinds — the index is
an ordinary fully applied type. Both backends represent the witness as Unit and
neither drops the parameter, so arities agree.

`Type a` is runtime-legal and has no eliminators. `TypeRepr` stays
compile-time-only and inspectable, and a stage-only `Meta.repr : Type a ->
TypeRepr` is the single door between them, so the compile-time-only rule keeps
its current roots and the boundary remains "can you look inside it".

Open decisions:

- The surface costs a lexer carve-out. `@` is an operator character, so it
  would begin a witness only when immediately followed by an uppercase letter
  or `(`, leaving `@@` and spaced `x @ y` alone while forcing `x @ Foo` for a
  user-defined `@` with a constructor argument. Fango already distinguishes
  `foo()` from `foo ()` and already bans a run beginning `--`, so this is the
  same kind of rule. The spellings that need no lexer change are the existing
  `typeOf` keyword and `(type Foo)`.
- A bare `parse Foo` is rejected. Constructors have their own namespace and
  `type Code = Code` is idiomatic, so a bare name would need type-directed
  resolution against resolution-before-inference, and it cannot spell an
  applied type.
- Visible type application on undeclared parameters is not the goal. It would
  require publishing an order for quantified variables that inferred
  definitions do not have, making variable order in an annotation a breaking
  change.
- No forcing consumer exists. JSON decoding, which remains application Fango
  code, is the candidate.

Delivery would change surface syntax, so it must update the TextMate grammar
and tokenize fixtures that mix a witness with a user-defined `@` operator.

## Longer-term candidates

These are directions, not commitments or an ordering:

- Richer safe sidecar types and panic/error translation, driven by concrete APIs.
- Transparent aliases, including effect-row aliases.
- Inline record variant payloads: settle construction, matching, and visibility.
  Also decide whether resolution should disambiguate `Wrap { x = 1 }` as a
  constructor call; today inferred record arguments require parentheses.
- Numeric semantics beyond Int/Float: overflow, division, conversions, arbitrary precision.
- Behaviour for a whole constructed type, such as displaying `List Char` as a
  string. A second instance head specializing a constructor's arguments is
  rejected, so the composition-consistent form is a hook method on the
  element's class that the constructed instance consults: `Show a` would gain
  a `showList`, and `Show (List a)` would call it. That works already when
  every instance writes the hook; what it waits on is default methods, so only
  the types that differ from the generic answer declare one, and derivers that
  supply it.
- Hoisting composed dictionaries. A composed dictionary inside a recursive body
  is rebuilt per call: ANF lifts only non-Direct or polymorphic slots and there
  is no CSE pass. Memoizing per definition and binding at entry is the fix, once
  a consumer shows it matters.
- Superclasses, method-local polymorphism, higher kinds, default methods, mutually
  recursive deriving groups, and richer Show-deriver precedence/display.
- Inline native instance methods: stable method identity, class-specialized ABI,
  registry agreement, and a choice of parameter-bearing or template-only syntax.
- Broader scalar specialization (multiple numeric parameters, effects, custom
  dictionaries) only when measurements justify it.
- Module-scoped fixity, operator sections/local bindings/qualified infix use,
  selective Prelude hiding, and widening operator characters when needed. Dot
  conflicts with qualification, dollar with splices, and leading `--` with comments.
