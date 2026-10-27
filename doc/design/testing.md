# Test framework

Architecture of the bundled [Expect](../../stdlib/Expect.fango),
[Test](../../stdlib/Test.fango), and [Test.Reporter](../../stdlib/Test/Reporter.fango)
modules. Their source comments document the API, report formats, JSON schema,
and exit status; the [testing roadmap](../roadmap-testing.md)
owns a test command and fuzzing.

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
holds pure tests, `Test File.FileSystem` tests that use whole-file services, and
a caller can handle its own effect around the whole run. `execute : Test e ->{Reporter | e} Summary` installs the
`Expect` handler around each body and leaves `e` to the caller. Handling
`Reporter` removes reporting from the row, so a pure suite needs no IO.
The constructors are private, so fuzz cases or richer `only` semantics can
be added without touching user code; a fuzz test is a case whose body loops
under `Random.runSeeded`.

## Failure positions

`Expect.Error` carries the frame of the failing expectation: the innermost
entry of the [call stack](repl.md#call-stack) outside the `Expect` module, so
a test body reports its own line and a helper defined elsewhere reports the
line inside the helper. Reading the stack keeps the expectations ordinary
functions with no hidden argument, and both backends produce the same frame,
since the compiled line is the outermost call on a Go line and the
interpreter records each call's origin, which coincide where an expectation
is a statement, a tail expression, or a lambda body. A compiler-solved
call-site constraint in the style of GHC's `HasCallStack` was designed and
rejected: it would have leaked into signatures and module interfaces, and the
stack serves crashes and logging as well. `onFail` keeps the inner failure's
frame while it prepends context. The console report prints `file:line` above
the message and the JSON report carries it as `location`; group failures
have none.

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

Execution emits synchronous `Reporter` events in depth-first order and returns
a summary. The runner owns counting and verdicts; handlers choose output.
Suite start/end events bracket the run, active bodies have start/end events,
and skipped leaves and todos have only end events. Paths carry unformatted
names from outermost to innermost. Group validation problems are separate
failure events before the group's children. Explicitly skipped subtrees emit
leaf outcomes without validating groups or running bodies. Unhandled effects
and runtime crashes propagate without a synthetic suite completion.

`Test` owns the private tree and execution. `Test.Reporter` owns the effect,
event and summary types, handlers, and command-line output selection. `Test`
re-exports those types and handlers, preserving the `Test.Reporter` effect's
nominal identity across both modules.

`silent` discards events and returns the result. `console` prints failures
and the summary to standard output without exiting. `progress` prints dots,
and `documentation` prints group headings and indented case outcomes; both
retain failures until suite completion to print their full details afterward.
`run` delegates command-line handling to `Test.Reporter.run` around `execute`,
exposing `Console` for output, `Process.Environment` for options, and
`File.FileSystem` for optional JSON reports. It answers a `Process.Status` for
`main` to return rather than exiting, so a suite needs no IO of its own and
cleanup runs before the status applies. No arguments select the existing
console report. An optional JSON report collects completed case outcomes and
group failures and writes them after execution, before the status is chosen.
JSON collection is enabled only when requested; argument parsing happens before
execution. The module's source comments own the options, diagnostics, and
versioned JSON schema.

The status is set with `Process.exit Process.failure` unless the run passed and was complete,
because the [differential harness](verification.md#differential-fixtures-and-examples)
compares standard output and the exit status and never standard error. A
suite containing `only`, skipped leaves, or `todo` is incomplete and exits 1,
as in elm-test, so a placeholder cannot make a build green. Problems with a group
(no tests, an empty name, a test with an empty name, duplicate names) are
failures of that group and do not stop its tests. A runtime crash inside a
body ends the whole run; there is no Fango-level catch. Custom resumptive
handlers can collect or render the same events without changing the tree or
execution policy.
