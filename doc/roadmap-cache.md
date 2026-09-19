# Roadmap: compilation cache artifact decoding

The persistent per-module cache works: a warm compile reuses every checked
module object and every emitted Go package. What it does not yet do is make
reuse cheap. Nearly all of a warm compile is now spent turning cache bytes
back into compiler data structures, and the format — not the reconstruction —
is what costs. This document owns the artifact framing, the object payload
encoding, and stage-section laziness. Implemented cache behavior belongs to
[pipeline](design/pipeline.md#pipeline) and
[backend](design/backend.md#module-emission-and-build-cache).

[Roadmap index](roadmap.md). Related work:
[metaprogramming](design/metaprogramming.md),
[language server](roadmap-tooling.md#language-server), and
[REPL hardening](roadmap-tooling.md#repl-hardening).

## Measured baseline

Measured on one machine with `examples/calculator.fango`, whose graph is the
Prelude plus a handful of stdlib modules. A warm `fango check` and a warm
`fango build` take the same time to within noise, so Go emission, the build
directory, and the Go toolchain contribute almost nothing once the emission
cache hits. The proportions are the durable observation; absolute times are not.

Framing the artifacts and replacing the object payload's JSON with a binary
encoding together took a warm compile to about a quarter of what it was and the
checked objects to about a fourteenth of their size. Decoding is no longer the
whole of a warm compile, but it is still most of the compiler-owned part of it.

Attributing the bytes of a checked object to the `ModuleObject` fields that
reach them puts stage Core at a little under half of every object, shared type
and declaration structure at about a third, runtime Core at well under a fifth,
and the declaration state itself at a few percent. Stage Core is therefore the
largest remaining input, and a compile that never evaluates a splice never
needs it.

## Goal and decisions

A warm compile should be dominated by the compiler work that remains, not by
artifact decoding. No change to what is cached, to any key, to any validity
rule, or to any user-visible behavior: this is entirely a change of
representation and of when a representation is materialized.

Two decisions govern the work.

- **The object encoding's node table makes partial decoding possible, and stage
  Core uses it.** The encoder emits named sections over one shared node pool.
  Decoding a section touches only the nodes it reaches, so a run that never
  needs stage Core never pays for it.
- **Stage Core is needed exactly when some module is checked from source.**
  Completing a module's stage snapshot elaborates its declarations against the
  installed stage definitions of its dependencies, so a single cache miss
  anywhere in the graph needs every dependency's stage Core, whether or not the
  missed module contains a splice. There is no useful finer-grained rule, and
  attempting one would put cache decisions inside the staging evaluator's index
  arithmetic. Deferral is therefore all-or-nothing per session and is forced at
  the first source check.

No new commands, flags, eviction policy, or on-disk layout beyond the artifact
bytes themselves. Cache failures stay indistinguishable from misses.

## Milestones

### M3 — Deferred stage sections

A checked module object is encoded as two sections over one node pool: the
object without its stage Core, and the stage Core with its completion groups.
Shared structure is stored once and decoded once regardless of which sections
are read.

Installation defers the stage section. `InstallObject` installs declaration
state, resolver, runtime Core, and templates as it does today, and registers
with the staging session a thunk holding the object's decoder and the remapper
that installed the rest of it. Reusing both is what makes deferral sound: the
same decoder returns the same pointers for structure shared with the runtime
half, and the same remapper maps those pointers to the copies already
installed, so a deferred stage section cannot acquire identities that disagree
with the module that was installed from it.

The staging session forces every pending thunk, in installation order, at three
seams, each chosen because no index into the evaluator's definition list is
live across it: beginning a module, completing a stage snapshot, and running a
splice. A batch run whose modules all hit never reaches any of them. A run with
any miss forces at that module's first seam, which restores exactly today's
behavior. The REPL reaches the splice seam without the others, which is why
that seam exists.

Test instrumentation gains a stage-section load event alongside the existing
artifact hit and miss events, so a test can assert that an all-hit compile
loaded no stage Core and that a compile with a miss loaded it.

## Acceptance gates

- The existing gates are preserved unchanged: Core lint, the
  interpreter/compiler differential suite, functional tests, `go vet`, and the
  goldens. No golden is expected to move; if one does, the change is not
  representation-only.
- Cache correctness tests keep their current shape: a damaged, truncated,
  foreign-schema, or unwritable artifact is a miss and never a diagnostic, and
  every stale-input case still rebuilds. The framing and the binary payload each
  need their own corruption cases.
  M3 must load no stage section on an all-hit compile and must not change the
  work done by a compile with a miss.
- Compile-latency measurement stays off the ordinary development path. The
  timing above was taken deliberately on an idle machine and is not a gate.

Remove completed milestones instead of retaining an implementation diary; their
durable contracts belong in the design topics.

## Out of scope

- Parallel artifact decoding. Decoding is pure and the objects are independent,
  so it would compose with this work, but coordination cost is real for small
  graphs and the representation fix is the larger and simpler win.
- Pruning superseded compiler-fingerprint namespaces. Each compiler build
  selects a cold namespace and nothing removes the previous one but `clean`,
  so a developer rebuilding the compiler accumulates them. Worth fixing;
  unrelated to decoding cost.
- Any change to what stage Core contains. A module's stage section is currently
  its declarations elaborated a second time for the compile-time evaluator, and
  it is the largest part of every object. Making it smaller is a staging
  question, not a cache one.
