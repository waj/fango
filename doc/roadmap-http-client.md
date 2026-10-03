# Roadmap: HTTP client

The HTTP/1.1 [client](reference/library-http-client.md) with TLS, gzip, and
redirects, the shared [bodies and errors](reference/library-http.md), and
[URLs](reference/library-url.md) are implemented; the
[HTTP design](design/http.md#the-client-transport) explains the client's
transport effect. This document tracks what remains: connection reuse and
mocking. The [HTTP roadmap](roadmap-io.md) tracks the remaining server
follow-ups.

## Connection reuse

Every request uses a new connection and sends `Connection: close`. HC5b adds a
pool to each `run`:

- Idle connections are keyed by `Endpoint` without its timeout (secure,
  host, port), with a limit on idle connections per key and an idle timeout.
- `release` gains a flag saying whether the connection can be reused, and
  returns a connection to the pool when the response was fully read, neither
  side sent `Connection: close`, and nothing aborted. Redirect responses are
  drained the same way before the next hop.
  A response body that the callback left unread is drained up to a small
  limit (64 KiB) and otherwise closed. This is the client side of the drain
  policy that [the server roadmap](roadmap-io.md) also leaves open.
- A reused connection can turn out to have been closed by the server. If it
  fails before any response byte arrives, and the request is idempotent with
  a body that can be sent again, the request is retried once on a new
  connection.
- The pool is shared by every task that inherits the handler, so it needs
  explicit synchronization, through an `Async` channel or a pool backed by
  native code. That choice is made in HC5b and ties into the cancellation
  question below.

## Mocking

```fango
{-# scoped s #-}
mock : Config -> (Server.Request s ->{s} Server.Response s)
    -> (() ->{Http, Fail Error | e} a) ->{Fail Error | e} a
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
| HC5 Redirects and keep-alive | HC2, HC6 |
| HC3 Mock | HC2, the [test framework](roadmap-testing.md) |

### HC1 Module split

DONE. See the [HTTP reference](reference/library-http.md) and the
[HTTP design](design/http.md).

### HC8 Runner-handled effects in scoped callbacks

DONE. See [scoped callbacks](reference/functions.md#scoped-callbacks) and the
[server reference](reference/library-http.md#server).

### HC6 URL

DONE. See the [URL reference](reference/library-url.md).

### HC2 Client core over plain HTTP

DONE. See the [client reference](reference/library-http-client.md) and the
[HTTP design](design/http.md#the-client-transport).

### HC3 Mock

Waits for the [test framework](roadmap-testing.md). It adds `mock`, plus tests
that use Route handlers as the mock, check the requests they receive, and run
`configure` overrides under the mock.

### HC4 TLS

DONE. See [configuration](reference/library-http-client.md#configuration) and
[Net](reference/library-io.md#net). TLS for the server stays deferred.

### HC5 Redirects and keep-alive

#### HC5a Redirects

DONE. See [redirects](reference/library-http-client.md#redirects).

#### HC5b Keep-alive

The [connection pool](#connection-reuse), with draining, the retry rule, and
a loopback test that counts accepted connections.

### HC7 GZip

DONE. See [client compression](reference/library-http-client.md#compression)
and [server GZip](reference/library-http.md#routing-and-gzip).

## Open questions

- Should sockets close when an `Async` task is cancelled (using
  `Async.contextToken`), or should synchronous `Net` calls with deadlines be
  enough? `Net` already has `…Async` variants that take a context token, so
  `run`'s handler could use them when an `Async` handler is in scope without
  `run` requiring `Async`. The answer also shapes the pool's synchronization.
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
