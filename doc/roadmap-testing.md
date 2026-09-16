# Roadmap: test framework

This document owns the design of a test framework written in fango: a bundled
`Expect` module for expectations, a bundled `Test` module for organizing and
running them, and the language work the two need. Nothing here is implemented
yet; when a milestone lands, its durable semantics move to [the
design](design.md) and [the reference](reference.md) and the section is
removed here. The main [roadmap](roadmap.md#test-framework) links here.

The framework is inspired by elm-test but follows fango's idioms rather than
Elm's: a test body is a statement-style block, an expectation that fails
*aborts* through an effect instead of returning an `Expectation` value, and
the places elm-test uses callbacks or combinators — `onFail`, `all`,
reporters — are handlers or plain sequencing here.

All APIs in this document are proposed. Snippets use existing Fango syntax
except for the explicitly marked call-site constraint. Shared call syntax and
callback inclusion are implemented in the language; see the
[reference](reference/effects.md).

## Decisions

These were settled when the design was drawn up and are not open:

- **Library first, command later.** The first versions ship `Expect`,
  `Test`, and `Test.run`; a program tests itself with
  `main() = Test.run suite` and `fango run`. A `fango test` command with
  Elm-style discovery is sketched under [Later](#later) and designed when the
  library has settled.
- **The tree is indexed by an effect row.** `Test e` carries the row its
  bodies may perform besides `Expect`. The alternative — a monomorphic `Test`
  whose bodies are `() ->{Expect, IO} ()` — has simpler signatures but fixes
  the effects a test may perform; the row-indexed form lets a caller handle
  its own effect around the whole run.
- **Shared call syntax.** Use the trailing final lambda `test "name" \_ ->`
  and ordinary `(|>)` / `(<|)` already supplied by the language. Unit callbacks retain
  `\_ ->`; no zero-pattern lambda or `do` keyword is planned here.
- **First-version scope** is `describe`, `test`, `skip`, `todo`, `only`, the
  expectations below, a console report, and failure source positions. Fuzz
  testing is deferred, and the tree is shaped so it can be added without a
  breaking change.

## Existing prerequisites

Use [callback inclusion](reference/effects.md#row-inclusion-and-callback-compatibility),
[row-kinded ADTs](reference/functions.md#row-kinded-parameters), and
[blanket instances](reference/classes.md#instance-heads-and-blanket-instances).
Adding bundled Expect/Test reserves those module names. Reports use stdout because
there is no stderr API; IO.exit supplies status. Existing differential fixtures
can pin output and failure status without a new harness.

## Modules

Two bundled modules, carrying `{-# no-prelude #-}` and explicit `Basics`
imports like the rest of `stdlib/`, with every function annotated:

- `Expect` — the `Expect` effect, the `Inspect` display class, and the
  expectation functions. The module and the effect share a name, following
  `IO`.
- `Test` — the tree, its constructors, and the runner.

## The test tree

```fango
type Test e
    = Describe String (List (Test e))
    | Case String (() ->{Expect | e} ())
    | Skip (Test e)
    | Only (Test e)
    | Todo String

describe : String -> List (Test e) -> Test e
test : String -> (() ->{Expect | e} ()) -> Test e
skip : Test e -> Test e
only : Test e -> Test e
todo : String -> Test e
```

`Test` is exported abstractly, so constructors can be added or reshaped —
fuzz cases, richer `only` semantics — without touching user code. A fuzz test
later is a `Case` whose body loops under `Random.runSeeded`, so the shape
already accommodates it.

The proposed canonical shape, with several
expectations sequenced as statements:

```fango
suite : Test IO
suite =
    describe "String.split"
        [ test "splits on the separator" \_ ->
            Expect.equal [ "a", "b" ] (String.split "," "a,b")
            Expect.equal [ "" ] (String.split "," "")
        , test "reads the fixture" \_ ->
            text = Expect.ok (File.read "fixture.txt")
            Expect.equal 3 (List.length (String.split "\n" text))
        , skip (test "unicode separators" \_ -> Expect.failWith "later")
        , todo "empty separator"
        ]

main() = Test.run suite
```

## Expectations

```fango
effect Expect
    abort fail : Failure -> a

type Failure = { message : String }

class Inspect a
    inspect : a -> String

instance Inspect a
    inspect _ = "<value>"

instance Show a => Inspect a
    inspect value = show value

equal : (Eq a, Inspect a) => a -> a ->{Expect} ()
notEqual : (Eq a, Inspect a) => a -> a ->{Expect} ()
lessThan : (Ord a, Inspect a) => a -> a ->{Expect} ()
greaterThan : (Ord a, Inspect a) => a -> a ->{Expect} ()
atMost : (Ord a, Inspect a) => a -> a ->{Expect} ()
atLeast : (Ord a, Inspect a) => a -> a ->{Expect} ()
ok : Inspect e => Result e a ->{Expect} a
err : Inspect a => Result e a ->{Expect} e
just : Maybe a ->{Expect} a
nothing : Inspect a => Maybe a ->{Expect} ()
failWith : String ->{Expect} a
onFail : String -> (() ->{Expect | e} a) ->{Expect | e} a
```

- Argument order is Elm's, expected first, so `actual |> Expect.equal expected`
  reads correctly and `List.length xs |> Expect.lessThan 10` means "less than
  ten".
- Because a failure aborts, `ok`, `err`, and `just` *return the unwrapped
  value*, which Elm's value-returning expectations cannot:
  `n = Expect.ok (parse "12")` followed by `Expect.equal 12 n`. `Expect.all`
  has no counterpart; sequence statements.
- `Inspect` is why `equal` does not require `Show`: a type without an instance
  still compares, and the message says `<value>` where it cannot show the
  operands. Requiring `Show a` was rejected because it would force
  `deriving (Show)` onto every tested type.
- `onFail` is a handler that catches the failure and re-raises it with the
  prefix prepended; it replaces Elm's `Expect.onFail` and doubles as the
  reference's example of an abort re-raised from an abort clause.
- Asserting that code fails means handling the code's own effect inside the
  body and asserting on the `Result` it produces.
- Deferred until an example asks: `within` for floats, `isTrue`/`isFalse`
  (`equal True` covers them), and collection-specific helpers.

## Runner and report

`run : Test e ->{IO | e} ()` walks the tree, installs an `Expect` handler
around each body, prints a report, and calls `IO.exit 1` unless the run both
passed and was complete. Output is ASCII and goes to stdout. Following
elm-test, only failures are printed in full, followed by a summary:

```text
FAIL  String.split > splits on the separator

    Expected: ["a", "b"]
    Actual:   ["a"]

TEST RUN FAILED

Passed:  3
Failed:  1
Skipped: 1
Todo:    1
```

Semantics:

- When the tree contains `only`, just the focused subtrees run. A run
  containing `only`, `skip`, or `todo` reports `TEST RUN INCOMPLETE` and exits
  1, as elm-test does, so a placeholder cannot make CI green.
- Duplicate case names within one `describe`, an empty `describe`, and an
  empty name are reported as failures of that group.
- A runtime crash inside a body ends the whole run; there is no fango-level
  catch. This is a limitation to record in the reference.
- The reporter is not pluggable in the first version. The earlier sketch's
  reporter effect — the runner performing `suiteStart`, `caseEnd`, and so on,
  with the console reporter as one handler — is the way to add one later
  without changing the tree.

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
`Failure` gains `location : Maybe Location`, and the report prints it above
the message.

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

Shared syntax and row subsumption are implemented; this document owns the
remaining framework work.

1. **M1 — library.** `stdlib/Expect.fango`, `stdlib/Test.fango`, the runner,
   and the report format. Fixtures under `testdata/run/`: a passing run, a
   failing run and its exit code, an incomplete run for each of `only`,
   `skip`, and `todo`, `onFail`, `ok`/`just` unwrapping, the `Inspect`
   fallback, and an effectful body with its own handler. A reference section
   describing the surface, and a formatter-clean stdlib.
2. **M2 — dogfooding.** Rewrite one or two existing stdlib fixtures, `Dict` or
   `String` say, as `Test` suites to find API gaps; each stays an ordinary
   differential fixture.
3. **M3 — positions.** The `Located` constraint, `Meta.location`, the
   `Failure` location, and the report line.

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
  reported on failure reproduces the run under `Random.runSeeded`.
- **Pluggable reporters** through the reporter effect described above, for a
  machine-readable format or a quieter CI mode.
