# Roadmap: HTTP client

The plain HTTP/1.1 [client](reference/library-http-client.md), the shared
[bodies and errors](reference/library-http.md), and [URLs](reference/library-url.md)
are implemented; the [HTTP design](design/http.md#the-client-transport) explains
the client's transport effect. This document tracks what remains: compression,
TLS, redirects, connection reuse, and mocking. The [HTTP roadmap](roadmap-io.md)
tracks the remaining server follow-ups.

## Configuration and errors still to come

Later milestones add two `Config` fields and one error:

- `decompress : Bool`, on by default ([HC7](#hc7-gzip)). When on, the client
  also sends `Accept-Encoding: gzip`, merged as a default header before
  `Config.headers`.
- `maxRedirects : Int`, 10 by default ([HC5a](#hc5a-redirects)).
- `TooManyRedirects` in `Client.Error`, raised once redirects exceed
  `maxRedirects`.

A malformed gzip stream reuses `Malformed`, and a decompressed body over its
limit is `BodyTooLarge`.

## Compression

`Http.GZip` provides a codec both directions use, plus a policy for each side.
Only `gzip` is supported. `deflate` is ambiguous in practice, and other
codings would need new native code.

**Codec.**

- `encoding : Body e -> Body e` generalizes today's `compressBody`. It returns
  a `StreamBody`, because the compressed length isn't known in advance, even
  for a `SizedBody`.
- `decoding : Int -> Reader e -> (Reader s ->{s} a) ->{e} a` wraps a reader
  in a decompressing one, with a limit on decompressed bytes. Each refill
  pulls compressed bytes from the inner reader and feeds a native inflater.
  The native side mirrors the existing compressor: `newDecompressor`,
  `push : Decompressor -> Bytes -> Result String Bytes`, and
  `finish : Decompressor -> Result String ()`, which checks the trailer.
  Concatenated gzip members are accepted, as Go does.
- A corrupt or truncated stream raises `Malformed`. Going over the limit
  raises `BodyTooLarge`. The limit guards against zip bombs.

**Client responses.** Decompression is on by default (`Config.decompress`).
The client adds `Accept-Encoding: gzip` unless the request or config sets
`Accept-Encoding` itself. If either does, the caller asked for specific
encodings and gets the raw bytes. This is Go's rule. When the client added the
header and the response has `Content-Encoding: gzip`:

- the response reader is wrapped with `decoding`;
- `Content-Encoding` and `Content-Length` are removed from `Response.headers`,
  since they no longer describe the body;
- `maxBodyBytes` counts decompressed bytes wherever a body is buffered. A
  `send` callback reading the stream itself is not limited.

HEAD, 204, and 304 responses are left alone. Any other `Content-Encoding` is
passed through untouched.

**Client requests.** Request compression is opt-in, since few servers accept
compressed request bodies:
`compressRequest : Request e -> Request e` sets `Content-Encoding: gzip` and
wraps the body with `encoding`.

**Server responses.** `wrap` keeps today's behaviour: it negotiates with the
request's `Accept-Encoding` and compresses eligible responses, `SizedBody`
included.

**Server requests.** A new opt-in middleware,
`decodeRequests : Int -> (Request e ->{e} Response e) -> Request e ->{e} Response e`,
decodes request bodies with `Content-Encoding: gzip` and removes the header.
The limit counts decompressed bytes. Any other content coding gets a 415
response.

## Redirects

Redirects are followed in library code, so they work the same under every
handler.

- 301, 302, 303, 307, and 308 are followed, up to `maxRedirects`. One more
  raises `TooManyRedirects`. `maxRedirects = 0` turns following off, and the
  caller gets the 3xx response.
- `Location` is resolved against the current URL with `Url.resolve`. A
  missing or unparseable `Location` returns the 3xx response unfollowed.
- On 303, and on 301 or 302 after a method other than GET or HEAD, the next
  request is a GET without a body, matching browsers and Go.
- 307 and 308 resend the same method and body. Only `Empty` and `BytesBody`
  can be sent again. A request with a streaming body gets the 3xx response
  back unfollowed, because the stream has already been consumed.
- When the origin (scheme, host, and port) changes, `Authorization` and
  `Proxy-Authorization` are dropped from the next request.
- A redirect from `https` to `http` is not followed; the caller gets the
  3xx response.
- Before following, the client discards the redirect response's body. In
  HC5b that means draining it if the connection is to be reused.
- `send`'s callback sees only the final response. `Response.url` and
  `Reply.url` say where it came from.

## Connection reuse

Until HC5, every request uses a new connection and sends `Connection: close`.
HC5b adds a pool to each `run`:

- Idle connections are keyed by `Endpoint` without its timeout (secure,
  host, port), with a limit on idle connections per key and an idle timeout.
- `release` gains a flag saying whether the connection can be reused, and
  returns a connection to the pool when the response
  was fully read, neither side sent `Connection: close`, and nothing aborted.
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
| HC7 GZip | HC2 |
| HC4 TLS | HC2 |
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

A `Net` native over Go's `crypto/tls` that uses the system roots, sends SNI,
and verifies the host name. It returns a `Net.Connection`, so the transport
treats both kinds of connection alike and `Endpoint.secure` picks the dial.
This milestone also adds `https://` URLs with 443 as the default port. Tests
need a loopback TLS server with a test certificate, so the native accepts
extra root certificates. Whether those are also exposed in `Config` is an open
question below. TLS for the server stays deferred.

### HC5 Redirects and keep-alive

#### HC5a Redirects

The [redirect rules](#redirects), with stub-transport fixtures for each
status, method rewriting, a streaming body that can't be sent again,
cross-origin header removal, refusing a downgrade, and the redirect limit.

#### HC5b Keep-alive

The [connection pool](#connection-reuse), with draining, the retry rule, and
a loopback test that counts accepted connections.

### HC7 GZip

The [compression](#compression) design:

- the native decompressor and `decoding`;
- `encoding`, generalized from `compressBody`;
- automatic decompression of client responses, and `compressRequest`;
- the server's `decodeRequests` middleware.

Fixtures cover a round trip through both directions, a response the caller
asked to keep encoded, a truncated stream, concatenated members, and a zip
bomb stopped by the decompressed limit.

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
- Custom root certificates and client certificates in `Config`.
- Proxy support (`HTTP_PROXY` and similar) and cookies are out of scope for
  now.
- Streaming a proxied response. A server handler can stream its request body
  into a client request, but not the upstream response back: the response
  body would read `send`'s reader after the callback has returned, which the
  scoped permission rejects, since the connection is closed by then. The server
  would need a way to write a response while the handler is still running.
