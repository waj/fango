# Testing and performance

Correctness gates, differential fixtures, generated-code stability, and manual performance measurements.

[Design index](../design.md). Source and checks: [Verification targets](../../Makefile), [Differential harness](../../cmd/fango/e2e_test.go), [Module tests](../../cmd/fango/modules_test.go), [Latency gates](../../benchmarks/latency_test.go), [Runtime gates](../../benchmarks/runtime_test.go).

## Verification commands

| Command | Purpose |
| --- | --- |
| make test | Correctness, including full interpreter/compiler differential tests |
| make test-llvm | Opt-in LLVM differential and command gate in the Nix shell on macOS ARM64 |
| make test-short | Short-mode tests without compiled differential legs |
| make test-grammar | TextMate tokenization of every .fango file under stdlib, testdata, and examples |
| make ci | Go/Fango formatting, go vet, and correctness |
| make update-goldens | Intentional lexer/parser/infer/elaborate/REPL/formatter golden updates |
| go vet ./benchmarks | Build-check benchmarks without timing them |
| go test -race ./runtime/fangort ./runtime/nativeworker ./internal/eval ./internal/nativehost ./stdlib/... | Race checks for shared storage, callback drain, native hosts, and bundled adapters |
| go test -race ./cmd/fango -run TestConcurrentSharedListBackends | Interpreter and emitted Direct shared-List race fixture |
| make test-perf | Manual latency and runtime-ratio gates on an idle machine |

Timing gates are excluded from correctness and CI. Do not run them during ordinary
development. Syntax changes also require `make test-grammar`, whose Node and grammar
packages come from the Nix development shell; see
[repository instructions](../../AGENTS.md). No documentation change weakens these gates.

## Differential fixtures and examples

Lexer, parser, inference, elaboration, and REPL use unit tests/goldens. Every runnable
fixture evaluates Core and, outside short mode, compiles through the real compiler;
compare outputs byte-for-byte against expectations and each other. Invalid fixtures
pin diagnostic substrings. Focused checker/elaboration tests load the real bundled
Prelude closure. Core lint runs in every batch compilation; malformed-Core tests
exercise independent rejection.

| Fixture file | Input/expectation |
| --- | --- |
| .expected | Exact output |
| .stdin | Scripted standard input |
| .args | One program argument per line |
| .status | Expected exit status |
| .files/ | Seed data copied to a fresh working directory for each backend |
| .native.go | Sidecar installed in a private interpreter worker |
| .native.c | Parallel sidecar used by the experimental LLVM gate |

Portable failure tests use missing paths or a directory opened as a file, not chmod.
Stateful command examples run sequences in isolated directories with matched argv
and working-directory contexts. Example sources stay directly under
[examples](../../examples/); their expectations, scripted inputs, arguments,
and seed files live under [examples/fixtures](../../examples/fixtures/), with
coverage in the CLI tests; the
[example roadmap](../roadmap-examples.md) contains only unfinished work.

Runnable fixtures share one generated Go project: each entry has its own package,
shared dependencies are emitted once and asserted byte-identical across consumers,
and one Go build creates fixture binaries. The build overlaps interpreter legs;
compiled cases then run in parallel. Examples/multi-module runners compile through
the real CLI once per runner, preserving isolation when temporary sources change.
Dedicated command tests cover the run wrapper.

