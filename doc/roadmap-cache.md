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
cache hits. The proportions below are the durable observation; the absolute
times are not.

| Warm-compile phase | Share |
| --- | --- |
| Scanning the JSON envelope to find the payload | ~20% |
| Parsing the payload into the codec's node graph | ~40% |
| Reconstructing typed values from that node graph | ~10% |
| Interning and identity remapping at install | ~5% |
| Everything else, including allocator and GC pressure | remainder |

Two conclusions follow. First, the reflective reference-graph codec is not the
problem: rebuilding the typed graph is a small minority of the work, and the
identity remapping on top of it is smaller still. The problem is that the graph
is spelled as JSON, and a warm compile is therefore a JSON parser benchmark.
Second, the artifacts are far larger than their information content — the
checked objects for this small program run to tens of megabytes across a
handful of modules, at hundreds of bytes per encoded value, because every value
is a JSON object with a `kind` string, every struct field repeats its name, and
fully qualified Go type paths are repeated thousands of times.

Attributing the bytes of a checked object to the `ModuleObject` fields that
reach them puts stage Core at a little under half of every object, shared type
and declaration structure at about a third, runtime Core at well under a fifth,
and the declaration state itself at a few percent.

## Goal and decisions

A warm compile should be dominated by the compiler work that remains, not by
artifact decoding. No change to what is cached, to any key, to any validity
rule, or to any user-visible behavior: this is entirely a change of
representation and of when a representation is materialized.

Three decisions govern the work.

- **The object payload becomes a binary encoding of the same graph.** The
  reference-numbered graph model, its sharing and cycle handling, its exact
  float bits, its sorted maps, and its strictness are all kept as they are. Only
  the spelling changes: tagged values, varints, a string pool, and a node offset
  table, so that decoding walks bytes into typed values without materializing an
  intermediate node representation at all.
- **A node offset table makes partial decoding possible, and stage Core uses
  it.** The encoder emits named sections over one shared node pool. Decoding a
  section touches only the nodes it reaches, so a run that never needs stage
  Core never pays for it.
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

### M2 — Binary object payload

`internal/objectcodec` keeps its exported shape and every validation rule it
enforces today, and replaces its JSON spelling with:

- a header magic and format version;
- a string pool holding every type name, struct field name, string value, and
  span text, so each distinct string is stored and allocated once;
- a node table of offsets into the value area, so any node can be decoded
  without reading the ones before it;
- tagged values — nil, ref, struct, slice, map, string, bool, int, uint, float,
  span, and a type wrapper — with varint lengths and indices, and exact IEEE
  bits for floats.

The decoder becomes a cursor over the payload that produces `reflect.Value`
directly. It keeps the existing per-node value cache and the filling/filled
marks that make cycles and sharing work, and it keeps rejecting unknown types,
malformed references, missing or extra struct fields, wrong value kinds,
duplicate map keys, invalid or unrelocatable spans, and oversized graphs. Two
checks replace the JSON reader's trailing-data rule: every node's encoding must
end exactly where the next node's begins, and the value area must be consumed
exactly.

Encoding stays deterministic: traversal order is unchanged, map keys keep their
current sort, and the string pool is ordered by first encounter.

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
- M2 should leave artifact decoding a minority of warm compile time and shrink
  checked objects severalfold.
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
