# Roadmap: HTTP client

The HTTP/1.1 [client](../stdlib/Http/Client.fango) with TLS, gzip,
redirects, and connection reuse, the shared
[bodies and errors](../stdlib/Http.fango), and
[URLs](../stdlib/Url.fango) are implemented; the
[HTTP design](design/http.md#the-client-transport) explains the client's
transport effect. This document tracks what remains: mocking, and the open
questions below. The [HTTP roadmap](roadmap-io.md) tracks the remaining server
follow-ups.

## Mocking

```fango
{-# scoped s #-}
mock : (Server.Request s ->{s} Server.Response s)
    -> (() ->{Http | e} a) ->{e} a
```

`mock` is a transport handler that uses `Memory` handles. It collects the bytes
written to each handle. On the first `receive` after writing, it:

1. parses the buffered request with `Server.withRequest`
2. runs the server handler
3. serializes the response with `Server.writeResponse` into a buffer
4. answers this and later `receive` calls from that buffer

The client side runs its real framing, so it is tested in both directions
without sockets or `IO`. The mock buffers whole
messages, which is fine for tests. `Http.Server.Route.dispatch` tables work as
mocks unchanged. A test records the requests it receives through its own
effects in the handler's row, such as State or Writer. A `Protocol` error from
the server side (a request the parser rejects) becomes the error response the
real server would send, so the client sees a 4xx just as it would against a
real server.

The mock waits for a test framework (see HC3). The byte-level transport, the
`Memory` connection case, and `Client.stub` already answer requests without
sockets; `mock` adds the server round trip.

## Milestones

The milestones are listed in the order they are expected to land. The IDs
follow allocation order, not that order.

| Milestone | Depends on |
| --- | --- |
| HC1 Module split | — (DONE) |
| HC8 Runner-handled effects in scoped callbacks | HC1 (DONE) |
| HC6 URL | — (DONE) |
| HC2 Client core over plain HTTP | HC1, HC6 (DONE) |
| HC7 GZip | HC2 (DONE) |
| HC4 TLS | HC2 (DONE) |
| HC5 Redirects and keep-alive | HC2, HC6 (DONE) |
| HC3 Mock | HC2, the [test framework](roadmap-testing.md) |

### HC1 Module split

DONE. See the [Http module](../stdlib/Http.fango) and the
[HTTP design](design/http.md).

### HC8 Runner-handled effects in scoped callbacks

DONE. See [scoped callbacks](reference/functions.md#scoped-callbacks) and the
[server module](../stdlib/Http/Server.fango).

### HC6 URL

DONE. See the [Url module](../stdlib/Url.fango).

### HC2 Client core over plain HTTP

DONE. See the [client module](../stdlib/Http/Client.fango) and the
[HTTP design](design/http.md#the-client-transport).

### HC3 Mock

Waits for the [test framework](roadmap-testing.md). It adds `mock`, plus tests
that use Route handlers as the mock, check the requests they receive, and run
`configure` overrides under the mock.

### HC4 TLS

DONE. See [configuration](../stdlib/Http/Client.fango) and
[Net](../stdlib/Net.fango). TLS for the server stays deferred.

### HC5 Redirects and keep-alive

#### HC5a Redirects

DONE. See [redirects](../stdlib/Http/Client.fango).

#### HC5b Keep-alive

DONE. See [running requests](../stdlib/Http/Client.fango).

### HC7 GZip

DONE. See [client compression](../stdlib/Http/Client.fango)
and [server GZip](../stdlib/Http/GZip.fango).

## Open questions

- Should sockets close when an `Async` task is cancelled (using
  `Async.contextToken`), or should synchronous `Net` calls with deadlines be
  enough? `Net` already has `…Async` variants that take a context token, so
  `run`'s handler could use them when an `Async` handler is in scope without
  `run` requiring `Async`.
- A deadline for a whole request, as opposed to the idle timeouts per
  operation.
- A server may answer before the client has sent the whole request body, for
  example a 413 during an upload. The client currently fails with `Transport`
  when its write is refused; it could read the early response instead.
  `Expect: 100-continue` is related.
- Client certificates in `Config`.
- Proxy support (`HTTP_PROXY` and similar) and cookies are out of scope for
  now.
- Streaming a proxied response. A server handler can stream its request body
  into a client request, but not the upstream response back: the response
  body would read `send`'s reader after the callback has returned, which the
  scoped permission rejects, since the connection is closed by then. The server
  would need a way to write a response while the handler is still running.