Interpreter legs are serialized because native workers/host contexts are process-level
infrastructure. Handler-local State and Random cells are independent. Correctness
commands use Go test parallelism sixteen to let structural/compiled checks advance
while interpreter access is serialized. Generated Go must be deterministic and
gofmt-idempotent; consumer-independent emission is a cross-fixture invariant.
Generated-project tests share Go's build cache with the rest of the suite. CI
removes the unused Android SDK from the Ubuntu runner before restoring that cache,
leaving disk space for the generated projects and Go's temporary build files.
CI gives packages a 45-minute timeout because the full differential suite runs
many compiled fixtures on a shared runner.
The scheduler storage probe runs short and long schedules in one instrumented
fixture, checking bounded live state after both schedules. The interpreter
probe emits the generated Go after its checked compilation, reusing its Fango
module objects; the compiled probe then runs those bytes in a private project.
The probe runs alongside the other parallel checks.
The shared-List concurrency fixture runs in the interpreter and emits a separate
Go project whose Direct workers extend the same published list.
Its emitted leg invokes `go test -race` even when the parent suite runs without
the race detector; run the parent under `-race` to instrument the interpreter
leg too. This checks runtime reentrancy before a source-level concurrent
executor is available.

## Performance evidence

### LLVM backend comparison

```sh
nix develop
go run ./benchmarks/llvmcompare -out /tmp/fango-llvm-evidence
```

This opt-in command builds Go and LLVM executables from the existing whole-document
typed JSON workload, generates a ten-megabyte input (or accepts `-input`), checks
matching output, warms both programs, and alternates their order in three paired
fresh-process rounds. `evidence.json` records input/source checksums, Clang identity,
elapsed time including process startup, and macOS maximum resident memory.
Compilation and warmup are excluded from measured rounds. Run on an otherwise idle
macOS ARM64 host. There is no CI threshold or required speedup; no LLVM performance
result has been recorded yet.

The immutable List and native task redesign has no new timing measurements.
Run performance comparisons only on an idle host. Existing thresholds and
historical sources remain available; passing build and correctness checks does
not establish performance parity.

### Go lowering comparison

```sh
go run ./benchmarks/loweringcompare -out /tmp/lowering-evidence \
  -baseline 30f8148 -samples 7 -rounds 2 -profile
```

The command snapshots the baseline revision and current working tree, builds
independent compilers/projects, and retains generated source, build timings, binary sizes, checksums, alternating runtime samples, allocation counts,
bootstrap ratio intervals, Go compiler diagnostics, CPU/allocation profiles,
and the existing macro programs compared between compilers.
Run it on an otherwise idle host after correctness checks finish. The baseline
must be a revision preceding the lowering change.

Input lists are built before timing. Cases cover known and unknown curried
callbacks, unary invocation, Direct/Exit folds, handler state, brackets, generic
string dictionaries, large product results, and handwritten Go loop baselines.
Structural and allocation checks run in correctness CI without timing thresholds.
Macro samples include process startup; focused samples time the computation
inside one process. `-reuse` repeats only macro comparisons using the retained
snapshots. The comparison does not replace the existing runtime and latency gates.

On 2026-09-30, Go 1.26.7 on darwin/arm64, GOMAXPROCS=1, the idle comparison
against `30f8148297263f41e78d0e5afe9fba98f9562af4` used seven alternating
samples in each of two rounds. Current/original median runtime ratios were:

| Focused case (10,000 elements/iterations) | Round 1 | Round 2 | Allocations per invocation, original → current |
| --- | ---: | ---: | ---: |
| Known fold | 0.056 | 0.059 | 20,000 → 0 |
| Unary callback | 0.378 | 0.360 | 0 → 0 |
| Exit fold | 0.161 | 0.164 | 10,004 → 4 |
| State | 0.999 | 0.998 | 8 → 8 |
| Bracket | 0.510 | 0.507 | 0 → 0 |
| Generic String Eq dictionary | 0.997 | 1.005 | 20,000 → 20,000 |
| Large product Exit result | 0.678 | 0.680 | 4 → 4 |
| Escaping partial callbacks | 0.615 | 0.616 | 60,000 → 40,000 |

Callback/control geometric mean ratios were 0.324 and 0.326. Complete cold,
warm, and changed build ratios were 1.014, 1.004, and 0.996; cold means a cold
Fango cache with Go's shared compilation cache retained. Focused generated source
was 156,925 → 153,684 bytes, and the measurement binary 4,720,178 → 4,716,546
bytes. The handwritten Go controls stayed near parity. No focused or macro case
had a reproducible regression above 5% across both rounds' bootstrap intervals.
Macro map/filter ratios were 0.850 and 0.860; bracket ratios 0.434 and 0.439.
Other macro programs remained near parity.

