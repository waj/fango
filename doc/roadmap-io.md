# Roadmap: HTTP and a concurrent server

This document owns what remains after the byte, buffered IO, file, and socket
layers: HTTP framing and a concurrent server. Implemented contracts live in
[byte sequences](reference/library-bytes.md), [buffered readers and
writers](reference/library-readers.md), and [IO, files, and
sockets](reference/library-io.md). The main [roadmap](roadmap.md#http-and-a-concurrent-server)
summarizes priority.

## HTTP over the implemented socket layer

HTTP is ordinary Fango over a reader and writer. The same parser and renderer
must work over a socket or a memory fixture:

```fango
serve connection =
    Reader.over (Net.source connection) \input ->
        Writer.over (Net.sink connection) 8192 \output ->
            request = Http.readRequest input
            Http.writeResponse output (respond request)
```

```fango
Reader.overBytes fixture \input ->
    Writer.collecting \output ->
        Http.writeResponse output (respond (Http.readRequest input))
```

The HTTP layer should own request-line and header parsing, framing limits,
content length, chunked transfer decoding, connection persistence, and response
serialization. It should not introduce compiler or runtime primitives.

## Concurrent server

`Net.withListener`, `Net.accept`, `Net.withClient`, `Net.source`, and `Net.sink`
are implemented with scoped `Native.Any` resources and typed `Net.Error`
failures. They are sufficient for a listener that handles one connection at a
time. A connection per task still depends on
[cooperative structured async](roadmap-effects.md#4-cooperative-structured-async).

## Delivery and acceptance

A scripted request set covers a malformed request, an oversized header block,
a body shorter than its declared length, chunked bodies, and a keep-alive
sequence on one connection. Each case runs against an in-memory reader and over
a loopback socket with identical results. A loopback proxy drives two readers
at once. The sequential server requires no async support; the concurrent form
is accepted with the structured-async milestone.

Alongside these, move a line-oriented example to the buffered path and add the
[unimplemented grep-lite comparison against Go](roadmap-examples.md), with
identical output and a read-throughput measurement recorded on an idle host.

## Open decisions

- Whether an HTTP limit overflow is a returned parse error or a raised failure.
- Whether read limits belong on a reader or on each operation.
- Which HTTP version subset is the first stable public surface; HTTP/1.1 is the
  intended starting point, without TLS or HTTP/2 in this milestone.

## Verification

Retain the [repository gates](../AGENTS.md) and
[verification contracts](design/verification.md): the Core linter, the
interpreter/compiler differential suite, functional tests, and `go vet`.
Both backends must produce identical bytes for every adapter fixture.
