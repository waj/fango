# Native Go sidecars

Go sidecar declarations, supported ABI, host access, and worker lifecycle.

[Reference index](../reference.md).

A module may implement an annotated value or effect operation in adjacent Go:

```fango
module Hash exposing (crc32)

crc32 : String -> Int
crc32 = native
```

`Hash.native.go` must declare `package native` and export the corresponding
capitalized function. Imported dotted modules follow their source layout
(`Foo/Bar.fango` and `Foo/Bar.native.go`); a headerless entry uses its source
basename.

```go
package native

import "hash/crc32"

func Crc32(text string) int64 {
    return int64(crc32.ChecksumIEEE([]byte(text)))
}
```

## Boundary types

The supported boundary types are `Int`/`int64`, `Float`/`float64`,
`String`/`string`, `Char`/`rune`, `Bool`/`bool`, and Unit. String and Char
results are validated, and an invalid UTF-8 string or non-scalar rune panics at
the native boundary. Unit parameters are omitted from
the Go function and a Unit result is represented by no Go result.

The bundled [`Bytes`](library-bytes.md) also crosses, as a plain `[]byte`, in
compiler-bundled sidecars only; a user sidecar naming it is a `NATIVE ABI`
error. It is not validated on the way out the way String and Char are, because
`Bytes` has no well-formedness contract — that is the point of it. A native
must answer storage nothing will write again, never a view into a buffer it
reuses, because a `Bytes` never aliases what something else can change.

The bundled `Runtime.Native.Any` crosses as Go `any`. It is intended only as the private
field of a nominal wrapper owned by a library with a Go sidecar. Its constructor
is not exposed, and the type has no equality, display, pattern-matching, or wire
format. The interpreter evaluates Core beside the sidecars, so the Go object
stays on one heap rather than being encoded as an ID in a native table.

One kind of declared type also crosses: a type the same module declares with
exactly one constructor holding exactly one boundary value, such as
`type Token = Token Int`, may appear as a parameter or result. The Go function
sees the underlying value (`int64` here, or `any` for `Runtime.Native.Any`); the compiler projects the field on the way in
and rebuilds the constructor on the way out, in both backends. Keep the
constructor out of the module's exposing list and derive no `Show` or `Eq`,
and callers hold an opaque handle they can neither forge nor inspect — the
bundled `File.Handle`, `Net.Listener`, and `Net.Connection` use this with
`Runtime.Native.Any`, with `{-# resource #-}` adding their scoped capability contract.
The indexed storage forms below additionally admit opaque typed payloads.
Other direct functions, ADTs, records, polymorphic variables, class constraints,
Go type parameters, and multiple results are `NATIVE ABI` errors. A Go `error` result is likewise
rejected in user sidecars (`FALLIBLE NATIVE NOT ALLOWED`); the bundled `File`
and `Net` modules use their declared `IO.Error` and `Net.Error` results.
Effect rows on native value types
are preserved for checking and may contain `IO` or user-declared effects; the
sidecar call itself uses the same boundary ABI and does not receive a hidden
evidence argument. Sidecars may import only Go standard-library packages.
Every call-form declaration needs its matching exported function, and every
exported sidecar function needs a declaration.

## Indexed native storage

A wrapper may have phantom parameters when its sole field remains a boundary
value, for example `type Key a = Key Int`. Native calls preserve the indices;
a signature changing `Key a` to `Key b` is a `NATIVE STORAGE` error. Once used
as an indexed native wrapper, its constructor cannot be applied or matched in
Fango, even in its defining module (`NATIVE HANDLE REPRESENTATION`). This
prevents unpacking and rebuilding a handle at another index.

Representation-blind storage uses a resource wrapper over `Runtime.Native.Any`:

```fango
{-# resource #-}
type Box a = Box Runtime.Native.Any

box : a ->{IO} Box a
box = native
read : Box a ->{IO} a
read = native
write : Box a -> a ->{IO} ()
write = native
```

The Go functions take/return `any`. Payload arguments are opaque tokens;
sidecars may store and return them but must not inspect them, invoke them, or
substitute another representation. A payload of type Unit still crosses as a
token. Records, recursive ADTs, and functions use this same representation-blind
contract in both backends. Invalid declarations report `NATIVE STORAGE`.

