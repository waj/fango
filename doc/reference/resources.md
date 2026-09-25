# Cleanup scopes and resources

Scope acquisition/release ordering, failure precedence, and resource lifetimes.

[Reference index](../reference.md).

A handler releases a resource only for the failure it handles. Cleanup that
must also run when an arbitrary residual effect leaves the region needs a
scope, which the bundled `Runtime.Scope` module provides through ordinary function
application — there is no `try`, `catch`, `finally`, `using`, or `defer`
syntax:

```fango
module Runtime.Scope exposing (bracket, finally)

bracket : (() ->{e} resource) -> (resource ->{e} ())
       -> (resource ->{e} result) ->{e} result

finally : (() ->{e} result) -> (() ->{e} ()) ->{e} result
```

```fango
withResource label action =
    Runtime.Scope.bracket (\_ -> open label) close action

main() =
    text = withResource "input" (\resource -> readAll resource)
    print text
```

`bracket acquire release use` evaluates `acquire()` once. If that fails,
nothing is released. Otherwise `use` runs on the acquired resource and
`release` runs exactly once when the scope exits:

| Event | Behavior |
| --- | --- |
| Acquisition fails | Propagate the failure; nothing is released |
| Body returns normally | Release, then produce the body value |
| Failure caught within the body | Continue the body; release at the real scope exit |
| Exit targets an outer handler | Release before the outer clause runs |
| Failure in a handler's `return` clause | Release before that failure propagates |
| Nested scopes exit | Release in reverse acquisition order |

So a scope inside a handler releases before the handler's abort clause sees
the failure:

```text
open -> body -> fail requested -> close -> outer fail clause
```

while a scope around a handler releases after that clause has produced the
handler's answer. The two nestings are different programs; neither ordering is
applied to the other.

A release runs with the evidence where it was written, not with whatever
handlers happened to be installed where the body exited, and may itself use
nested handlers.

Acquisition may suspend. The release obligation is registered only after
acquisition succeeds; a failed acquisition is responsible for its own partial
cleanup. Release may also suspend. The owner remains in its closing state,
retaining the resource and definition-site handlers until the driver resumes
the release. An inner release finishes before an outer release starts, even
when either pauses. Abandoning an unfinished coroutine starts the same drain;
use `Runtime.Coroutine.stop` or `Runtime.Work.stop` to receive cleanup requests
and `advance` to reply to them. A release that never completes prevents its
owner or enclosing context from completing.

## Cleanup failures

When a release fails, the failure the body was already carrying stays primary:

| Body | Release | Result |
| --- | --- | --- |
| Succeeds | Succeeds | The body value |
| Succeeds | Fails | The release failure |
| Fails | Succeeds | The original failure |
| Fails | Fails | The original failure, with the release failure recorded alongside it |

A recorded release failure is kept in the exit in deterministic inner-to-outer
order. `Fail.attemptReport` exposes these failures as typed-inspectable snapshots.
If successful completion or an early stream stop is followed by failed cleanup,
the first cleanup failure becomes primary and later failures remain secondary.

`finally action cleanup` is `bracket` without a resource. Because its resource
is `()`, it places no restriction on the result it returns.

## Resource escape checks

A scope rejects a result that retains its resource with `RESOURCE ESCAPES`.
This includes a resource hidden in an ADT or captured by a returned function.
An unrelated function may be returned when its inferred contract proves that
it does not capture the resource. Scalars and transitively capture-free data
remain valid results.

## Resource types

Libraries mark opaque resource types with a declaration pragma:

```fango
module Connection exposing (Handle, withConnection)

import Runtime.Native

{-# resource #-}
type Handle = Handle Runtime.Native.Any

withConnection address use =
    Runtime.Scope.bracket (\_ -> openConnection address) closeConnection use
```

Here `openConnection` and `closeConnection` are private library functions.
`{-# resource #-}` must precede exactly one type declaration, allowing comments
and whitespace between them. It supports unions, records, and parameterized
types. It takes no arguments, cannot be repeated for one declaration, and is
not a file-header directive. `resource` remains an ordinary identifier outside
the pragma. The declaration works at the REPL as well.

A resource type carries a capability independently of its representation.
Bundled native resources wrap `Runtime.Native.Any`, so their Go object is held directly
without an integer handle table.
Export it as `Handle`; exporting its representation with `Handle(..)` or
`exposing (..)` reports `RESOURCE REPRESENTATION EXPOSED`. Importers cannot
inspect its constructors, record fields, or reflected schema. Native code and
the defining module remain responsible for resource representation and native
correctness. The marker alone does not acquire or release resources.

## Wrapper contracts

Wrappers and helpers infer and export capture and retention contracts without
compiler registration or written lifetime annotations. A helper may borrow a
resource synchronously. Returning it, retaining it through an indirect callback,
or storing it in an outer handler reports `RESOURCE ESCAPES`, even when the
enclosing result is `()`. A proven non-retaining outer resumptive handler may
use it synchronously. Passing it as an abort payload across its cleanup boundary
is rejected because the abort clause runs after release. Release callbacks obey
the same retention checks. Diagnostics identify the owning scope and the value
or destination that would outlive it.

Contracts are conservative at recursive joins where distinct dynamic owners
cannot be proved identical. Cursor advancement additionally carries exclusive
access obligations. Written capture contracts are not implemented.

`Runtime.Scope.bracket` remains a compiler intrinsic for cleanup and lifetime handling.
Its callbacks use the ordinary argument-inclusion rule: acquisition and release
may use IO while the body also fails. Partial applications and ordinary wrappers
have the same effect compatibility, subject to the existing resource restrictions.

A scope does whatever its callbacks do, so a program whose parts are all
stage-safe may run one at compile time. Resources and system entropy remain
forbidden there because they are effects, not because a scope is special.