Allocation profiles attribute the original fold's per-element allocation to its
curried callback factory. The generic dictionary case still allocates in the
String Eq factory; selected saturated parameters do not specialize an abstract
dictionary's methods. Existing handwritten-Go runtime gates still fail for sum
and state, and compile-latency gates lack capture-pure/capture-fail baselines.
All three conditions reproduce on the original revision. Current map/filter and
bracket pass their existing gates, which fail on that original revision; thresholds
and stored baselines were left intact.

### Typed JSON comparison

```sh
nix develop -c go run ./benchmarks/jsoncompare -out /tmp/fango-json-evidence -profile -probes
```

The opt-in harness generates a deterministic 10 MB nested order array and
compares Fango's derived `Json.Decode` with Go's `encoding/json.Decoder`.
Both retain the complete typed result, then verify independently generated
record, ID, money, quantity, and UTF-8 byte checksums. `-bytes 500000000`
selects the larger fixture; `-input FILE` reuses a fixture and its adjacent
`.meta.json`. Output directories must be new. Run from the Nix development
shell so the generator uses its Python interpreter.

Three fresh processes per implementation run sequentially in alternating order.
Wall time includes startup, file IO, decoding, verification, and exit; generation
and compilation are excluded. Blocking process waits avoid polling delay.
The harness records every sample, user/system CPU, peak RSS, runtime settings,
revision, working-tree patch, new implementation sources, exported Go project,
and checksums. Each subprocess has a ten-minute timeout. These are manual
same-host measurements, not portable timing gates.

`-profile` builds a separate executable and records CPU/allocation profiles and
MemStats. Its decode boundary precedes the checksum fold, with the decoded
output still needed by that fold. A forced GC there measures retained heap;
profile timings include instrumentation and must not replace ordinary timings.
`gc_available_cpu_fraction` measures GC's share of available CPU, not elapsed
wall time. `-probes` checks that state callback invocation and typed
product/Maybe/Result construction have no allocation growth between one and
one thousand operations; fixed activation allocations are reported separately.

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

### Task comparison

```sh
go run ./benchmarks/asynccompare -out /tmp/fango-async-evidence -profile
```

The output directory must not exist. The runner snapshots the historical native
Async implementation at `b102a4e10bb6c4199fd5445ca9003fa42b29be02` and the tracked
working tree, builds both with the same Go toolchain, and retains sources,
binaries, raw JSON samples and optional CPU/allocation profiles. Historical
revisions retain their Async workloads; the current workload uses closure-based Async
tasks with explicit cancellation checks. A check does not yield to a cooperative
scheduler, so the comparison measures the architectural change as well as costs.
Checksums are verified before and after timing.

Timing invokes generated code in-process, excluding compilation, startup and
printing. Repetitions are calibrated from the faster build; order alternates
over seven samples and two rounds, with one Go worker by default. Run on an
otherwise idle host. Sustained cases use 1, 2, 16 and 128 workers with 320,000
total yields; short-task and no-yield cases expose setup and completion costs.
The sustained-yield target is a current/baseline median ratio at most 1.10 in
each round. This is manual same-host evidence, not a portable CI timing gate.

### Stream comparison

The opt-in Stream comparison runs separately from `make test` and `make ci`:

```sh
go run ./benchmarks/streamcompare -out /tmp/fango-stream-evidence
```

The output directory must not exist. The runner snapshots the historical
pre-coroutine revision, pre-Stream-migration revision, current HEAD, and working
tree; builds them with one Go toolchain; and verifies independently computed
checksums. Generated Go test harnesses time the Fango worker in-process and
report bytes and allocations. Input sizes are identical, repetitions are
calibrated using the fastest build, execution order alternates, and compilation,
printing and process startup are outside the timer. Default measurements use
one Go worker, 15 samples, and two rounds. Run on an otherwise idle host.

