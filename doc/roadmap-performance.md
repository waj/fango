# Roadmap: generated-code performance

This document owns the next round of performance work found while closing the
typed JSON gap. The measured motivation is in the
[JSON baselines](design/json-performance-baselines.md#ascii-fast-paths-and-single-pass-spans);
the main [roadmap](roadmap.md#json-and-generated-code-performance) links here.
When a milestone lands, its durable contracts move to [design](design.md) and
[reference](reference.md) and its section is removed.

Every milestone keeps semantics unchanged and passes `make ci`. Each is timed
with alternating fresh-process pairs of the previous commit's binaries on the
10 MB typed JSON fixture, both backends, as in the baselines. A change that
helps one backend and costs the other states both numbers in its commit and
in the baselines.

## P5 — static-argument specialization

**Problem.** Recursive functions that receive an unchanged function argument,
such as `scanListShort scanValue` with the element decoder or a predicate
passed through a scanning loop, call it indirectly on every iteration.

**Change.** When every self-call of a worker passes a parameter of function
type unchanged, and a call site supplies a known worker or a closed lambda,
clone the worker for that argument (owner-local, named like the scalar
variants) and redirect the call. The clone's body then calls the argument
directly, and P4 can inline it. Specialization is bounded per worker, so
distinct arguments cannot multiply clones without limit.

**Acceptance.** Fixtures for a specialized fold and a loop with an unchanged
predicate, a recursive call that changes the argument (not specialized), and
the clone bound.

**Status: deferred on measurement.** The motivating calls do not fit this
change. The bundled folds and maps are effect-polymorphic (`List.foldl`,
`List.map`, and `filterHelp` carry a row variable), so a clone would have to
specialize the row and its evidence too. JSON's `scanListShort` receives the
element decoder as a method of a class dictionary, not as a known worker, so
it needs instance specialization instead. On the typed JSON fixture that
indirect call runs about once per list element, roughly a hundred thousand
times per decode, which bounds its cost well under one percent. Revisit with
row-polymorphic clones or dictionary specialization when a workload shows the
indirect calls.
