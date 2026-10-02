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

## P4 — a Core inliner

**Problem.** Small helpers are not inlined: Go's inliner rejects most
generated functions, and Clang's decision depends on lowered size. The
earlier fast paths were hand-written workarounds for this.

**Change.** An automatic inliner in elaboration, before
`LintProgIn`, so the interpreter executes the result and the differential
suite referees it. There is no inlining pragma; one is added only if a
measured case needs it.

- **Candidates.** Non-recursive workers that are effect-free: an empty
  evidence row and no effect parameters, no `Perform`, `Handle`,
  `ResumeTail`, `Bracket`, `ControlExit`, or task nodes, Direct control, and
  no residual row (extending the purity test in `elaborate/specialize.go`).
  Recursion is decided on a Core call graph, so mutually recursive groups and
  lifted locals are excluded.
- **Cost.** Node count after simplification, with a discount for parameters
  the body immediately scrutinizes, so a function whose wrappers vanish at a
  call site that matches its result is inlined even above the base size.
  Wrapper bodies that only forward to another call are always inlined.
- **Simplification.** Beta reduction to strict `Let`, substitution of
  duplicable bindings, and case of a known constructor, generalizing the
  simplifier that scalar specialization already has. Inlined bodies are
  alpha-renamed and receive fresh capture variables, scope IDs, and type
  argument substitution, keeping every lint invariant. ABI summaries are
  recomputed.
- **Within a module first,** then across modules. Cross-module inlining makes
  a dependent's checked object contain its dependency's code, so each module
  exposes an unfolding set (the bodies of its candidates) with its own
  fingerprint, and the checked-object key and emission record of a dependent
  include the unfolding fingerprints of the dependencies it inlined from.
  Changing an inlinable body then invalidates only its inliners. Update the
  build-cache design, which currently promises that dependency bodies are
  withheld.

**Acceptance.** Lint, differential, and LLVM suites pass with the inliner
enabled. `DisableOptimizations` also disables it. A cache test changes an
inlined function's body and verifies that the dependent is rebuilt.

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
