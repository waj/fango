# Typed JSON comparison baselines

The [current harness and parser measurements](json-performance.md) own the
active comparison and measurement contract. These recorded baselines retain
their original versions, inputs, and controls.

## ASCII fast paths and single-pass spans

Five changes after the single parser were measured one at a time against the
previous commit's binaries: alternating fresh-process pairs on the
10,000,260-byte declaration-order fixture, one warmup per binary, default
runtime settings, matching checksums, and no builds or tests during samples.
Medians, with pair counts of 11 unless noted:

| Change | Go backend | LLVM backend |
| --- | ---: | ---: |
| `matchWindow` compares UTF-8 keys in place (`Encoding.matchAt`) | 145.8 → 142.2 ms | 84.4 → 84.3 ms |
| `readWindow` decodes one-byte ASCII itself | 143.2 → 115.7 ms | 84.7 → 67.0 ms |
| punctuation and integer digits branch on `asciiAt` codes | 114.4 → 102.5 ms | 66.3 → 65.3 ms |
| string spans use a precomputed `AsciiSet` and one validating pass | 103.2 → 97.4 ms | 64.9 → 66.4 ms (21 pairs) |
| 64 KiB file pulls; the C sidecar reads large requests directly | 97.9 → 96.8 ms | 67.1 → 60.1 ms |

Fifteen final pairs measure 148.3 → 98.1 ms for Go (33.9% less time) and
86.2 → 60.5 ms for LLVM (29.9%). The plain Go control takes 68.9 ms in the
same session, so Go is about 1.4× the control and LLVM about 12% faster.

The `readWindow` gain on both backends came from removing the
`peekWindow → decodeScalar → byteAt → fromCode` chain: Go's inliner rejects
each function, and the nested result values moved through memory. An
`asciiAt` whitespace loop saved a further ~7% on Go but cost LLVM 11%.
Clang then stopped inlining `fastWhite` at its call sites; re-optimizing the
same bitcode with `-inline-threshold=600` recovered the earlier LLVM time,
so the loss is an inlining decision that depends on the size of the
generated record and sum code.

Removing the tag from LLVM single-constructor products, and the unreachable
panic default from exhaustive switches, then took LLVM from 59.7 to 57.7 ms
(11 pairs). With that change the ASCII whitespace loop helps both backends:
Go 95.9 → 89.3 ms and LLVM 57.5 → 53.4 ms (11 pairs). The span
change costs LLVM one more call per string, where the predicate loop was
already inlined. Before the chunk change the C file handle answered at most
4096 bytes per pull whatever the request.

Seven measured runs of the same 10,000,260-byte fixture, following one warmup
per implementation, compared GHC 9.10.3/Aeson 2.2.4.1 (`-O2`, default RTS) with
Go 1.26.7 and both Fango backends using [pure buffered value scans](json.md)
on the macOS ARM64 host. All checksums matched; generation, compilation,
builds, and correctness tests were
outside the timed samples.

| Implementation | Median whole-process wall time | Median peak RSS |
| --- | ---: | ---: |
| Plain Go | 0.0656 s | 50.4 MB |
| Haskell/Aeson, generic instances | 0.1343 s | 113.2 MB |
| Fango, LLVM backend | 0.1008 s | 45.5 MB |
| Fango, Go backend | 0.1662 s | 27.3 MB |

Aeson takes about 2.0× the Go control's time; Fango LLVM takes about 25% less
time than Aeson, while Fango Go takes about 1.2× Aeson's time. The fixture's
required record fields arrive in declaration order, exercising generated
straight-line scanning and bulk encoded key matches. These results precede
pure scan continuation for reordered keys and do not establish its timing.
Peak RSS is whole-process residency, not retained decoded heap. The Aeson
input is read into a strict ByteString,
whereas the other implementations read through buffered readers. The result
applies to this typed workload and these default runtime settings.

Five additional rotated paired runs compare the final binaries against saved
shared-window-cursor binaries on that same fixture, after one warmup each.
Fango Go changes from 0.2661 s to 0.1662 s (37.6% less time), and LLVM from
0.1678 s to 0.1014 s (39.6% less time). All checksums match; builds and tests
are excluded from the samples. The change combines pure buffered record/list
decoding, direct integer accumulation, and encoded key matching, rather than
isolating their individual contributions.

Seven rotated fresh-process runs of that same fixture compare the tuple-based
streaming record decoder at `5ad6073` with the generated
[field-argument key loop](json.md). Both streaming variants bypass every record
and list scan attempt; buffered scalar reads remain enabled. They use the same
current compiler and remaining library, restoring only the earlier Json source
for the tuple variant. Compilation, library snapshot construction, and correctness
tests precede the samples, with one warmup per binary. All checksums match.

