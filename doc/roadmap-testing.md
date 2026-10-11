# Roadmap: test framework

[Expect](../stdlib/Expect.fango) and [Test](../stdlib/Test.fango) are
implemented: the modules document their API and report, and the
[design](design/testing.md) records why expectations abort, why the tree is
row-indexed, how `Inspect` renders operands, and where failure positions come
from. This document owns what remains: a `fango test` command and fuzz tests.
IO-free execution and pluggable reporters are
[implemented](design/testing.md#the-report). The main
[roadmap](roadmap.md#test-framework) links here.

## Milestones

1. **M1 — library.** DONE: `stdlib/Expect.fango`, `stdlib/Test.fango`, the
   runner, the report, and the `test_*` fixtures under `testdata/run/` and
   `testdata/suite/`.
2. **M2 — dogfooding.** DONE: the `Dict` fold and update fixtures and the
   `String` pad/split/trim fixture are `Test` suites. Call-order checks that
   printed from inside callbacks became `State` accumulators asserted with
   `Expect.equal`; no expectation beyond the first version was needed.
3. **M3 — positions.** DONE: `Expect.Error` carries the failing frame from
   the call stack and the report prints `file:line`; see the
   [design](design/testing.md#failure-positions).

## Later

Directions the design leaves room for, none committed:

- **`fango test [paths]`.** Each `.fango` argument is a test module and each
  directory is searched for `*Test.fango`; the command type-checks the graph
  under a synthesized entry that imports them, collects every exposed value
  of type `Test`, synthesizes `main() = Test.run (describe ... [...])`, and
  runs it through the ordinary build pipeline. Its one compiler prerequisite is
  loading a graph whose entry is in-memory source over a given root, which is
  the `modules.Provider` seam the [tooling roadmap](roadmap-tooling.md)
  already wants for the language server. Tests stay in the same source root
  as the code they import, since there is only one root.
- **Fuzz tests.** `fuzz : Fuzzer a -> String -> (a ->{Expect | e} ()) -> Test e`
  over generators built on the `Random` effect, with shrinking. The seed
  reported on failure reproduces the run under `Random.runSeeded`. The
  private constructors of `Test` leave room for the case.
- **More expectations** when an example asks: `within` for floats,
  `isTrue`/`isFalse` (`equal True` covers them), and collection helpers.