A fresh empty allocation may take Unit and return `Box a`; it cannot recover a
polymorphic box by an untyped ID. Same-index aliases between opaque wrapper types
are supported. Writes return Unit or Bool. These contracts apply to value
natives; ordinary Fango effect handlers can wrap them.

Storage keeps its typed payload alive but does not extend the lifetime of any
external resource it references. Native resource operations check validity at
runtime. [Runtime.Ref](../../stdlib/Runtime/Ref.fango) provides ordinary IO-marked
mutable storage; native handles and functions cannot cross a task boundary.
Native code must honor its declaration and may not invoke opaque Fango payloads.

## IO references

`Runtime.Ref` provides `new : a ->{IO} Ref a`, `read : Ref a ->{IO} a`, and
`write : Ref a -> a ->{IO} ()`. Aliases observe the latest write. The reference
keeps its value alive until garbage collection; it has no close operation.
Its representation is private, and references cannot cross the task boundary.
Reader and Writer use this ordinary native module for their private buffers.

## Scoped local state

`Runtime.Local` exposes local mutable state under a
[scoped callback](functions.md#scoped-callbacks):

```fango
type Cell e a = { read : () ->{e} a, write : a ->{e} () }

{-# scoped s #-}
run : a -> (Cell s a ->{s} b) ->{e} b
```

`run initial use` creates a fresh cell, calls `use`, and returns its result.
Reading returns the last written value, initially `initial`. Writing replaces
that value. The cell's permission must stay inside `use`; parsed data or other
permission-independent results may leave. Additional consumer effects remain
in `e`. An entirely local computation is pure at the call boundary. `Cell`
values contain functions and cannot cross a task boundary.

This bundled intrinsic uses ordinary reference storage internally and is the
foundation of [`Reader.withBytes`](library-readers.md#reader). Its reference
sidecar is unavailable to compile-time evaluation, so executing either runner
in a splice currently reports `COMPILE-TIME NATIVE`, despite a pure source row.

## Native effect operations

An operation in an `effect` declaration may also use call form:

```fango
effect Clock
    tick : () -> Int = native
```

Its Go function follows the same scalar and Unit-erased ABI. A Fango handler
takes precedence; the native function supplies an otherwise unhandled native
operation.

## FangoHost

Every materialized sidecar package receives the reserved process-global
`FangoHost`. Its `HasInput`, `ReadInputLine`, `WriteOutput`, `Arguments`,
`WorkingDirectory`, and `Exit` methods expose the surrounding Fango process.
Compiled programs install the system host; the interpreter worker installs a
proxy to the active interpreter session. Native function signatures never gain
a hidden context argument. Sidecars may use `FangoHost` only during a native
call and must not replace it or retain it for asynchronous work. Host input
buffer access and individual output writes are serialized. In the interpreter
worker, concurrent host calls keep each request paired with its own reply.

Concurrent Fango invocation uses the restricted [task boundary](library-tasks.md).
Arbitrary native background callbacks are not supported.

## Build and interpreter lifecycle

Native sidecars participate in `check`, build manifests, incremental rebuilds,
`build`, `run`, and `--emit-go`. Bundled standard-library modules use the same
sidecar form, host, and ABI; their sidecars are materialized into generated
projects just like user sidecars. During ordinary interpreter and REPL
evaluation, checked Core runs in a cached persistent worker process beside
the call-form sidecars. Package globals and opaque Go values persist for the
session. Panics are reported across the worker
boundary and reproduced as native panics; `FangoHost.Exit` becomes a
program-exit error instead of terminating the REPL. Native sidecars are trusted
code and are not sandboxed.

A module imported at the REPL prompt brings its sidecar along: the session
rebuilds its worker over the bundled sidecars and every user sidecar imported
so far, and the worker builds and starts the first time one of its functions
is called. Package globals persist across calls but not across such a rebuild.

The word `native` is reserved. Inline `native "Go expression"` templates are
compiler-bundled syntax and are rejected in user modules. The standard library
keeps templates for inlined scalar primitives and compiler-only metaprogramming
representations. `IO` uses its ordinary sidecar.
