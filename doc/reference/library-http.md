# HTTP/1.1 server and middleware

[Reference index](../reference.md). `Http` parses and writes HTTP/1.1 over the
[`Reader` and `Writer`](library-readers.md) interfaces. `Http.Server` binds TCP
through [`Net`](library-io.md#net) and runs connections in [`Async`](library-async.md)
tasks. The bundled API is experimental.

## Messages and bodies

`Http` holds what both directions share: `Header`, `Version`, `Body e`, `Error`,
and the `Protocol` effect. `Http.Server` defines the server's messages.
`Request e` has `method : String`, `target : String`, `version : Version`,
`headers : List Header`, and `body : Reader e`. The target is the raw request
target, including its query. `Version` currently has only `Http11`. Header
names and values are strings; incoming values must be valid UTF-8. Header
lookup and replacement (`getHeader`, `getHeaders`, `hasHeader`, `removeHeader`,
`setHeader`) compare names without ASCII case. Duplicate fields remain in the
list unless replaced.

`Response e` has `status : Int`, `headers : List Header`, and `body : Body e`.
A body is one of:

- `Empty`
- `BytesBody Bytes`
- `StreamBody (Writer e ->{e} ())`, pushed into a writer and sent chunked
- `SizedBody Int (Writer e ->{e} ())`, pushed into a writer and sent with that
  `Content-Length`

A streaming body runs with the handler's effects, so it can produce its output
while it writes, holding one buffer rather than the whole payload. Writes to a
`StreamBody` are collected into chunks of up to 8 KiB; an explicit `flush`
frames whatever is pending and flushes the connection. A `SizedBody` must write
exactly its declared length: a write past it fails at that write, and a body
that returns short fails when it returns. Both failures raise `InvalidMessage`.

The server returns this value from the application handler and serializes it.
The handler's effect row includes its own failures; it must handle them before
returning. The server handles `Http.Protocol.invalid` for malformed HTTP,
invalid responses, and protocol failures the handler or its body raise.

`Http.Server.writeResponse method output response` accepts final status codes
200–599 except 101. It supplies `Content-Length` for empty, byte, and sized
bodies and chunked transfer encoding for streams. Applications must not set
`Content-Length` or `Transfer-Encoding` in response headers. `HEAD` writes only
the head, including a sized body's length, and runs no body. 204 and 304 cannot
carry a body. Response header names must be tokens and values cannot contain
control characters other than tab. Every check runs before the first byte is
written.

A body that fails partway leaves the message truncated: the final chunk is
never written and a sized body is never padded. `Http.abortBody reason` stops a
body on purpose by raising `BodyAborted`.

## Errors

`Http.Error` is a plain union:

| Constructor | Meaning | Server status |
| --- | --- | --- |
| `Malformed String` | Invalid syntax in a start line, header, or chunk; a fixed body that ends early | 400 |
| `LineTooLong` | Request line over its limit | 414 |
| `HeadersTooLarge` | Header block or trailers over the limit | 431 |
| `BodyTooLarge` | Body over the limit | 413 |
| `Unsupported String` | Unknown transfer coding | 501 |
| `InvalidMessage String` | A response the application built is invalid: a framing header, a body on a status that cannot carry one, a sized body whose length does not match | 500 when nothing has been written |
| `BodyAborted String` | A body stopped through `abortBody` | none; the head has been written |

## Parsing and framing

`Http.Server.withRequest lineLimit headerLimit bodyLimit reader use` parses one
request from any reader, scopes the body reader to `use`, and returns `Nothing`
at a clean end of input or `Just (value, bodyComplete)`. `bodyComplete` says
whether the consumer reached the end of the framed body. The callback can return
a response whose streaming body reads the request; the caller must write that
response inside the callback's scope.

The parser requires an HTTP/1.1 request line and exactly one `Host` field. It
accepts no body, one decimal `Content-Length`, or one `Transfer-Encoding:
chunked`. Conflicting or duplicate framing fields are rejected. Fixed lengths
and decoded chunk data obey `bodyLimit`; request lines and header blocks have
separate limits. Chunk trailers are parsed with the header limit. Invalid syntax
raises `Http.Protocol.invalid` with an `Http.Error`. A fixed body that ends
early raises `Malformed` when a consumer reads past the available bytes.

`Http.Wire` holds the framing both directions share: header blocks, body
framing, and message serialization after the start line.

## Server

`Http.Server.serve config stop handler` binds `config.port`, uses up to
`config.workers` concurrent accept tasks, and returns after a value arrives on
the `Async.Channel ()` stop channel. `defaultConfig port` supplies worker,
framing, deadline, and shutdown limits. Each connection owns its socket and
scoped reader and writer. A connection can serve consecutive requests only
when the previous body was fully consumed and neither side requested
`Connection: close`. Invalid request framing produces a 400, 413, 414, 431, or
501 response and closes that connection. Stopping closes the listener, waits
for active connections up to `shutdownGraceMs`, then cancels remaining workers.
Cancellation closes their blocking sockets. A response that fails its checks
gets a 500 and the connection closes. A body that fails after the head was
written closes the connection without completing the response.

`serve` takes a handler of shape `Request {Protocol | s} ->{Protocol | s} Response {Protocol | s}`,
where `s` is the connection's scope plus the application's row; a handler of
shape `Request e ->{e} Response e` also fits. Handlers and their bodies may
raise `Protocol`: before the head is written the server answers with the status
for the error, and afterwards it closes the connection. So `Route.dispatch` and
`abortBody` work without a local handler. Application errors should be handled
inside the handler; the server does not require a `Result appError Response`
value.

The runnable [server example](../../examples/http_server.fango) combines a
parameter route with streaming GZip and accepts a port argument.

## Routing and GZip

`Http.Server.Route.dispatch routes fallback request` checks routes in order. A route
has `method`, `pattern`, and `handler : List Param -> Request e ->{e} Response e`.
Path segments named `:name` capture one decoded segment. `path` removes the
query and extracts the path from an absolute-form target. Percent decoding
rejects malformed escapes, invalid UTF-8, and encoded slashes or backslashes.
A path matching another method produces 405 with `Allow`; otherwise the fallback
runs. `Http.Server.Route.lookup` finds a captured parameter.

`Http.GZip.wrap handler request` compresses eligible responses when the request
accepts gzip, including quality values and wildcard negotiation. It leaves
`HEAD`, 204, 304, empty, and already encoded responses alone. Compression is
streaming, turns a sized body into a chunked one, and sets `Content-Encoding: gzip` plus `Vary: Accept-Encoding`.
