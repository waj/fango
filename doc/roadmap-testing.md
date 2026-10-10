# Roadmap: test framework

[Expect](../stdlib/Expect.fango) and [Test](../stdlib/Test.fango) are
implemented: the modules document their API and report, and the
[design](design/testing.md) records why expectations abort, why the tree is
row-indexed, and how `Inspect` renders operands. This document owns what
remains: failure source positions, a `fango test` command, fuzz tests, and
pluggable reporters. The main [roadmap](roadmap.md#test-framework) links
here.

## Failure source positions

A failing expectation should name its `file:line`; the test name locates the
case but not which of several expectations failed. The recommended mechanism
is a compiler-solved **call-site constraint**, after GHC's `HasCallStack`
(*proposed syntax*):

```fango
equal : (Eq a, Inspect a, Located) => a -> a ->{Expect} ()
```

`Located` is a nullary constraint. At a call site the compiler discharges it
with that site's position; when the caller itself carries `Located =>`, the
constraint propagates, so a user's helper reports *its* caller. The body reads
it with `Meta.location : Located => Location`. It rides the existing
evidence-passing ABI — the dictionary is a `Location` record — and both
backends see the same Core, so the invariant that the backends agree is kept.
`Expect.Error` gains `location : Maybe Location`, and the report prints it
above the message.

Nothing in the language supplies this yet. The parser requires exactly one
class parameter (`CLASS PARAMETER`, [`internal/parser/classes.go`](../internal/parser/classes.go)),
the checker's predicate always carries a type ([`internal/types/types.go`](../internal/types/types.go)),
and the compile-time stage has no splice that reads its own position:
`Meta.Site` is an attribute's attachment site, usable only by `Meta.failAt`.

Rejected alternatives:

- `//line` directives in generated Go read back through `runtime.Caller`:
  compiled-only, so the interpreter would report something different.
- An explicit `$(Meta.here)` argument at every expectation: correct, but the
  ergonomics defeat the purpose.
- Numbering expectations in the handler: a passing expectation performs no
  operation, and a resumptive counter cannot share an effect with the abort
  operation.

Open questions: the surface syntax for a nullary constraint, whether
`Located` and `Location` live in `Meta` or `Basics`, what a `Location`
holds (module, line, column), and how the constraint reads in an inferred
signature.

## Milestones

1. **M1 — library.** DONE: `stdlib/Expect.fango`, `stdlib/Test.fango`, the
   runner, the report, and the `test_*` fixtures under `testdata/run/`.
2. **M2 — dogfooding.** DONE: the `Dict` fold and update fixtures and the
   `String` pad/split/trim fixture are `Test` suites. Call-order checks that
   printed from inside callbacks became `State` accumulators asserted with
   `Expect.equal`; no expectation beyond the first version was needed.
3. **M3 — positions.** The `Located` constraint, `Meta.location`, the
   `Error` location, and the report line, as above.

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
- **Pluggable reporters.** A reporter effect — the runner performing
  `suiteStart`, `caseEnd`, and so on, with the console reporter as one
  handler — for a machine-readable format or a quieter CI mode, without
  changing the tree.
- **More expectations** when an example asks: `within` for floats,
  `isTrue`/`isFalse` (`equal True` covers them), and collection helpers.
