# Effect handler performance

The opt-in [effectscale tool](../../benchmarks/effectscale/) measures compiled
handler costs and build scaling. [Verification](verification.md#effect-handler-comparison)
links the invocation; [effect execution](effects.md) and
[the backend ABI](backend.md#representations-and-abi) own the implemented contracts.

## Running and comparing

Run on an otherwise idle host, after correctness checks finish. The harness
reaches row lookup through `fangort.ValueEvidence`, so its helper controls show
the search but not the inlined first-slot check generated code uses.

```sh
nix develop -c go run ./benchmarks/effectscale -out /tmp/effects-current
nix develop -c go run ./benchmarks/effectscale -out /tmp/effects-revision -baseline HEAD~1
nix develop -c go run ./benchmarks/effectscale -out /tmp/effects-later \
  -compare /tmp/effects-current/results.json
```

The output directory must be new and outside the checkout. Defaults are seven
samples in each of two runtime rounds, seven samples per build mode,
`GOMAXPROCS=1`, and at least 200 ms of computation per calibrated runtime sample.
`-samples`, `-rounds`, `-procs`, and `-min-time` override them.
`-shapes 1:1,3:2` selects handler-count:operation-count pairs for a smaller run;
the default sweeps handler counts 1, 2, 3, 8, 16, 32, 64, operation counts
1, 2, 8, 16, 32, 64, and the 16-handler/16-operation and
16-handler/64-operation interactions. Unrelated effects each have one operation;
the operation-count dimension changes the hot effect.

`-baseline` builds a compiler and library snapshot from the named Git revision
and one from the current working tree, then alternates measurements. Both use
the current tool's workload and harness. A historical compiler must understand
that workload, the residual-row ABI, and `-timings json`; failure is explicit.
`-compare` instead reads a completed saved run and compares its current samples
to the new run. The options are mutually exclusive. Saved runs must have the
same schema, workload/harness hash, selected shapes, and process count. Host,
Go, and sampling differences are flagged. Saved comparisons are historical
evidence rather than an alternating same-host experiment. The saved reference is
copied into the new output directory and its hash is recorded, so the comparison
retains its inputs if the original file moves.

## Measurement contract

Closed-row calls use explicit typed operation evidence. Open-row callbacks
include all surrounding effects and perform only the hot operation on their
normal path. The other operations are exercised once before timing and remain
reachable on a runtime-dependent negative-input branch. Rows are ordered by
effect name and lookup begins at the newest binding, so position depends on the
name rather than on lexical nesting: `AHot` sorts first, the deepest slot, and
the `openlast` cases use `ZHot`, the newest. A callback passed directly to a
worker that [forwards its row](lowering.md#callback-contracts) needs no lookup.
The `escaping` and `escapinglast` cases pass it through a function that returns
it, which keeps invocation-time lookup. No compiler optimization is disabled.

Stateful clauses advance an integer cell and produce a triangular checksum.
Stateless clauses, pure Fango loops, and handwritten Go loops provide separate
controls. The first/last operation cases select different fields of the same
effect record. They do not invoke every declared operation.

Native sidecar markers time computation inside the handler scopes, after
activation and initial unrelated-operation calls. Open-row construction occurs
before its timed worker starts. Allocation snapshots use the same boundaries;
snapshot collection is outside elapsed time. Setup samples instead time repeated
installation, one call per handler, and scope exit, so they include exercising
the installed handlers as well as activation construction. Each sample checks
its result. Input checks and process startup are outside runtime timing.

Direct Go helpers separately measure residual-row lookup at the newest/oldest
binding and whole-row extension from prebuilt bindings. These controls isolate
row costs without state cells or Fango callback transport. They are not estimates
of a cost paid by every Fango call. Whole-row extension includes duplicate and
visibility checks; it does not measure just adding one binding to an existing row.

Cold program builds start with fresh Fango caches and generated projects. Warm
builds prime a dedicated project, then rebuild unchanged source. Changed builds
modify the integer argument in `main`, changing executable behavior. Go's cache
is never reset. Compiler bootstrap, measurement-harness compilation, and
optimizer diagnostic collection are outside program build timing.

All variants for a shape share one source program. Code and executable sizes
therefore describe that entire probe suite, including imported modules, rather
than a minimal application with that shape. Source bytes exclude runtime support
and the test harness; executable bytes include the Go test harness.

## Evidence and interpretation

Each run retains compiler/library snapshots, dirty-tree patch/status, source
hashes, generated Fango/Go, native markers, measurement executables, Go optimizer
diagnostics, and build-stage JSON. `results.json` includes raw runtime and build
observations, configuration, host details, and comparisons. `runtime.csv`,
`build.csv`, and `report.md` expose samples and readable summaries. JSON checkpoints are atomic; an
interrupted run remains readable but cannot serve as a completed saved reference.

Ratios are measured case/reference, so lower is faster. Scaling references use
the corresponding one-handler/one-operation case; last-operation cases use its
first operation. Runtime intervals resample medians within each round. Historical
references pool saved rounds; build intervals resample build observations.
Bootstrap intervals describe sampling uncertainty, not systematic host changes.
There are no timing thresholds or automatic regression verdicts.

Cases whose within-round sample range exceeds 30% are repeated once; originals
remain in the results and the report flags the repeat. Compare both runtime
rounds and the sample ranges before attributing a change.
Inspect generated lookup placement, operation dispatch, handler retention, and
Go optimizer diagnostics to explain flat or growing curves. Allocation counts
are normalized per loop step or setup construction; raw totals and iteration
counts are retained. Build-stage timings distinguish frontend and emission
growth from Go compilation. Any measured conclusion is specific to the recorded
compiler, host, and workload; interpreter, task inheritance, and abort-heavy
execution are outside this experiment.

[`realistic.fango`](../../benchmarks/effectscale/realistic.fango) and
[`realistic_test.go.txt`](../../benchmarks/effectscale/realistic_test.go.txt)
cover what the sweep does not: abort-capable transport, abort depth, library
higher-order functions, non-tail recursion, and literals built per iteration.
Build the first with `fango build --emit-go`, copy the second into the emitted
entry as `measure_test.go`, and run it with `go test -bench`.

## Recorded compiled results

On 2026-10-31, alternating revision `3032f1b0` with the change that bound
forwarded-row callbacks at their call site, on an Apple M5 Max, darwin/arm64,
Go 1.26.7, `GOMAXPROCS=1`, seven samples in each of two runtime rounds. Normal
desktop processes remained active; repeated cases are flagged in the raw report,
which is not retained. Medians, ns per step, before → after:

| Active handlers | Closed state | Open, deepest slot | Open, newest slot | Escaping, deepest | Escaping, newest |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 2.41 → 1.01 | 6.62 → 1.75 | 6.63 → 1.74 | 7.44 → 3.80 | 7.36 → 3.80 |
| 3 | 2.42 → 0.91 | 8.79 → 1.69 | 6.64 → 1.69 | 9.63 → 8.23 | 7.42 → 3.75 |
| 8 | 2.43 → 0.91 | 16.39 → 1.82 | 7.72 → 1.81 | 17.42 → 15.54 | 8.60 → 7.20 |
| 16 | 2.41 → 0.91 | 28.07 → 2.10 | 7.72 → 2.09 | 29.23 → 26.86 | 8.60 → 7.21 |
| 64 | 2.21 → 0.91 | 98.59 → 5.76 | 7.81 → 5.77 | 103.15 → 101.80 | 8.63 → 7.22 |

Closed calls are flat in handlers and in operations per effect, at one and 64
members. The first-operation and last-operation cases at 64 members are 0.93 and
0.94. Steady-state samples show no recurring allocation. An escaping callback's
cost still follows its effect's row position; a deep slot is about 1.5 ns per
binding. Setup, including one call per handler, falls from 101.95 ns and 6
allocations to 92.71 ns and 5 at one handler, and from 4606 ns and 258 to 4150 ns
and 194 at 64.

External wall time in milliseconds, before → after, Go caches preserved. Binaries
are unchanged to about 2% larger.

| Handlers | Operations | Fresh Fango cache | Changed entry |
| ---: | ---: | ---: | ---: |
| 1 | 1 | 1279 → 1281 | 266 → 269 |
| 8 | 1 | 1421 → 1385 | 420 → 385 |
| 16 | 1 | 1994 → 1757 | 980 → 742 |
| 32 | 1 | 5529 → 3694 | 4506 → 2674 |
| 64 | 1 | 33535 → 18763 | 32289 → 17665 |

These suites hold many probes, so they do not describe a minimal application's
per-handler cost. Checking remains superlinear in nested handlers; see
[compile latency](../roadmap-compile-latency.md#cl6-row-solving-under-nested-handlers).
Any measured conclusion is specific to the recorded compiler, host, and workload.
