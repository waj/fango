# Hidden-state lowering experiments

[Design index](../design.md) · [Verification](verification.md) · [Remaining compiler work](../roadmap-hidden-types.md)

The adapter harness tests proposed hidden-state lowerings around the actual
compiled `Stream.fold`. Its hand-written Go adapters are separate from the
compiler-generated hidden-type controls below.
Run its correctness, allocation-growth, concurrent traversal, race, and
`checkptr` checks without timing:

```sh
python3 benchmarks/existentialcompare/compare.py --out /tmp/fango-storage-check --prepare-only
```

On an otherwise idle machine, use a different, new output directory and replace
`--prepare-only` with `--idle`. Run Python from the Nix development environment.
The default experiment compares concrete state with checked `any` storage containing a
typed pointer and with `unsafe.Pointer` storage recovered as its original type.
Both erased variants own one private state slot per traversal; the source and
published iterator values are never mutated. Their adapters call the original
Fango step, so the measurement includes dispatch that a type assertion alone
cannot remove. Nil type descriptors in this harness are valid only for these
nonreflecting, class-free state paths.

Cases include scalar, pair and List state, four maps, a rejecting filter,
take, zip, and a runtime choice between different concrete states. Runtime step choice and
construction-plus-consumption are measured separately from consuming prepared
sources. All variants use the same generated fold and independently checked
checksums. Allocation checks reject growth beyond the explicit baseline;
the current generic `take` worker allocates in both representations.
Twenty alternating process samples produce paired bootstrap ratio
intervals; an inconclusive interval extends to sixty. Passing requires the
upper 95% bound to be at most 1.05 for every selected case. Sources, hashes,
generated Go, binaries, command logs, raw observations, and summaries remain
in the output directory. The host's idle state is asserted by the operator.

Passing this experiment establishes only these storage cases, not the full
hidden-type feature's performance gate. Published immutable iterator positions,
effects, transformations after dynamic package selection, cross-module
specialization, and the JSON workload still require their own migration
measurements.

## Per-step erasure

The 2026-10-12 run on darwin/arm64, Go 1.26.7, GOMAXPROCS=1 and GOGC=100
rejected both adapters against the annotated explicit-state fold. Across six
workloads, three sizes (16, 10,000 and 100,000), and both consumption modes,
each adapter passed four cases, failed thirty, and remained inconclusive on
two after sixty samples. The run retained 2,400 process observations under
`/private/tmp/fango-existential-measured-20261012`, including source snapshots,
hashes, raw observations, and paired confidence intervals.

For consumption of prepared 100,000-element sources, median paired elapsed-time
ratios against explicit state were:

| Workload | Checked pointer | Unsafe pointer |
| --- | ---: | ---: |
| Scalar state | 2.231× | 1.624× |
| Pair state | 2.049× | 1.555× |
| List state | 2.409× | 1.881× |
| Four maps | 1.186× | 1.120× |
| Rejecting filter | 0.999× | 0.999× |
| Runtime scalar/pair choice | 2.042× | 1.573× |

Both adapters added two allocations per scalar traversal (56 bytes), with no
per-element allocation growth. Correctness, concurrent traversal, race and
pointer checks passed. Unsafe recovery reduced elapsed time but did not remove
the adapter's extra dispatch and state traffic. The rejecting filter scans
inside the original step and crosses the adapter only when returning exhaustion;
its parity does not establish parity for streams that yield elements. These
results reject the measured adapters, not existential types in general; neither
adapter is suitable for the proposed library migration under the 5% limit.

## Typed consumers

The `opened` prototype keeps the concrete `Stream[S, Int]` record, its original
state field and its original step. A Go interface selects a worker specialized
for that record's hidden state type once per consumer call. The worker calls
the same generated Fango fold as the explicit baseline; its loop performs no
state conversion, wrapper step, or mutable-slot operation. This prototype uses
no unsafe conversions.

```sh
python3 benchmarks/existentialcompare/compare.py \
  --out /tmp/fango-opening-check --prepare-only --variants explicit,opened \
  --cases scalar,pair,list,mapped,rejected,choice,taken,zipped,cursor
```

For timing, replace `--prepare-only` with `--idle --sample-ms 200`. The interval
and per-case threshold are unchanged; the longer target reduces timing noise.
The original explicit, checked and unsafe comparison remains available through
`--variants explicit,checked,unsafe` (without the `cursor` case).

