# Roadmap: compile latency

This document owns the cost of a cold build: what the compiler spends between
parsing and handing generated Go to the toolchain, on a program whose modules
are not yet in the [compilation cache](design/backend.md#module-emission-and-build-cache).
Warm builds, precompiled library artifacts, and the Go toolchain's own time are
out of scope: [modules and distribution](roadmap-modules.md#distributing-the-bundled-sources)
owns shipping prebuilt objects, and the [cache section](roadmap.md#compilation-cache)
owns what the cache itself still lacks. The main
[roadmap](roadmap.md#capture-flow-analysis-cost) summarizes priority.

Stage IDs and titles follow the [repository milestone rules](../AGENTS.md).
Promote completed stages into [design](design.md) and remove them or mark them
`DONE` here; the measurements below describe the state that motivated the
work, not a record to be kept current.

## Where a cold build spends its time

Measured on one machine at the commit that introduced this document, a cold
build of a program that does nothing but `import Async` takes about three
seconds for some twenty modules and two thousand lines of library source. The
Go toolchain is a few hundred milliseconds of that, once its own cache is warm
a little over a hundred. The rest is the compiler, and roughly seven eighths of
the compiler's time is the
[capture-flow analysis](design/ownership.md#capture-flow-graph-and-abstract-heap):
`core.checkCaptureFlows` and `core.CollectExecutionNeeds`. Nearly all of that
is the `Async` module. Inference, elaboration, lowering, and emission proper
are small by comparison. `fango build -vv` now gives the analysis its own
[row and counts](reference/commands.md#build-progress-and-statistics), carved
out of the stages it runs under.

The cost is not one slow pass. The same module is interpreted, root by root,
four times over the batch pipeline:

1. **Inference.** `generator.executionNeeds` interprets each completion
   group's roots against every installed contract to collect work-budget and
   coroutine-control constraints before generalization
   ([inference](design/inference.md#types-rows-and-annotations)). The
   constraint solver's fixed point runs it again after the collected needs add
   constraints, and the second run almost always collects the same set. A
   chain of thin wrappers — `run`, `runOn`, `runWithCapacity`, `runBounded`
   — re-interprets the whole scheduler closure beneath it once per root.
2. **Elaboration.** `elaborate.Increment` discharges the module's obligations
   over its elaborated Core. This is the discharge the design requires.
3. **Stage snapshot.** The staging evaluator elaborates the same declarations
   a second time for the module's
   [declarative stage Core](design/metaprogramming.md#evaluator-and-completion-order),
   group by group, and each group's elaboration discharges its obligations
   again over Core that differs from the runtime Core only by scalar
   specialization.
4. **Lowering.** `machine.LowerUnit` lints the owner's Core as Machine input,
   and that lint discharges every obligation a third time. The
   [design](design/ownership.md#evidence-and-independent-reconstruction)
   already says a caller that has just discharged these definitions may say
   so and have lint only compare what it reconstructs; the install-time lint
   says so, the lowering-time lint does not.

Underneath, one cold build allocates several gigabytes across tens of millions
of objects, and two functions account for two thirds of the bytes: `joinFlow`,
which appends, sorts, and compacts on every join, and `maps.Clone`, mostly the
`let` case of `eval` copying the whole value environment for every local
binding, so a function's cost grows with the square of its bindings. The
garbage collector is not the lever — raising `GOGC` recovers about a tenth —
the volume is. The command disables the Go runtime's heap-profile sampling,
as the Go compiler does, because the checker's recursion makes each sampled
stack long; profiled through a test harness it looked like close to a tenth
of the build, but measured on the command itself it is within noise.

A prototype of the first three stages below, each behind a toggle, brought the
in-process build from about 2.7s to about 1.1s and the wall-clock build from
about 3.2s to about 1.4s, with allocation reduced by more than half. What
remains after them is, in order: the inference-time interpretation, the one
required discharge, writing the cache, the Go toolchain, and the per-object
`fsync` of the checked store.

## Goals and boundaries

The compiler's share of a cold build for a small program over the async stack
should be a fraction of a second. Nothing proven changes: the same
obligations are discharged over the same definitions at least once, lint still
reconstructs every contract and compares it, and every diagnostic keeps its
text and its root. Any stage here holds the differential suite, the
capture-flow diagnostic fixtures, and the [latency
baselines](design/verification.md), re-recorded on an idle machine when a
stage is meant to move them.

Not goals: reusing analysis across machines or builds, which is the cache's
concern; changing what the analysis proves or how precisely it folds
recursion; and the Go toolchain, whose floor is fixed by going through `go
build` at all.

## Stages

### CL1 Allocation in joins and environments

DONE. Joins merge linearly, the command disables heap-profile sampling, and
the flow checker's value environment is a
[chain of layers](design/ownership.md#capture-flow-graph-and-abstract-heap)
with flat snapshots where fingerprints read it.

### CL2 One discharge per module in the batch pipeline

The design's rule that reconstruction is unconditional and re-discharge is not
is applied to the two places that still re-discharge.

- **Lowering.** The unit program the backend hands to `machine.LowerUnit`
  states `CaptureFlowsProven` when the owner's Core was checked in this
  session, on the same slice `Increment` discharged, or decoded from a
  verified object that was published after that discharge. Nothing rewrites
  Core between install and lowering — `AssembleModuleProgram` performs entry
  validation only — so the argument is the one `installer.go` already makes
  for the install-time lint. Lint still reconstructs every contract and
  summary and discharges on any disagreement.
- **Stage Core.** The stage elaboration of a completion group skips the
  discharge for roots `Increment` has already proven, and discharges the
  rest: compile-time-only declarations exist only in stage Core and get their
  one discharge there. Both elaborations run the same `decl`; the runtime
  Core additionally receives scalar specialization, which erases values the
  analysis already treats as scalars. The discharge is per root from a fresh
  checker, so skipping a root is exactly "do not re-discharge this
  definition", and summaries and contracts are still solved for every
  definition because later splices and lint read them.

The [ownership design](design/ownership.md#evidence-and-independent-reconstruction)
changes its last sentence — the whole-program path is no longer the only one
that states nothing — and the [pipeline](design/pipeline.md#pipeline) and
[metaprogramming](design/metaprogramming.md#evaluator-and-completion-order)
entries name which pass owns a definition's discharge. Once
[lazy stage Core](#open-decisions) is decided, the stage half of this stage
may be subsumed by it.

### CL3 Execution needs across solve iterations

The interpreted input of `CollectExecutionNeeds` is a deterministic function
of the roots' definitions as built under the current substitution and the
installed contracts. The generator fingerprints that input and returns the
previous iteration's needs when it has not changed. The prototype's upper
bound, reusing the first iteration's needs unconditionally, is what a correct
memo can save; a correct memo must key on the built definitions, not on the
iteration count, because a substitution that changed a root's type can change
what its body interprets to.

### CL4 Cache writes

The checked store syncs every module object to disk as it is published, and
those syncs are a noticeable share of what remains after the stages above.
Whether to sync once per build, or to leave durability to the rename and
accept a torn object as a cache miss, is a cache-integrity question the
[artifact framing](design/backend.md#module-emission-and-build-cache) may
already answer; this stage decides it and measures the result.

### CL5 Sharing interpretation across roots

Every definition is its own root, from a checker that shares nothing with the
others, so a helper reached from many definitions is interpreted once per root
that reaches it. In the `Async` module the contexts one discharge creates are
spread over about a hundred distinct callables, and the most-visited of them
are re-interpreted from a dozen different roots. A module of mutually
recursive functions over a recursive polymorphic type — `Dict` is the standard
library's example — pays the same multiplier.

This is a redesign rather than an optimization: object identities, owners, and
allocation ancestry are relative to the root being checked, and a diagnostic
names the root it was found from. What is wanted is a per-callable summary
strong enough that a second root can reuse it without reinterpreting the
callee, which is the question the
[contract](design/ownership.md#capture-flow-graph-and-abstract-heap) already
answers for dependency modules and does not answer within a module. Two
shapes are candidates: one checker per module, treating the module as a
program with one synthetic root that calls every definition with unknown
arguments, so that the existing sharing key folds identical invocations across
roots; or a summary attached to a definition's contract, valid for
all-unknown inputs, that a call instantiates instead of interpreting the body.
The first keeps the current invariants but changes which root a diagnostic
names; the second is the same compositional question the design declines for
callbacks. Either must keep obligations replayed at every cached return, as
today. This stage begins with a design and a measurement of how many contexts
the sharing key would actually fold.

### Dependencies

| Stage | Depends on | Documentation |
| --- | --- | --- |
| CL1 | — | ownership (environment representation) |
| CL2 | — | ownership, pipeline, metaprogramming |
| CL3 | — | inference |
| CL4 | — | backend (cache) |
| CL5 | CL1, CL2, measurement | ownership; the [roadmap entry](roadmap.md#capture-flow-analysis-cost) |

## Open decisions

- **Lazy stage Core.** The staging evaluator elaborates every completed group
  of every module, but a splice executes only the closure it reaches. Building
  stage Core on demand from the retained `DeclInfo` — and publishing the
  section only when it was built — would remove the second elaboration
  entirely, not just its discharge, and answers the cache roadmap's "shrink
  stage Core" item. What it costs is that a consumer module that splices into
  a dependency for the first time elaborates that dependency's closure itself.
- **Profiling entry point.** The command has no way to write a CPU profile;
  this work was profiled through a throwaway test. Whether `fango build`
  should accept a profile flag, or the benchmark package should own a
  profiling harness, is a tooling question.
