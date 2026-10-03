# Fango roadmap

Priorities, unfinished work, and open decisions. Follow topic links only as
needed; [design](design.md) and [reference](reference.md) own implemented contracts.
Library growth follows [concrete example programs](roadmap-examples.md).
The sections below do not imply a new global ordering. Stage IDs and
titles follow the [repository milestone rules](../AGENTS.md).

## Current foundation

[Synchronous effects](design/effects.md), [explicit streams](design/tasks.md#library-state-and-traversal),
and [native tasks](design/tasks.md#async-runtime-foundation) replace the
coroutine-driven direction. The retired coroutine design and deferred
capture-flow proposal do not authorize expansion of the old architecture.

Measure immutable cons allocation and task overhead on an idle host using the
retained historical comparisons. Improve private bulk List construction only
when measurements justify it, preserving immutable published nodes. Add task
combinators when concrete applications need them, using the existing closure
invocation boundary. Cancellation-aware socket IO is implemented through
[Net](reference/library-io.md#net).

## JSON and generated-code performance

Use the [typed JSON comparison](design/json-performance.md)
to close the remaining gap with Go after the first tenfold speedup over the
original 10 MB whole-document run (15.159 seconds on its recorded host).
Shared evidence families, product structs, tagged Maybe/Result, compact enums,
and value rows are [implemented](design/backend.md#representations-and-abi).
Derived record decoders pass [separate field arguments](design/json.md) through
their key loop without a local handler activation. State cells synchronize
only once a task inherits them, the text reader decodes ASCII inline, error
paths are a segment stack, keys and separators are consumed without
lookahead, renaming temporaries are elided from emitted Go, and cleanup
scopes over literal callbacks lower at their call sites. Short decoded lists
build directly without a reversal, and bound callbacks for simple local
effect forwarders call the captured operation slot directly. Window reads
decode ASCII without the scalar decoding chain, punctuation and integers branch
on ASCII codes, string spans are a single validating pass, keys match in
place, and file pulls are 64 KiB; current numbers and the measured series are in the
[typed JSON comparison](design/json-performance.md).

[Generated-code performance](roadmap-performance.md) owns the next measured
round: static-argument specialization.

The [buffered pull cursor](design/json.md) shares its text window's buffer
description, defers reader commits to refills and scope exit, and fuses record
keys and typed scalar reads. Array iteration leaves values for their decoders
rather than eagerly constructing tokens. These changes replace the earlier
large-window-in-state experiment; the smaller shared representation is what
makes deferred commits useful.

Measure remaining costs against the [shared cursor and pure value scans](design/json.md).
They include handler snapshot/store traffic, token construction for
custom token and generic value consumers, bounded retries of a split escape,
partial key comparisons before a source-aware key read, and
element callbacks that allocate a closure over a decoding dictionary. Typed
record/list scans now [suspend and resume](design/json.md) across
boundaries, unknown or escaped keys, and custom children without reparsing the
container prefix. Typed scanned strings retain their fragment/escape position.
The decoder class has one resumable parser per type; scalar window phases
continue through refills, and the common driver replaces the duplicate streaming
record/list parsers. Investigate nonmaterializing string validation for
`skipValue` when measurements justify it. Reduce dictionary
closure construction for nonnumeric scalar comparisons;
the string-scanner experiment exposed per-character closure allocation through
generic inequality. Preserve invocation-time handler selection, lexical
shadowing, and gated state synchronization. Parsing and codec derivation remain
in Fango. Tagged value layouts for `Json.Pull.Token` copied more than the allocation
they removed on the recorded fixture; further sum-layout changes remain gated
on measurements of the values flowing through hot operations.

## Builder blocks and generators

[Builder blocks and generators](roadmap-builders.md) propose a module-directed
source lowering to ordinary delayed producer values. The proposal is separate
from the deferred coroutine machinery and does not change current Stream or
handler behavior.

## Standard library expansion

Add APIs when programs need them, preferably in Fango; use bundled natives only
for otherwise unavailable semantics or measured performance needs.

- Dict filter/union/intersect/partition and Ord; floored division to
  pair with modBy; Tuple mapFirst/mapSecond and Triple accessors await consumers.
- Unicode normalization, grapheme segmentation, and Unicode-aware word/case APIs
  remain deferred. Add differential, diagnostic, documentation, and appropriate
  performance coverage with each library increment.
- Dict balance needs a durable gate: abstract exports hide height, and existing
  insertion-order fixtures prove correctness rather than balance. A manual
  n-log-n versus quadratic benchmark is the proposed check.

## HTTP and a concurrent server

[HTTP/1.1 framing and the concurrent server](reference/library-http.md) are
implemented over [buffered readers and writers](reference/library-readers.md),
[Net](reference/library-io.md#net), and [Async](reference/library-async.md).
[HTTP follow-up work](roadmap-io.md) tracks transport acceptance coverage and
future protocol variants. The [HTTP client](reference/library-http-client.md)
is implemented; the [HTTP client roadmap](roadmap-http-client.md) covers
mocking.

## Addressing a specific handler

Scoped activation binding is implemented; see the
[closure contract](reference/effects.md#closures-and-handler-effects). Further
instance APIs remain in the [effects roadmap](roadmap-effects.md#handler-instances-open-questions).

## List representation

[Lists](roadmap-list.md) owns private bulk-allocation experiments and possible
length/indexing APIs over immutable storage.
[Selected saturated callbacks](design/lowering.md#callback-contracts) remove
intermediate currying for known fold lambdas. Unknown curried values retain a
conservative adapter; measure these residual cases before expanding the contract.

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
- Shrink stage Core. A module's stage section is its declarations elaborated a
  second time for the compile-time evaluator, and it is the largest part of
  every object. Changing one module forces it for every module it depends on,
  which `-vv` reports as its own stage. That is a staging question, not a cache
  one.

## Compile-time metaprogramming

[Declaration generation](roadmap-meta.md) waits for a concrete consumer and
must keep public names discoverable without executing generators. Expression
staging and deriving are already [reference contracts](reference/metaprogramming.md).
[Direct quotation blocks](roadmap-meta.md#expression-quotation-blocks) remain deferred.

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
and language-server follow-up work. [Formatter behavior](reference/commands.md#formatting)
and [editor support](reference/commands.md#language-server-and-editor-support) are implemented.

## Test framework

[Testing](roadmap-testing.md) proposes Expect, a row-indexed Test tree, Test.run,
and call-site failure positions. A test command and fuzzing remain deferred.

## Tail calls beyond the self-call loop

Deferred until a consumer needs them:

- Mutual tail calls require fused dispatch/trampolines and a cross-module strategy.
- Capture-excluded self loops could copy changed parameters into per-iteration locals.
- A diagnostic/LSP hint could explain near-miss tail-loop eligibility.

## Deferred analysis proposal

[Synchronous effects](design/effects.md) and [native tasks](design/tasks.md)
are implemented. [Capture-flow optimization](roadmap-compile-latency.md)
retains its stage identities as a deferred historical direction; it does not
mandate reintroducing lifetime analysis. Future concurrency APIs should be
designed against the current [task architecture](design/tasks.md) when needed.

## Directing a type-polymorphic call

DONE

Type witnesses and their use in JSON decoding are specified in the
[JSON reference](reference/library-json.md#type-witnesses). A compile-time
conversion from `Type a` to `Meta.TypeRepr` remains a separate future feature.

## Longer-term candidates

These are directions, not commitments or an ordering:

- A second, native backend. An experimental LLVM backend for macOS ARM64 was
  removed so the language keeps one backend whose compile speed it can rely on;
  its last implementation, with C sidecars and a BDWGC runtime, is at `2de4f1f`.
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
  a [defaulted](reference/classes.md#default-methods) `showList`, overridden
  only by the types that differ from the generic answer, and
  `Show (List a)` would call it.
- Hoisting composed dictionaries. A composed dictionary inside a recursive body
  is rebuilt per call: ANF lifts only non-Direct or polymorphic slots and there
  is no CSE pass. Memoizing per definition and binding at entry is the fix, once
  a consumer shows it matters.
- Superclasses, method-local polymorphism, higher kinds, and mutually
  recursive deriving groups.
- Inline native instance methods: stable method identity, class-specialized ABI,
  registry agreement, and a choice of parameter-bearing or template-only syntax.
- Broader scalar specialization (multiple numeric parameters, effects, custom
  dictionaries) only when measurements justify it.
- Module-scoped fixity, operator sections/local bindings/qualified infix use,
  selective Prelude hiding, and widening operator characters when needed. Dot
  conflicts with qualification, dollar with splices, and leading `--` with comments.