| Backend | Tuple slots, Scan disabled | Field arguments, Scan disabled | Field arguments, Scan enabled |
| --- | ---: | ---: | ---: |
| Go | 0.2547 s | 0.2212 s | 0.1579 s |
| LLVM | 0.1581 s | 0.1316 s | 0.0968 s |

Field arguments reduce streaming time by 13.2% on Go and 16.8% on LLVM.
The same rotated runs measure plain Go at 0.0661 s and Aeson at 0.1251 s.
Streaming LLVM takes about 5% more time than Aeson's median; keeping Scan
enabled reduces LLVM time by a further 26.4% and Go time by 28.6%. The fixture
still has fields in declaration order, so these comparisons measure forced
streaming of the same data, not the timing of alternative object layouts.

The [pure field-argument scan loop](json.md) is measured against the preceding
ordered-only scanner with the field-argument streaming decoder unchanged.
Seven rotated paired fresh-process runs, following one warmup per binary and
layout, compare the original 10,000,260-byte fixture with `-field-order reverse`.
Reversal applies to every object, including nested records; values, byte count,
and expected checksums remain identical. The reversed fixture's SHA-256 is
`be2044cbff343b735009d36339b623f024ea05aa67e839be093f087d76f2e045`.
Both variants use the same compiler and runtime settings. All checksums match;
compilation and correctness suites finish before the measurements.

| Backend | Field order | Ordered-only Scan | Scan with continuation |
| --- | --- | ---: | ---: |
| Go | Declaration | 0.1586 s | 0.1589 s |
| LLVM | Declaration | 0.0962 s | 0.0949 s |
| Go | Reversed | 0.2291 s | 0.2009 s |
| LLVM | Reversed | 0.1305 s | 0.1063 s |

Continuation reduces reversed-key time by 12.3% on Go and 18.5% on LLVM.
Declaration-order timings remain within the observed run variation. On the
reversed fixture, the same runs measure plain Go at 0.0674 s and Aeson at
0.1262 s; LLVM takes about 16% less time than Aeson. This comparison measures
fully reversed keys, rather than the timing of every supported permutation.

The earlier dual-method resumable scanner keeps completed record/list prefixes
across refills and creates diagnostic paths only while suspended work executes.
Fifteen paired fresh-process runs per backend and layout, after one warmup,
compare saved `c3009c7` binaries with the resumable scanner and its supporting
compiler fixes. They use the same fixtures, default runtime settings, and
verified checksums as above. Case order rotates, and each case alternates
baseline-first and resumable-first execution. These measurements repeat the
comparison after the earlier host load subsided, using the same prebuilt
binaries. No builds or tests run during the samples.

| Backend | Field order | Optional Scan | Resumable Scan |
| --- | --- | ---: | ---: |
| Go | Declaration | 0.1590 s | 0.1496 s |
| LLVM | Declaration | 0.0964 s | 0.0854 s |
| Go | Reversed | 0.2005 s | 0.2014 s |
| LLVM | Reversed | 0.1072 s | 0.0981 s |

Declaration-order time falls by 5.9% in Go and 11.5% in LLVM. Reversed Go
remains within run variation: its 0.4% slower point estimate has a paired
bootstrap 95% interval spanning 2.1% slower to 1.7% faster. Reversed LLVM falls
by 8.4%.

A boundary-heavy control caps the chunks supplied to JSON at 64 bytes through
an additional Reader layer over the ordinary buffered file source. Both
variants share that layer, the current compiler, and the same input/checksum
workload; the baseline restores the original Json library. Fifteen paired
runs with the same ordering and warmup protocol give:

| Backend | Field order | Optional Scan, 64-byte chunks | Resumable Scan, 64-byte chunks |
| --- | --- | ---: | ---: |
| Go | Declaration | 0.3659 s | 0.3343 s |
| LLVM | Declaration | 0.2309 s | 0.2141 s |
| Go | Reversed | 0.3732 s | 0.3567 s |
| LLVM | Reversed | 0.2311 s | 0.2191 s |

Declaration-order time falls by 8.6% in Go and 7.3% in LLVM; reversed-field
time falls by 4.4% and 5.2%. Every checksum matches. The extra reader layer's
cost is included, so these absolute times describe this controlled source.

Keeping the Scan cursor at its original size matters: an earlier otherwise
similar version with an extra pending-lookahead flag measured 0.2102 s for
reversed Go, versus 0.1993 s for its paired baseline.

