# Testing and performance

Correctness gates, differential fixtures, generated-code stability, and manual performance measurements.

[Design index](../design.md). Source and checks: [Verification targets](../../Makefile), [Differential harness](../../cmd/fango/e2e_test.go), [Module tests](../../cmd/fango/modules_test.go), [Latency gates](../../benchmarks/latency_test.go), [Runtime gates](../../benchmarks/runtime_test.go).

## Verification commands

| Command | Purpose |
| --- | --- |
| make test | Correctness, including full interpreter/compiler differential tests |
| make test-short | Short-mode tests without compiled differential legs |
| make test-grammar | TextMate tokenization of every .fango file under stdlib, testdata, and examples |
| make check-files | Reject executable or oversized files in the Git index |
| make ci | Indexed-file check, Go/Fango formatting, go vet, and correctness |
| make update-goldens | Intentional lexer/parser/infer/elaborate/REPL/formatter golden updates |
| go vet ./benchmarks | Build-check benchmarks without timing them |
| go test -race ./runtime/fangort ./runtime/nativeworker ./internal/eval ./internal/nativehost ./stdlib/... | Race checks for shared storage, callback drain, native hosts, and bundled adapters |
| go test -race ./cmd/fango -run TestConcurrentSharedListBackends | Interpreter and emitted Direct shared-List race fixture |
| make test-perf | Manual latency and runtime-ratio gates on an idle machine |

Timing gates are excluded from correctness and CI. Do not run them during ordinary
development. Syntax changes also require `make test-grammar`, whose Node and grammar
packages come from the Nix development shell; see
[repository instructions](../../AGENTS.md). No documentation change weakens these gates.

`make check-files` keeps build outputs out of history: no indexed file outside
`.githooks/` may be executable or exceed 512 KiB. `fango build` writes its
executable to the current directory, and every Go executable is larger than
the limit. The check is `.githooks/pre-commit`; the Nix development shell sets
`core.hooksPath` to `.githooks`, so it also runs before each commit from that
shell, and `make ci` repeats it in case the hook was bypassed.

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

Portable failure tests use missing paths or a directory opened as a file, not chmod.
Stateful command examples run sequences in isolated directories with matched argv
and working-directory contexts. Example sources stay directly under
[examples](../../examples/); their expectations, scripted inputs, arguments,
and seed files live under [examples/fixtures](../../examples/fixtures/), with
coverage in the CLI tests; the
[example roadmap](../roadmap-examples.md) contains only unfinished work.

The HTTP server example's socket client sends a write-side EOF only for the
truncated-body case. Complete requests are framed by HTTP, and rejected requests
can close the peer before a client half-close; response parsing and body reads
check their outcomes without racing that shutdown.

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

[Typed JSON comparison](json-performance.md) documents the opt-in 10 MB
Fango/Go/Aeson harness, workload and forcing contracts, runtime settings,
profiling controls, and same-host measurements.

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
