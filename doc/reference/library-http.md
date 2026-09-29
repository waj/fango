# HTTP/1.1 server and middleware

[Reference index](../reference.md). `Http` parses and writes HTTP/1.1 over the
[`Reader` and `Writer`](library-readers.md) interfaces. `Http.Server` binds TCP
through [`Net`](library-io.md#net) and runs connections in [`Async`](library-async.md)
tasks. The bundled API is experimental.

## Request and response

`Request e` has `method : String`, `target : String`, `version : Version`,
`headers : List Header`, and `body : Reader e`. The target is the raw request
target, including its query. `Version` currently has only `Http11`. Header
names and values are strings; incoming values must be valid UTF-8. Header
lookup and replacement (`getHeader`, `getHeaders`, `hasHeader`, `removeHeader`,
`setHeader`) compare names without ASCII case. Duplicate fields remain in the
list unless replaced.

`Response e` has `status : Int`, `headers : List Header`, and
`body : ResponseBody e`. A body is `Empty`, `BytesBody Bytes`, or
`StreamBody (Writer e ->{e} ())`. The server returns this value from the
application handler and serializes it. The handler's effect row includes its
own failures; it must handle them before returning. The server handles
`Http.Protocol.invalid` for malformed HTTP and invalid responses.

`Http.writeResponse method output response` accepts final status codes 200–599
except 101. It supplies `Content-Length` for empty or byte bodies and chunked
transfer encoding for streams. Applications must not set `Content-Length` or
`Transfer-Encoding` in response headers. `HEAD` writes only the head; 204 and
304 cannot carry a body. Response header names must be tokens and values cannot
contain control characters other than tab.

## Parsing and framing

`Http.withRequest lineLimit headerLimit bodyLimit reader use` parses one request
from any reader, scopes the body reader to `use`, and returns `Nothing` at a clean
end of input or `Just (value, bodyComplete)`. `bodyComplete` says whether the
consumer reached the end of the framed body. The callback can return a response
whose streaming body reads the request; the caller must write that response
inside the callback's scope.

The parser requires an HTTP/1.1 request line and exactly one `Host` field. It
accepts no body, one decimal `Content-Length`, or one `Transfer-Encoding:
chunked`. Conflicting or duplicate framing fields are rejected. Fixed lengths
and decoded chunk data obey `bodyLimit`; request lines and header blocks have
separate limits. Chunk trailers are parsed with the header limit. Invalid syntax
raises `Http.Protocol.invalid` with a typed `ErrorKind` and message. A fixed body
that ends early raises `BadRequest` when a consumer reads past the available bytes.

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
Cancellation closes their blocking sockets.

A handler has the shape `Request e ->{e} Response e`, where the server instantiates
`e` with IO, network failure, and HTTP protocol effects plus the application's
row. Application errors should be handled inside the handler; the server does
not require a `Result appError Response` value.

The runnable [server example](../../examples/http_server.fango) combines a
parameter route with streaming GZip and accepts a port argument.

## Routing and GZip

`Http.Route.dispatch routes fallback request` checks routes in order. A route
has `method`, `pattern`, and `handler : List Param -> Request e ->{e} Response e`.
Path segments named `:name` capture one decoded segment. `path` removes the
query and extracts the path from an absolute-form target. Percent decoding
rejects malformed escapes, invalid UTF-8, and encoded slashes or backslashes.
A path matching another method produces 405 with `Allow`; otherwise the fallback
runs. `Http.Route.lookup` finds a captured parameter.

`Http.GZip.wrap handler request` compresses eligible responses when the request
accepts gzip, including quality values and wildcard negotiation. It leaves
`HEAD`, 204, 304, empty, and already encoded responses alone. Compression is
streaming and sets `Content-Encoding: gzip` plus `Vary: Accept-Encoding`.
