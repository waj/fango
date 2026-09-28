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
    Runtime.Scope.bracket { open label } close action

main() =
    text = withResource "input" { resource -> readAll resource }
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

Acquisition, body, and release are synchronous effectful calls. Failed
acquisition is responsible for its own partial cleanup. A release that never
completes prevents its scope from completing. Cleanup guarantees cover normal
returns and language aborts; they do not turn arbitrary native panics into
language failures.

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
If successful completion is followed by failed cleanup,
the first cleanup failure becomes primary and later failures remain secondary.

`finally action cleanup` is `bracket` without a resource. Because its resource
is `()`, it places no restriction on the result it returns.

## Resource lifetimes

A resource or a closure referring to it may outlive its acquiring scope. This
does not keep the resource open: bracket release still runs at scope exit.
Native resource operations validate the handle and report use after close.
The compiler does not infer resource retention or non-escape contracts.

## Resource types

Libraries mark opaque resource types with a declaration pragma:

```fango
module Connection exposing (Handle, withConnection)

import Runtime.Native

{-# resource #-}
type Handle = Handle Runtime.Native.Any

withConnection address use =
    Runtime.Scope.bracket { openConnection address } closeConnection use
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

## Wrappers and staging

`Runtime.Scope.bracket` is an intrinsic for cleanup. Ordinary wrappers need no
compiler registration. Its callbacks use ordinary row inclusion: acquisition
and release may use IO while the body also fails. Partial applications have the
same effect compatibility.

A scope whose callbacks are stage-safe may run at compile time. Native resource
operations and system entropy remain forbidden there because they are effects.