An earlier effectful-cursor experiment measured diagnostic path cost separately.
Removing path entry/restoration only in a private benchmark copy changed
declaration-order Go from 0.1955 s to 0.1806 s, and LLVM from 0.1044 s to
0.0988 s. Its paired optional-scan baseline measured 0.1611 s and 0.0959 s.
The seven rotated runs verified all checksums but did not establish diagnostic
correctness for that copy. Line/column tracking stayed enabled. Path allocation
contributes to the cost, but removing it did not recover the Go fast path;
the implemented scanner keeps diagnostics and avoids path work on successful
pure scans.

On the original Apple M5 Max/macOS arm64 host with Go 1.26.7 and default GC
settings, the 10,000,260-byte fixture measured as follows. The baseline is the
original diagnostic at revision `49685999fa953b33cbe4c09ea13b25a11a31889f`;
its polling runner could add up to 50 ms. Allocation totals come from separate
profiled runs, not the timing samples.

| Measurement | Original | Measured optimized decoder |
| --- | ---: | ---: |
| Whole-process wall time | 15.159 s | 1.163 / 1.179 / 1.174 s |
| Cumulative allocated bytes | 27,084,442,272 | 141,412,216 |
| Allocation events | 650,664,856 | 5,045,371 |
| GC cycles | 3,016 | 26 |

