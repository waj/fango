# Roadmap: generated-code performance

This document owns the next round of performance work found while closing the
typed JSON gap. The measured motivation is in the
[JSON history](design/json-performance.md#ascii-fast-paths-and-single-pass-spans);
the main [roadmap](roadmap.md#json-and-generated-code-performance) links here.
When a milestone lands, its durable contracts move to [design](design.md) and
[reference](reference.md) and its section is removed.

Every milestone keeps semantics unchanged and passes `make ci`. Each is timed
with alternating fresh-process pairs of the previous commit's binaries on the
10 MB typed JSON fixture, as in the [JSON history](design/json-performance.md#history),
and replaces the [current results](design/json-performance.md#current-results)
when it moves them.

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

## P6 — constant-offset row evidence

**Problem.** A callback that escapes the worker it is passed to, or is stored in
a value, [looks its evidence up](design/effect-performance.md#recorded-compiled-results)
at invocation. The row is a chain searched from its newest binding, so an effect
that sorts first costs 8 ns at three handlers and 16 ns at eight, and the cost
follows an effect's name. Callbacks that only flow through forwarding workers no
longer look anything up, which leaves this path for a narrower set of programs.

**Change.** Represent a row as one canonically ordered vector, as Koka does, so
that a use whose row is closed indexes it at a constant offset. For an open row,
pass the offset as a hidden argument where the label's position is fixed, the
TREX-style approach "Effect Handlers, Evidently" proposes, or verify a static
position hint before falling back to a search. Extension copies a vector, so the
allocation-free inline case must keep its measured benefit.

**Acceptance.** The `escaping` cases of the
[handler comparison](design/verification.md#effect-handler-comparison) stop
depending on row position, with no new allocation on row extension and no
regression in the forwarding cases.

**Status: deferred on measurement.** Measure on a program whose hot callbacks
escape first. List traversal does not need it, since `List.foldl` and `List.map`
forward their rows.
