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

One kind of declared type also crosses: a type the same module declares with
exactly one constructor holding exactly one boundary scalar, such as
`type Token = Token Int`, may appear as a parameter or result. The Go function
sees the scalar (`int64` here); the compiler projects the field on the way in
and rebuilds the constructor on the way out, in both backends. Keep the
constructor out of the module's exposing list and derive no `Show` or `Eq`,
and callers hold an opaque handle they can neither forge nor inspect — the
bundled `File.Handle` is exactly this, with `{-# resource #-}` adding its scoped
capability contract. Anything else — functions, other ADTs,
records, polymorphic variables, class constraints, Go type parameters, and
multiple results — is a `NATIVE ABI` error. A Go `error` result is likewise
rejected in user sidecars (`FALLIBLE NATIVE NOT ALLOWED`); only the bundled
`File` module's natives return one, which the compiler turns into
`Result IO.Error a`. Effect rows on native value types
are preserved for checking and may contain `IO` or user-declared effects; the
sidecar call itself uses the same scalar ABI and does not receive a hidden
evidence argument. Sidecars may import only Go standard-library packages.
Every call-form declaration needs its matching exported function, and every
exported sidecar function needs a declaration.

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
call and must not replace it or retain it for asynchronous work.

## Build and interpreter lifecycle

Native sidecars participate in `check`, build manifests, incremental rebuilds,
`build`, `run`, and `--emit-go`. Bundled standard-library modules use the same
sidecar form, host, and ABI; their sidecars are materialized into generated
projects just like user sidecars. During ordinary interpreter and REPL
evaluation, call-form sidecars run in a cached persistent worker process.
Package globals persist for the session. Panics are reported across the worker
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