The median improvement is 12.9× over the original. The current Go control
median is 0.0718 s, leaving a 16.4× gap. The retained decoded heap is 13.3 MB
at the pre-verification snapshot. Decode alone allocates 134,383,864 bytes in
4,909,041 events; the table includes the subsequent checksum fold and profiling
overhead. Product construction allocates zero objects in the probe; state
execution allocates 12 objects per
activation for both one and one thousand operations. Remaining optimization
work belongs in the [roadmap](../roadmap.md#json-and-generated-code-performance).

`-diagnostics -runs 5` isolates the reader layers on the same fixture. It times
an `Encoding.decodeAt` traversal, scalar reads, pull-token traversal, and typed
decoding through file, preloaded-byte, and preloaded-text readers. Loading and
adapter construction precede the compute timer; typed verification follows it.
Go independently checks source scalar counts/code sums and token counts/decoded
string bytes/number bytes/boolean/null totals. Counters and generated-code
experiments live only in exported copies, with no changes to the compiler or
stdlib. Counter runs are separate from timing samples. The experiments simplify
bound local-state callbacks, pass immutable evidence bindings by pointer, and
short-circuit an extension by its already-visible first binding. They retain
snapshot/commit synchronization; counter comparisons require unchanged state
and reader operations. Rewrite counts record which changes an exported runtime
still needs; an implemented pointer representation is left as-is, so that
variant then measures only the first-binding shortcut. Existing evidence shadowing and task-rebasing tests run
against the changed exported runtime after timing ends.

On the same host at `f10775e`, five runs of the 10,000,260-byte fixture give
these approximate median compute times:

| Traversal | File reader | Preloaded bytes | Preloaded text |
| --- | ---: | ---: | ---: |
| Scalar reads | 0.646 s | 0.647 s | 0.339 s |
| JSON tokens | 0.633 s | 0.621 s | 0.576 s |
| Typed JSON | 0.915 s | 0.889 s | 0.893 s |

The pure Encoding traversal takes 0.053 s, with no allocation. Typed file
decoding performs 22,352,628 state snapshots, 28,978,680 state commits,
6,932,599 value-row extensions, and 32,899,179 binding comparisons, against
only 1,223 native file reads. Source preloading therefore has little effect
on JSON timing. Token traversal already accounts for most of the typed run;
reader/state bookkeeping is a larger target than raw UTF-8 scalar decoding.
The callback experiment saves about 4%, the binding experiment about 6%, and
their combination about 11% in five alternating baseline/variant pairs. These
experiments isolate removable overhead; binding comparisons now pass the
existing immutable pointer in the production runtime. The binding experiment
leaves almost all comparisons in place: its gain comes
from changing their argument representation, while repeated lookup remains.
Preloaded text also allocates about 389 MB versus 146 MB for typed file input,
so a shorter reader stack alone does not establish a better implementation.
On this Darwin/arm64 host, Go CPU profiles report syscall/madvise percentages
inconsistent with process system CPU accounting. Native sampling and controlled
layer comparisons provide the cross-check; do not interpret those profiles as
evidence that file I/O dominates.

The [buffered token scanner](json.md) and pointer comparisons were measured
against saved `f10775e` binaries on this same 10 MB fixture. Seven fresh,
alternating whole-process samples per implementation, all with matching typed
checksums, give the following medians. Allocation totals are from separate
profiled runs; operation counts are from separate diagnostic runs.

| Measurement | `f10775e` | Buffered token scanner |
| --- | ---: | ---: |
| Whole-process wall time | 0.916 s | 0.681 s |
| Go control wall time | 0.066 s | 0.066 s |
| Cumulative allocated bytes | 146,473,296 | 131,434,512 |
| Allocation events | 5,211,987 | 4,337,072 |
| State snapshots + commits | 51,331,308 | 21,505,932 |
| Byte-reader skips | 3,145,798 | 1,559,497 |
| Native file reads | 1,223 | 1,223 |

Elapsed time falls 25.6%, allocations 10.3%, and allocation events 16.8%.
The retained typed result remains about 13.3 MB. Three diagnostic layer rounds
put token traversal at 0.415 / 0.399 / 0.397 s for file / bytes / text, and
typed traversal at 0.662 / 0.655 / 0.641 s. The pure Encoding traversal remains
0.053 s. The remaining whole-process gap with Go is 10.3×; dispatch at token
boundaries and derived decoding still cost much more than byte decoding.
Window/token-attempt/commit and incremental-token counters report 1,543,774
buffered commits and 1,222 incremental scans: 99.92% of lexing operations use
the buffered path on this fixture. Correctness coverage compares
one-byte chunks with every split of valid and malformed examples, including
remaining parent bytes, and covers long tokens, Latin-1, escaped surrogate
pairs, precise byte positions, and source effects. This comparison uses only
the 10 MB fixture; it does not establish timing for other input shapes.

Passing the derived record's immutable slot tuple through its key loop removes
the private local handler allocation and its state operations. An ASCII fast
path in Encoding also avoids the general UTF-8 decoder for single-byte scalars.
Seven fresh alternating whole-process runs per implementation on the same
fixture give medians of 0.684 s for the committed buffered scanner (`ff48710`)
and 0.554 s with loop-carried slots; Go takes 0.068 s. Every checksum matches.
A separate profiled run allocates 75,081,344 bytes in 3,049,573 events, while
retaining the same 13.3 MB typed result. Elapsed time falls 19.0%, and allocated
bytes fall 42.9%. Actual handler snapshot/commit synchronization is
unchanged.

Three diagnostic layer rounds put typed traversal at 0.537 / 0.526 / 0.519 s
for file / bytes / text and token traversal at 0.411 / 0.395 / 0.388 s. Pure
Encoding traversal takes about 0.048 s. Typed file traversal performs
19,173,294 state snapshots and commits, with the same 1,559,497 byte skips and
1,223 native reads. All 61 diagnostic samples match their expected checksums.
The remaining whole-process gap with Go is about 8.2×; token-boundary dispatch
and token construction remain larger costs than UTF-8 decoding.

On this host the CPU profiler is unreliable for compiled Fango: SIGPROF lands
on idle threads, so most samples report syscalls and madvise while process
system time stays near two percent. Ablation builds of the harness's exported
project, one construct changed at a time and timed over alternating fresh
runs, give dependable deltas; a looped decode profiled with idle-thread
samples ignored gives a usable distribution. Allocation profiles are
unaffected. A round of structural changes measured this way, three fresh
alternating runs per step on the 10 MB fixture with matching checksums,
gives these medians:

| Change | Whole-process wall time |
| --- | ---: |
| loop-carried slots (`7623816`) | 0.545 s |
| state cells synchronize only once a task inherits them | 0.497 s |
| ASCII decoded inline by the window scanners | 0.430 s |
| error paths as a segment stack | 0.434 s |
| keys and separators consumed without lookahead | 0.411 s |
| renaming temporaries elided from emitted Go | 0.372 s |
| cleanup scopes over literal callbacks lowered at the call | 0.345 s |
| Go control | 0.065 s |

The segment stack removes 15% of allocation events without changing time:
a field's remaining cost is its two handler operations, not its allocation.
A separate profiled run of the final state allocates 77,215,808 bytes in
2,595,258 events and retains the same 13.4 MB typed result; three fresh runs
of that build measured 0.324 to 0.329 s. The remaining gap with Go is about
5×. Two further changes were measured and rejected: keeping the staged
window in the pull state and committing lazily removed every per-token reader
operation yet ran slower, because the larger state cost more to snapshot,
store, and return than the reader chain it replaced; and a tagged value
layout for `Json.Token`, with one slot per payload or with same-typed slots
shared across constructors, copied more than the allocation it removed. Both
point at the same conclusion as the copy-elision gain: what an operation
moves now matters more than what it allocates.

A further 10 MB same-host comparison isolates bounded direct construction for
short decoded lists. Ten alternating pairs of saved binaries with matching
checksums measured medians of 0.3353 s before and 0.3229 s after (3.7% less
time). A separate profile measured 73.96 MB and 2,505,077 allocations during
decode, versus 77.22 MB and 2,595,259 before; the full typed result still
retains about 13.4 MB. The large outer list uses the constant-stack path;
the short `tags` and `items` lists avoid building a second spine.

The next isolated change removes bound-callable forwarding in generated Go for
the local state cell, while retaining the operation dispatch itself. Ten
alternating pairs of the saved short-list binary and the new compiler output,
with the same 10 MB input and matching checksums, measured medians of 0.3231 s
and 0.3143 s respectively (2.7% less time). The final guarded pass emits the
same direct operation calls and verified at 0.314–0.315 s in three more runs;
the Go control took 0.065–0.067 s. These measurements leave roughly a 4.8×
whole-process gap. The remaining pull handler still dispatches an operation,
copies its state at snapshot/store, and carries the token and cursor returned
by the lexer. A final profiled run allocates 73.96 MB in 2,504,360 decode
allocations, retaining 13.39 MB of typed output: this change saves call time,
not allocation. Two final diagnostic rounds put token/file traversal at
0.239 s and typed/file traversal at 0.298 s; pure Encoding takes 0.042–0.043 s.
The diagnostic harness includes the bound-cell exported-Go variant only when
the compiler output still contains the forwarding adapters.

The [shared-window pull cursor](json.md) was compared with saved `81f96f6`
binaries on the same 10,000,260-byte fixture, using seven rotated sequential
fresh-process runs per implementation after one warmup each. Compilation is
excluded; wall time includes startup, file I/O, typed decode, checksum folding,
and exit. No builds or tests ran concurrently, and every checksum matched.
These are whole-process measurements on the macOS ARM64 host, not parser-only
throughput or a claim about other input shapes.

| Implementation | Before | Shared-window cursor | Median peak RSS before / after |
| --- | ---: | ---: | ---: |
| Fango, Go backend | 0.341 s | 0.272 s | 28.7 / 29.1 MB |
| Fango, LLVM backend | 0.239 s | 0.174 s | 54.5 / 50.8 MB |
| Plain Go control | 0.079 s | 0.079 s | 50.3 / 50.3 MB |

The redesign reduces wall time by 20.2% for Go and 27.2% for LLVM. The remaining
gaps to the Go control are about 3.5× and 2.2×. Unlike the rejected larger
window-in-state experiment above, this cursor shares the immutable buffer
description and moves only its pointer and position; fused keys and typed
scalar reads also avoid intermediate tokens. These measurements cover the
combined redesign and do not isolate each change's contribution.

Five fresh alternating runs of saved binaries isolate
[immediate-application lowering](backend.md#representations-and-abi) from
between-session variation: median time changes from 1.133 s to 1.099 s,
a 3.0% improvement, with the new binary faster in every pair. Allocation totals
are effectively unchanged. This workload derives and invokes Decode; it does
not exercise the encoder's immediate sequence lambdas.

String-list nodes account for roughly a quarter of sampled allocation bytes.
Other sources include decoder callbacks, token constructors, text spans, file
chunks, and retained result lists. Residual row extensions no longer dominate.
A diagnostic run with `GOGC=off` took 1.056 s, so eliminating GC alone would
not close the remaining gap. CPU samples include reader state access, copying,
and substantial Darwin/runtime activity; they do not establish precise
wall-time percentages for GC or disk IO.

A single 500,000,417-byte run completed with all checksums correct in 53.137 s
(1.26 GB peak RSS); the Go control took 2.719 s (1.72 GB peak RSS). This larger
sample predates immediate-application lowering. The original
Fango run was interrupted at 172.339 s before decoding finished, so it provides
no completed-run speedup ratio. Retaining the complete decoded document still
has substantial memory cost.

The last deliberate performance-gate run, before the latest forwarding and
aggregate-slot changes, reported missing `capture-pure` and `capture-fail`
compile-latency baselines and failing sum/mapfilter/state runtime ceilings.
The original revision reproduced all three runtime failures on the same host
(58.1/67.2/105.8 ms). Those timing gates have not been rerun for the latest
implementation. Thresholds remain unchanged; this JSON comparison does not
waive those gates. The full correctness/differential suite, runtime race tests,
Go vet, source formatting, and editor grammar checks pass. The Go formatting
check excludes the original unformatted diagnostic sources in `json-benchmark/`,
which remain untouched.