Both the explicit closure and the interface package cross an opaque construction
boundary. Each measured traversal invokes one consumer selected indirectly, and
both consume the original typed stream. Factory costs are included in
`construct` mode. The explicit baseline already represents its runtime choice
by selecting a typed consuming closure; this is the control for dynamic dispatch,
not a claim that every existing concrete construction allocates a closure.
Where a compiler can eliminate a package, it should preserve the original
concrete representation without either wrapper.

The same package also supports a caller-supplied combining callback and seed,
a consumer with a different return shape, a typed iterator traversal, and an
Exit consumer. Tests check invocation evidence on repeated traversals, exact
abort propagation from both step and consumer, and retained immutable positions
across collection. Exporting a new opaque position currently boxes it; only
the iterator traversal inside a specialized consumer is timed. `take` and `zip`
in this experiment construct their typed streams before packaging.

The interface is a hand-instantiated, finite table of opening sites in the
experiment. It is not the proposed Fango Stream API. Discovering arbitrary
opening sites, specializing their public argument/result types, handling
recursive package construction, and generating these workers across modules
are still [compiler work](../roadmap-hidden-types.md). No method introduces its
own type parameters: every consumer signature in this table is fixed. Passing
this benchmark does not supply the missing specialization analysis.

On 2026-10-12, Go 1.26.7 on darwin/arm64 with GOMAXPROCS=1 and GOGC=100,
the expanded comparison passed all 54 cases: nine workloads, three sizes
(16, 10,000 and 100,000), and both construction and consumption modes. Each
case resolved after twenty alternating pairs with a 200 ms baseline target,
retaining 2,160 process observations in
`/private/tmp/fango-existential-opening-measured-v2`. Median ratios ranged from
0.944× to 1.022×; the largest upper 95% bound was 1.043×, below the unchanged
1.05× limit. The preliminary 40 ms run retained in
`/private/tmp/fango-existential-opening-measured-v1` passed 35 of its 36 cases
and left the 10,000-element List consumption case inconclusive after sixty
pairs; it did not pass its gate.

For prepared 100,000-element sources in the expanded run:

| Workload | Typed consumer / explicit time |
| --- | ---: |
| Scalar state | 1.004× |
| Pair state | 1.007× |
| List state | 0.994× |
| Four maps | 0.998× |
| Rejecting filter | 0.999× |
| Runtime scalar/pair choice | 1.004× |
| Take | 1.007× |
| Zip | 1.002× |
| Typed iterator traversal | 0.999× |

The prototype added no traversal allocations. Both versions of the scalar
construction benchmark allocated once per constructed source: 48 bytes for
the explicit consuming closure and 24 bytes for the interface's typed record.
The generic `take` worker retained its existing allocations in both versions;
this experiment does not optimize them. Correctness, retained aliases,
invocation evidence, exit propagation, race, and pointer checks all passed.
This establishes feasibility for the measured consumer specialization, not
performance or soundness of a completed hidden-type compiler feature.

## Compiler-generated controls

The separate compiler harness compiles ordinary Fango hidden declarations and
consumers, without replacing their implementation with hand-written Go:

```sh
python3 benchmarks/existentialcompare/compiler_compare.py \
  --out /tmp/fango-hidden-compiler-check --prepare-only
```

Replace `--prepare-only` with `--idle` for timing. The controls compare immediate
construction/opening, calls through a small factory and consumer, and a runtime
scalar/pair choice against equivalent explicit-state loops. They check empty,
single-element, and long traversals for allocation growth, and repeated
repacking for immutable aliases and zero additional allocations. Race and
`checkptr` checks precede timing. The harness preserves a complete source
snapshot, generated Go, command logs, binaries, hashes, and paired observations.
It uses the same 5% upper-confidence-bound gate as the adapter harness.

The 2026-10-12 run on Apple M5 Max, Go 1.26.7, GOMAXPROCS=1 and GOGC=100
retained its artifacts in `/private/tmp/fango-hidden-compiler-gate-20261012`:

