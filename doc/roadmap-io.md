# Roadmap: sockets and an HTTP server

This document owns what is left of the buffered IO layer: sockets, HTTP, and a
server over them. Its motivating consumer is an HTTP server written in Fango,
so acceptance is stated in terms of what a server needs rather than library
breadth.

Implemented contracts remain in [design](design.md) and
[reference](reference.md); the main
[roadmap](roadmap.md#byte-io-sockets-and-an-http-server) summarizes
priorities. Everything below is **proposed**. Fango blocks are acceptance
specifications rather than fixtures that compile today.

`Bytes`, the buffering layer over it, and the file adapters are implemented.
[Byte sequences](reference/library-bytes.md) owns `Bytes`, its `Source`, and
its `Sink`; [buffered readers and writers](reference/library-readers.md) owns
`Reader`, `Writer`, and everything derived from them; [IO and
files](reference/library-io.md) owns `File.source` and `File.sink`; and
[backend](design/backend.md#bytes-representation) owns the representation and
the byte boundary a bundled sidecar crosses.

That settled two questions this document used to ask. A pure `Memory.source`
cannot exist — a source that answers a buffer once and then ends is state, and
a pure closure has none — so reading memory is `Reader.overBytes`, which seeds
the `over` activation's own cell and is the counterpart of
`Writer.collecting`. And a writer's final flush lives in its handler's
`return` clause, so a body that fails emits nothing further rather than a
truncated message.

The commitments that remain are: hot loops that scan inside a native and cross
the Fango boundary once per line or per chunk, and lifetimes proven by the
existing capture checker.

## What today's library cannot do

Files are done: `File.source` and `File.sink` drive the same parsing and
writing code a memory buffer does. What is left is the same pair over a
socket, and the console — `IO.stdout : Sink {IO}` and `IO.stdin : Source {IO}`
wait for a program that needs them, because reaching them from `IO` puts
`Bytes` in every program's Prelude closure.

## Sockets, HTTP, and a concurrent server

```fango
{-# resource #-}
type Listener
{-# resource #-}
type Connection

Net.withListener : Int -> (Listener ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
Net.accept : Listener -> (Connection ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
Net.withClient : String -> Int -> (Connection ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
Net.source : Connection -> Source {IO, Fail IO.Error}
Net.sink : Connection -> Sink {IO, Fail IO.Error}
```

Same lifetime shape as `File`, with the same private handle table. Two
compiler-side questions come with it. Fallible natives that the boundary turns
into `Result IO.Error a` are spelled for the bundled `File` module alone, and
the [general case is deferred](roadmap-effects.md#deferred-topics) pending
resolved error identities; admitting a second bundled module is the smallest
form of that. And socket failures — connection refused, reset by peer, address
in use — have no member in `IO.Error`'s `Kind`.

HTTP is then ordinary Fango over a reader and a writer:

```fango
serve : Net.Connection ->{IO, Fail IO.Error} ()
serve connection =
    Reader.over (Net.source connection) \input ->
        Writer.over (Net.sink connection) 8192 \output ->
            request = Http.readRequest input
            Http.writeResponse output (respond request)
```

and the same code over fixtures:

```fango
Reader.over (Memory.source fixture) \input ->
    Writer.collecting \output ->
        Http.writeResponse output (respond (Http.readRequest input))
```

A proxy holds two readers at once, which is what a reader being a value
bought:

```fango
Reader.over (Net.source client) \downstream ->
    Reader.over (Net.source upstream) \up ->
        relay downstream up
```

`downstream` was bound in the outer activation and is driven from the inner
scope; each `refill` reaches its own socket.

A connection per task depends on
[cooperative structured async](roadmap-effects.md#4-cooperative-structured-async).
A server handling one connection at a time needs none of it and is this
document's acceptance program.

## Compiler and runtime boundary

| Compiler or runtime | Ordinary Fango library |
| --- | --- |
| Scope ownership and resource escape proofs | `Net` scopes |
| Socket reads and writes | `Net.source` and `Net.sink` |
| — | Framing, limits, chunked decoding, HTTP |

`Net` and its adapters are library names and need no grammar change, as `Bytes`,
the buffering layer, and the file adapters needed none.

## Delivery and acceptance

A client fetches over a loopback connection; a listener serves one connection
at a time; a peer closing mid-read and mid-write is an ordinary typed failure;
no handle outlives its scope. A scripted request set covers a malformed
request, an oversized header block, a body shorter than its declared length,
and a keep-alive sequence on one connection — each exercised against a memory
reader in a fixture as well as over a socket. A loopback proxy drives two
readers at once.

Alongside these, move a line-oriented example to the buffered path and add the
[unimplemented grep-lite comparison against Go](roadmap-examples.md), with
identical output and a read-throughput measurement recorded on an idle host.

## Open decisions

- Whether socket failures extend `IO.Error`'s `Kind` or introduce `Net.Error`,
  and whether a limit overflow should ever be raised rather than returned.
- Whether `Bytes` should cross the sidecar boundary in user modules too. The
  syntactic check matches a spelling before resolution, and in a user module
  `Bytes` could be the bundled type, the module's own, or one imported
  unqualified from a third; the last would be taken for a scalar wrapper and
  miscompile. Lifting the bundled-only restriction needs a resolved-type
  backstop over every boundary position, not just the fallible payload, which
  would also start diagnosing shapes the loader rejects today.
- Whether `Bytes.fromString` and `Bytes.toString` may share storage rather than
  copy. Both directions are safe for immutable values, but the Go spelling
  needs `unsafe`, which no part of the runtime uses today.
- Whether a read limit belongs on the reader rather than on each call. The
  three answers `Read` carries have shipped; where the bound is declared has
  not been revisited.

## Verification

Retain the [repository gates](../AGENTS.md) and
[verification contracts](design/verification.md): the Core linter, the
interpreter/compiler differential suite, functional tests, `go vet`, and
updated goldens for intentional language changes. Both backends must produce
identical bytes for every adapter fixture.
