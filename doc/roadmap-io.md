# Roadmap: buffered readers and writers, and sockets

This document owns what is left of the buffered IO layer and its delivery
sequence: the file, memory, and socket adapters beneath `Reader` and `Writer`,
and the sockets and HTTP above them. Its motivating consumer is an HTTP server
written in Fango, so acceptance is stated in terms of what a server needs
rather than library breadth.

Implemented contracts remain in [design](design.md) and
[reference](reference.md); the main
[roadmap](roadmap.md#byte-io-buffered-readers-and-writers) summarizes
priorities. Everything below is **proposed**. Fango blocks are acceptance
specifications rather than fixtures that compile today.

`Bytes` and the buffering layer over it are implemented. [Byte
sequences](reference/library-bytes.md) owns `Bytes`, its `Source`, and its
`Sink`; [buffered readers and writers](reference/library-readers.md) owns
`Reader`, `Writer`, and everything derived from them; and
[backend](design/backend.md#bytes-representation) owns the representation.
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

`File.Handle` is the right lifetime model at the wrong granularity. Reading is
`readLine`, whose contract is a `String` and its terminator; there is no
counted read, so no `Source` can be built over a file that carries bytes a
`String` cannot hold. Writing goes straight to the `*os.File`, so a response
assembled from a status line, several headers, and a body costs that many
syscalls even though a `Writer` above it would have batched them.

Filling that in needs one compiler change. The bundled native boundary accepts
only scalars, so a native answering `Bytes` — a counted file read, later a
socket read — is the first that needs `[]byte` admitted to it.

## Step 1 — the adapters

```fango
File.source : File.Handle -> Source {IO, Fail IO.Error}
File.sink : File.Handle -> Sink {IO, Fail IO.Error}
IO.stdout : Sink {IO}
IO.stdin : Source {IO}
```

`File` gains counted byte natives beside its existing line natives, which keep
their contracts; the unbuffered `File.write` stays as it is. `IO.stdout` and
`IO.stdin` wait for a program that needs them, because reaching them from `IO`
puts `Bytes` in every program's Prelude closure.
## Step 2 — sockets, HTTP, and a concurrent server

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
compiler-side questions come with it beyond the byte boundary Step 1 opens.
Fallible natives that the
boundary turns into `Result IO.Error a` are spelled for the bundled `File`
module alone, and the
[general case is deferred](roadmap-effects.md#deferred-topics) pending
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
| Counted file and socket reads and writes | `Source` and `Sink` adapters |
| — | Framing, limits, chunked decoding, HTTP |

The adapters and `Net` are library names and need no grammar change, as `Bytes`
and the buffering layer needed none.

## Delivery and acceptance

Each step is usable without the ones after it.

**1. The adapters.** The same parsing code passes over a memory reader and
over a file, and reading a file through `Reader.chunks` matches `File.read`
byte for byte. A writer over a file sink emits one underlying write per flush
window. A counted read carries bytes no `String` could hold, and interleaves
with `File.readLine` on one handle. No source outlives the scope owning its
handle.

**2. Sockets and HTTP.** A client fetches over a loopback connection; a
listener serves one connection at a time; a peer closing mid-read and
mid-write is an ordinary typed failure; no handle outlives its scope. A
scripted request set covers a malformed request, an oversized header block, a
body shorter than its declared length, and a keep-alive sequence on one
connection — each exercised against a memory source in a fixture as well as
over a socket. A loopback proxy drives two readers at once.

Alongside these, move a line-oriented example to the buffered path and add the
[unimplemented grep-lite comparison against Go](roadmap-examples.md), with
identical output and a read-throughput measurement recorded on an idle host.

## Open decisions

- Whether socket failures extend `IO.Error`'s `Kind` or introduce `Net.Error`,
  and whether a limit overflow should ever be raised rather than returned.
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
