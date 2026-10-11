# Roadmap: test framework

[Expect](../stdlib/Expect.fango) and [Test](../stdlib/Test.fango) are
implemented: the modules document their API and report, and the
[design](design/testing.md) records why expectations abort, why the tree is
row-indexed, and how `Inspect` renders operands. This document owns what
remains: failure source positions, a `fango test` command, and fuzz tests.
IO-free execution and pluggable reporters are
[implemented](design/testing.md#the-report). The main
[roadmap](roadmap.md#test-framework) links here.

## Failure source positions

A failing expectation should name its `file:line`; the test name locates the
case but not which of several expectations failed. The mechanism is the
[stack trace](roadmap.md#stack-traces) facility: `Expect.Error` gains
`location : Maybe Trace.Frame`, the failing helpers take the first captured
frame whose module is not `Expect`, and the report prints `file:line` above
the message. The report is standard output compared under both backends, so
this waits for interpreter parity: compiled frames report the line of the
outermost call on a Go line, the interpreter each call's own origin, which
agree when the expectation call is the statement, the tail expression, or a
lambda body.

A compiler-solved call-site constraint after GHC's `HasCallStack` was designed
and rejected: it would leak into signatures and interfaces, and the stack
facility serves crashes and logging as well as tests. Helper authors who want
a failure attributed to their caller may later get an explicit opt-in, as
Go's `t.Helper()` is.

## Milestones

1. **M1 — library.** DONE: `stdlib/Expect.fango`, `stdlib/Test.fango`, the
   runner, the report, and the `test_*` fixtures under `testdata/run/` and
   `testdata/suite/`.
2. **M2 — dogfooding.** DONE: the `Dict` fold and update fixtures and the
   `String` pad/split/trim fixture are `Test` suites. Call-order checks that
   printed from inside callbacks became `State` accumulators asserted with
   `Expect.equal`; no expectation beyond the first version was needed.
3. **M3 — positions.** The trace-based location, the `Error` location, and
   the report line, as above.

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