Current sources use explicit iterator state; archived revisions retain their
generator-based workloads. Cases cover direct traversal, cursor consumers, pipeline depth, lists, zip,
early stop, reopening, residual State and cleanup. Revisions supporting
the C2 Coroutine/Iterator API additionally run direct Coroutine, Iterator, an
independently compiled Pull abstraction, handwritten frames using the same
runtime, and a specialized Go pull state machine. These diagnostic controls do not replace the historical
Stream gate. Instrumented binaries count dispatcher steps, frame factories and
nonempty evidence extensions separately; their timings never enter comparisons.

### HTTP response comparison

```sh
go run ./benchmarks/httpcompare -out /tmp/fango-http-evidence.json
```

This opt-in tool builds a static-response Fango HTTP server and a plain Go
`net/http` server with the same body. It excludes compilation and startup from
timing, warms persistent loopback connections, checks every response, and times
complete request/response exchanges with one and sixteen clients. Three paired
rounds alternate server order. The report shows mean, p50, p95, p99, and
aggregate requests per second; optional JSON retains every latency and the
source checksum. Both servers use the same `GOMAXPROCS` setting. The shared Go
client and loopback transport contribute to both measurements, so the numbers
describe end-to-end response time, not isolated server CPU cost. Run on an
otherwise idle host; these measurements are not a CI threshold.
`-server-gogc off` is a diagnostic run that disables GC in both server processes
while leaving the client unchanged; it is not a production comparison.
Allocation profiles and raw JSON are retained beside the source snapshots.

Full parity requires every primary case's median optimized/baseline ratio to
be at most 1.00, its bootstrapped upper 95% bound at most 1.03, and no increase
in bytes or allocations, in both rounds. Partial runs are inconclusive. These
are manual same-host evidence requirements, not portable CI timing thresholds.
The runner reports full parity independently of a project decision to accept
specific remaining costs. Future optimization work belongs in the
[current roadmap](../roadmap.md#current-foundation).
Keep the historical baseline and compare against the accepted implementation
as well when evaluating subsequent changes.
The original checked Core remains the semantic reference; an internal
optimization-disabled lowering path supports deterministic differential tests.

Distinguish setup cost from per-element cost: compare long traversals with
repeated short ones, then count frames, dispatcher steps, and evidence extensions
at multiple input sizes. Bounded live depth alone does not prove bounded frame
allocation, and constant frame allocation does not eliminate boxing or closures.
Use the independent Pull library to check that improvements generalize beyond
Stream, the handwritten runtime control to isolate dispatcher overhead, and the
specialized Go control to show remaining representation costs. These controls
do not establish that every gap is removable. Check profile sample quality before
attributing CPU time; profiles dominated by host event waits cannot locate a
compiler hot path reliably. Retain uninstrumented timing and instrumented counts
as separate evidence.

Latency benchmarks cover cold, warm-unchanged, and warm-changed builds against
machine-specific baselines. Compact local-helper diamonds, pure and under Fail.attempt,
exercise capture sharing; deterministic Core tests assert context/object growth and
one evaluation per context per generation without relying on wall-clock thresholds.

Runtime gates compare scalar, matching, string, list, tree, and repeated state
operations against handwritten Go with per-case ratio ceilings. The State baseline
uses the same one-cell/two-closure setup. List cases use the real bundled representation;
branchcons measures branching against cons cells, and tree retains ordinary pointer ADTs.

Both gates depend on host load; same-host Go ratios do not remove contention.
Latency baselines are additionally machine-specific. Whole-process runtime timings
include spawn/collection, understating short workloads' compute differences. Use
measurements to choose optimizations without treating those ratios as pure operation
costs. Baseline portability and retuning remain in the [roadmap](../roadmap.md#product-polish).
