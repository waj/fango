# Test framework

Architecture of the bundled [Expect](../../stdlib/Expect.fango) and
[Test](../../stdlib/Test.fango) modules. The modules document their own API,
report format, and exit status; the [testing roadmap](../roadmap-testing.md)
owns failure positions, a test command, fuzzing, and reporters.

[Design index](../design.md).

## Expectations abort

A failing expectation performs the abort-only `Expect` effect instead of
returning an expectation value, which is where the framework departs from
elm-test:

- A test body is a statement block; the first failing expectation ends it, so
  there is no `Expect.all` and no expectation combinators.
- `ok`, `err`, and `just` return the value they unwrap, because they never
  return on the other constructor.
- `onFail` is a handler that re-raises from its abort clause with a context
  line prepended; an abort raised by an abort clause propagates outward, as
  [handler clauses](../reference/effects.md#handler-clauses-and-abort-routing)
  specify.
- Asserting that code fails means handling the code's own effect inside the
  body, with `Fail.attempt` say, and asserting on the `Result`.

`Expect` is its own effect rather than an application of `Fail`, so a body's
own `Fail` applications stay distinct from expectation failures and no
effect-row alias is needed.

## The tree is row-indexed

`Test e` carries the row its bodies perform besides `Expect`, so `Test {}`
holds pure tests, `Test IO` tests that read files, and a caller can handle its
own effect around the whole run, as `State.run 0 { Test.run suite }` does in
the fixtures. `run : Test e ->{IO | e} ()` installs the `Expect` handler
around each body and leaves `e` to the caller. The constructors are private,
so fuzz cases or richer `only` semantics can be added without touching user
code; a fuzz test is a case whose body loops under `Random.runSeeded`.

## Inspect

Failure messages render operands with `Inspect`, a class with two blanket
instances: `Show a => Inspect a` shows the value and the unconditional
instance writes `<value>`. Requiring `Show` was rejected because it would
force `deriving (Show)` onto every tested type. `Display` was rejected as the
delegate because it has no fallback for types without `Show`, and `display`
does not quote strings, so `"1"` and `1` would print the same. Blanket
instances do not match function types, which is why the module's example uses
a handle for the fallback.

## The report

The report goes to standard output and the status is set with
`Process.exit 1` unless the run passed and was complete, because the
[differential harness](verification.md#differential-fixtures-and-examples)
compares standard output and the exit status and never standard error. A
suite containing `only`, `skip`, or `todo` is incomplete and exits 1, as in
elm-test, so a placeholder cannot make a build green. Problems with a group
(no tests, an empty name, a test with an empty name, duplicate names) are
failures of that group and do not stop its tests. A runtime crash inside a
body ends the whole run; there is no Fango-level catch. The reporter is not
pluggable; the roadmap sketches a reporter effect for that.