| Actual compiler control | Median ratio | 95% interval | Pairs | Gate |
| --- | ---: | --- | ---: | --- |
| Immediate known package | 1.003× | 0.931–1.089× | 60 | Inconclusive |
| Factory and consumer calls | 1.015× | 0.906–1.063× | 60 | Inconclusive |
| Runtime scalar/pair choice | 0.958× | 0.950–0.982× | 20 | Pass |

All controls allocate zero per traversal. Correctness, allocation slope,
repacking, race, and pointer checks passed. The two inconclusive cases do not
pass the timing gate; a median below 1.05 is insufficient on its own.

The repository runtime-ratio checks passed on this run. Its compile-latency
gate did not: warm-unchanged `hello` measured 53 ms against the recorded 14 ms,
and both capture baselines were missing. A clean `HEAD` snapshot on the same
host also failed, measuring 56 ms for that case and reporting the same missing
baselines. Those failures therefore do not isolate a regression from this
change; neither baseline values nor thresholds were updated.

These early controls exercise bounded package elimination. The library
migration has a separate compiler-output matrix below.


## Library migration

`migration_compare.py` compares two complete source snapshots using the same
compiler. The explicit snapshot restores the retained state-parameter versions
of Stream, Iterator, and the workload annotations; the candidate uses the hidden
state library. Both sides call generated factories and generic consumers, with
no hand-written state erasure or unsafe adapter. The runtime scalar/pair choice
is Fango code: the explicit control branches before constructing its consuming
closure, while the hidden source returns one Stream type before consumption.

```sh
python3 benchmarks/existentialcompare/migration_compare.py \
  --out /tmp/fango-hidden-migration --prepare-only
```

Use `--resume --idle` with the same output directory to measure its prepared
binaries. The matrix includes scalar, pair and List state, four maps, rejecting
filter, take, zip, recursive iterator consumption, runtime witness choices, and
retained immutable positions. Each runs at 16, 10,000 and 100,000 elements, with
prepared consumption and construction included separately: 60 cases. Alternating
pairs use the unchanged 1.05 upper-confidence-bound gate. Allocation comparisons
precede timing; pre-existing allocations, such as take's callback adaptation,
are compared with the explicit baseline instead of being attributed to hiding.
Checksums, repeated traversal, both choice branches, race, and checkptr are also
checked. Source snapshots, hashes, generated projects and raw observations are
retained together.

The matrix covers typed storage propagation through reusable workers and bounded
finite choices. It does not claim parity for the general boxed fallback used
when storage cannot be propagated, including recursively growing shapes.

The 2026-10-12 compiler migration run on Apple M5 Max with Go 1.26.7,
GOMAXPROCS=1 and GOGC=100 passed all 60 cases, retaining 27 alternating pairs
per case (3,240 observations) in `/private/tmp/fango-migration-gate5-20261012`.
Median ratios ranged from 0.794× to 1.012×; the largest upper 95% bound was
1.048×, below the unchanged 1.05× limit. All 160 allocation comparisons passed,
including construction, both runtime-choice branches, and inputs of 0, 1, 16,
and 10,000 elements. Generated Go from the final compiler was byte-for-byte
identical to the prepared hidden benchmark project.

For prepared 100,000-element sources:

| Workload | Hidden / explicit time | Added allocations |
| --- | ---: | ---: |
| Scalar state | 0.994× | 0 |
| Pair state | 0.998× | 0 |
| List state | 0.963× | 0 |
| Four maps | 0.979× | 0 |
| Rejecting filter | 1.002× | 0 |
| Take | 0.998× | 0 |
| Zip | 0.987× | 0 |
| Iterator traversal | 0.916× | 0 |
| Runtime scalar/pair choice | 1.012× | 0 |
| Retained iterator positions | 0.953× | 0 |

Consumption allocates zero in every case except take, whose existing
per-element callback adaptation remains equal on both sides. Closed callback
hoisting is necessary for construction parity: callbacks introduced by factory
inlining must not capture an enclosing Go generic dictionary when their runtime
types and values are concrete. The ordinary
[callback lowering](lowering.md#callback-contracts) performs that check.

The final migration build also passed the repository's runtime-ratio tests.
`make test-perf` still reports the pre-existing compile-latency failures: warm
unchanged hello is 53 ms against 14 ms, and the capture baselines are absent.
The clean-HEAD comparison above reproduces those failures. No baseline or
threshold was changed.
