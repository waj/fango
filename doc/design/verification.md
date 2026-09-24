# Testing and performance

Correctness gates, differential fixtures, generated-code stability, and manual performance measurements.

[Design index](../design.md). Source and checks: [Verification targets](../../Makefile), [Differential harness](../../cmd/fango/e2e_test.go), [Module tests](../../cmd/fango/modules_test.go), [Latency gates](../../benchmarks/latency_test.go), [Runtime gates](../../benchmarks/runtime_test.go).

## Verification commands

| Command | Purpose |
| --- | --- |
| make test | Correctness, including full interpreter/compiler differential tests |
| make test-short | Short-mode tests without compiled differential legs |
| make test-grammar | TextMate tokenization of every .fango file under stdlib, testdata, and examples |
| make ci | Go/Fango formatting, go vet, and correctness |
| make update-goldens | Intentional lexer/parser/infer/elaborate/REPL/formatter golden updates |
| go vet ./benchmarks | Build-check benchmarks without timing them |
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

| Fixture sibling | Input/expectation |
| --- | --- |
| .expected | Exact output |
| .stdin | Scripted standard input |
| .args | One program argument per line |
| .status | Expected exit status |
| .files/ | Seed data copied to a fresh working directory for each backend |
| .native.go | Sidecar installed in a private interpreter worker |

Portable failure tests use missing paths or a directory opened as a file, not chmod.
Stateful command examples run sequences in isolated directories with matched argv
and working-directory contexts. Examples are runnable source under
[examples](../../examples/), with coverage in the CLI tests; the
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
The scheduler storage probe runs short and long schedules in one instrumented
fixture, checking bounded live state after both schedules. The interpreter
probe emits the generated Go after its checked compilation, reusing its Fango
module objects; the compiled probe then runs those bytes in a private project.
The probe runs alongside the other parallel checks.

## Performance evidence

### Cooperative Async comparison

```sh
go run ./benchmarks/asynccompare -out /tmp/fango-async-evidence -profile
```

The output directory must not exist. The runner snapshots the historical native
Async implementation at `b102a4e10bb6c4199fd5445ca9003fa42b29be02` and the tracked
working tree, builds both with the same Go toolchain, and retains sources,
binaries, raw JSON samples and optional CPU/allocation profiles. The shared
workload differs only at the runner boundary (`Async.run` versus
`Async.Cooperative.run` and its Result). Each worker counts and yields, and the
driver awaits every result; checksums are verified before and after timing.

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

Cases cover direct traversal, cursor consumers, pipeline depth, lists, zip,
early stop, reopening, residual State and cleanup. Revisions supporting
the C2 Coroutine/Iterator API additionally run direct Coroutine, Iterator, an
independently compiled Pull abstraction, handwritten frames using the same
runtime, and a specialized Go pull state machine. These diagnostic controls do not replace the historical
Stream gate. Instrumented binaries count dispatcher steps, frame factories and
nonempty evidence extensions separately; their timings never enter comparisons.
Allocation profiles and raw JSON are retained beside the source snapshots.

Full parity requires every primary case's median optimized/baseline ratio to
be at most 1.00, its bootstrapped upper 95% bound at most 1.03, and no increase
in bytes or allocations, in both rounds. Partial runs are inconclusive. These
are manual same-host evidence requirements, not portable CI timing thresholds.
The runner reports full parity independently of a project decision to accept
specific remaining costs; that decision and unfinished optimization work belong
to the [coroutine roadmap](../roadmap-coroutines.md#performance-prerequisite).
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
