# Testing and performance

Correctness gates, differential fixtures, generated-code stability, and manual performance measurements.

[Design index](../design.md). Source and checks: [Verification targets](../../Makefile), [Differential harness](../../cmd/fango/e2e_test.go), [Module tests](../../cmd/fango/modules_test.go), [Latency gates](../../benchmarks/latency_test.go), [Runtime gates](../../benchmarks/runtime_test.go).

## Verification commands

| Command | Purpose |
| --- | --- |
| make test | Correctness, including full interpreter/compiler differential tests |
| make test-short | Short-mode tests without compiled differential legs |
| make ci | Go/Fango formatting, go vet, and correctness |
| make update-goldens | Intentional lexer/parser/infer/elaborate/REPL/formatter golden updates |
| go vet ./benchmarks | Build-check benchmarks without timing them |
| make test-perf | Manual latency and runtime-ratio gates on an idle machine |

Timing gates are excluded from correctness and CI. Do not run them during ordinary
development. Syntax changes also require the TextMate checks in
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

## Performance evidence

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
