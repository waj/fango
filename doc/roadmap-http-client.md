# Roadmap: HTTP client

A client for HTTP/1.1, and later HTTPS, that is consistent with the
[server](reference/library-http.md). It provides helpers for common requests,
streams request and response bodies without buffering them, decodes compressed
responses, and can be mocked in tests. Several pieces are shared with the
server: the body type, the framing code, compression, and the error type. This
document therefore also covers the server changes they bring. This is a
proposal: none of it is implemented yet. The [HTTP roadmap](roadmap-io.md)
tracks the remaining server follow-ups.

## Why the server types do not fit

`Http.Request e` has `body : Reader e`, which the handler pulls from.
`Http.Response e` has `body : ResponseBody e`, which the serializer pushes out.
A client needs the opposite pair: a request body it sends and a response body
it reads. A single message type taking the body type as a parameter would turn
every server signature into `Http.Request (Reader s)`, because Fango has no
type aliases. So each direction gets its own message types, and `Http` keeps
only what both directions share.

## Module layout

| Module | Contents |
| --- | --- |
| `Url` | The `Url` type, parsing, printing, resolution, percent-encoding, and query helpers. See [URLs](#urls) |
| `Http` | `Header`, `Version`, `Body e` (renamed from `ResponseBody`, see [bodies](#request-and-response-bodies)), `Error`, the `Protocol` effect, `abortBody`, `fileBody`, and the header helpers (`getHeader`, `getHeaders`, `hasHeader`, `setHeader`, `removeHeader`, `equalAscii`) |
| `Http.Wire` | Low-level framing pieces both directions use: header-block parsing, the chunked reader, the buffered chunked writer, the length-checked writer, field validation, and the choice between Content-Length and chunked framing |
| `Http.GZip` | The gzip codec both directions use (a compressing body and a decompressing reader), content-coding negotiation, the server's response and request middleware, and the client's request compression. See [compression](#compression) |
| `Http.Server` | `Request e` (`body : Reader e`) and `Response e` (`body : Body e`), `withRequest`, `writeResponse`, `Config`, `serve` |
| `Http.Server.Route` | The router, moved from `Http.Route`, typed over `Http.Server.Request` and `Http.Server.Response` |
| `Http.Client` | Client `Request e`, `Response e`, `Reply`, `Error`, `Unexpected`, `Config`, the `Http` effect, `writeRequest`, `withResponse`, `run`, `configure`, and later `mock` |

Server handlers change from `Http.Request e ->{e} Http.Response e` to
`Server.Request e ->{e} Server.Response e`. Server behaviour does not change,
except for the chunk sizes described under [bodies](#request-and-response-bodies).
The modules are layered without cycles: `Url` and `Http`, then `Http.Wire`,
then `Http.GZip`, then `Http.Server` and `Http.Server.Route`, then
`Http.Client`. The client imports the server module only for `mock`.

`Http.Route` moves to `Http.Server.Route` because it only serves the server:
`dispatch` takes and returns server messages, and a client has no use for it.
An import alias keeps call sites short (`import Http.Server.Route as Route`).
`Http.GZip` stays at the top level because it now serves both directions.

## Errors

Errors come in three levels, each with its own type:

- **Protocol:** `Http.Error`, shared by both directions. It reports malformed
  or oversized messages and aborted bodies.
- **Transport:** `Client.Error`, everything a client request can fail with.
  It wraps `Http.Error` and `Net.Error`.
- **Expectations:** `Client.Unexpected`, raised only when a status or body
  doesn't meet the caller's expectation. Only the helpers and `expectSuccess`
  raise it.

`Http.Error` is [implemented](reference/library-http.md#errors). The client
adds two types:

```fango
type Error      = InvalidUrl String | Transport Net.Error | Timeout | Protocol Http.Error
                | TooManyRedirects
type Unexpected = BadStatus Reply | BadBody Json.Error
```

Both are plain unions, and each constructor carries only the data its case
needs. The caller already knows which URL it requested, so errors don't repeat
it. A malformed status line, a malformed gzip stream, and a status line over
`maxHeaderBytes` reuse `Malformed` and `LineTooLong`; a decompressed body over
its limit is `BodyTooLarge`.

`Client.Error` reports what can go wrong with a request regardless of how the
caller reads the response:

- `InvalidUrl` covers a string that doesn't parse, an unsupported scheme, a URL
  with user information, and a relative URL with no base URL.
- `Transport` wraps the `Net.Error` from connecting, reading, or writing.
  `Net`'s `TimedOut` kind never appears inside `Transport`: an expired deadline
  becomes `Timeout`, so callers have one case to match.
- `Protocol` wraps the `Http.Error` raised while writing the request or parsing
  the response. A response body larger than `maxBodyBytes` while a helper
  buffers it is `Protocol BodyTooLarge`.
- `TooManyRedirects` is raised once redirects exceed `maxRedirects`; see
  [redirects](#redirects).

`send` and `fetch` treat every status as a normal result, so they can't raise
`Unexpected`. A non-2xx status or an undecodable body is only an error for the
caller who expected otherwise. The helpers expect a 2xx status and, for JSON,
a decodable body, and raise `Unexpected` when that fails. `BadStatus` carries
the whole buffered `Reply`, because the body of an error response often
explains the error (an API's error message, for example).

Effect rows carry both failures as separate effects:
`{Http, Fail Client.Error, Fail Client.Unexpected}`. A script that only needs
"it failed" handles both; code that retries transport errors but reports bad
statuses can tell them apart without matching half a union. A row can already
hold two `Fail` effects with different payloads. In a quick check, nested
`attempt` calls separated `Fail A` from `Fail B`, and the inner one caught the
first label in the row. HC2 must confirm that a caller can catch either one
without awkward annotations. If it can't, that is a language problem to fix,
not one the client API should work around.

## Client types

```fango
type Request e  = { method : String, url : Url, headers : List Header, body : Body e }
type Response e = { url : Url, status : Int, headers : List Header, body : Reader e }   -- streamed, scoped
type Reply      = { url : Url, status : Int, headers : List Header, body : Bytes }      -- buffered
```

As in the server, the method is a `String` and the status an `Int`. A
`Request` URL may be relative; it is resolved against `baseUrl` when sent.
`Response.url` and `Reply.url` give the absolute URL of the final response,
which differs from the request after a redirect.

The client manages `Host`, `Content-Length`, `Transfer-Encoding`, and
`Connection` itself. Setting any of them in a request or in `Config.headers`
raises `Protocol (InvalidMessage …)`, the way `writeResponse` rejects
framing headers. `Host` is the URL's host, followed by the port when it isn't
the scheme's default.

`writeRequest` frames the request body by its variant (see
[bodies](#request-and-response-bodies)). `withResponse` frames the response as
follows:

- HEAD requests and 1xx, 204, and 304 responses have no body.
- Any other response is framed by Content-Length, by chunked encoding, or by
  the connection closing. Conflicting or duplicate framing fields are
  `Malformed`, as in requests.
- Interim 1xx responses are skipped. A `101 Switching Protocols` is
  `Unsupported`, since the client never asks for an upgrade.
- The status line, header block, and trailers have limits, as requests do. The
  status line uses `maxHeaderBytes`.
- A body that ends before its Content-Length, or a chunked body without its
  final chunk, is `Malformed` when the reader reaches the gap.

Both functions work over any `Reader` and `Writer`, with no `Net` dependency,
so memory fixtures and a future mock can drive them.

## URLs

A new top-level `Url` module. URLs aren't HTTP-specific, so the module isn't
under `Http`.

```fango
type Url =
    { scheme : Maybe String
    , userinfo : Maybe String
    , host : Maybe String
    , port : Maybe Int
    , path : String
    , query : Maybe String
    , fragment : Maybe String
    }

parse : String -> Maybe Url
toString : Url -> String
resolve : Url -> Url -> Url
requestTarget : Url -> String

percentEncode : Component -> String -> String
percentDecode : String -> Maybe String

withQuery : List (String, String) -> Url -> Url
queryParams : Url -> List (String, String)
appendPath : List String -> Url -> Url
```

- `parse` accepts an RFC 3986 URI reference, absolute or relative. A relative
  reference has no scheme and possibly no host, and is representable because
  `baseUrl` resolution and redirect `Location` headers need it.
- The scheme and a registered host name are lowercased. An IPv6 literal is
  stored without its brackets, which is the form `Net.dial` needs; `toString`
  adds them back. Hosts must be ASCII: internationalized names (IDNA) are out
  of scope, and a non-ASCII host fails to parse. A port must be 0–65535.
- `path`, `query`, and `fragment` keep their encoded form exactly as written,
  without the `?` or `#`. Then `toString (parse s)` gives back `s` for any
  valid input, so a signed URL survives a round trip. `query = Just ""`
  (a bare `?`) and `query = Nothing` stay distinct.
- `resolve base reference` implements RFC 3986 §5.2, dot-segment removal
  included. It serves both `baseUrl` and redirects.
- `requestTarget` gives the origin-form target the client sends: the path
  (`/` when empty) followed by `?query` when present. The fragment is never
  sent.
- `percentEncode` takes the component being encoded, which decides the
  allowed character set: a path segment, a query key or value, or a fragment.
  `percentDecode` returns `Nothing` for a malformed escape or invalid UTF-8.
- `withQuery` appends encoded pairs to any existing query. It encodes spaces
  as `%20`, which every server accepts. `queryParams` decodes the query and
  also reads `+` as a space, because HTML forms produce it.
- `appendPath` encodes each segment, `/` included, and joins it to the path.

The client rejects a URL with `userinfo` (`InvalidUrl`), so credentials can't
end up in URLs, logs, or redirect chains. Callers use `withBearer` or an
`Authorization` header instead. Only `http` and, after HC4, `https` are
accepted schemes.

`Http.Server.Route` keeps its stricter decoding rules (no encoded `/` or `\`
in a segment) on top of `Url.percentDecode`, and its own percent-decoding is
removed. `Route.path` can use `Url.parse` for absolute-form targets.

## The `Http` effect

User code has the row `{Http, Fail Client.Error}`, plus
`Fail Client.Unexpected` when it uses the helpers, and no `IO`. `IO` appears
only where `run` is called, the same rule [Async](reference/library-async.md)
follows. Spawned tasks inherit the handler, so parallel requests need no setup.
The effect is declared in `Http.Client` and exposed under the name `Http`. HC2
must confirm that this name doesn't clash with the `Http` module qualifier, as
in `Http.Header`. If it does, the effect moves into the `Http` module.

The effect is a byte transport. HTTP itself lives entirely in library code
running in the caller's context. The operations are private:

```fango
type Endpoint = { secure : Bool, host : String, port : Int, connectTimeoutMs : Int }

effect Http
    connect : Endpoint -> Result Error Connection
    transmit : Connection -> Bytes -> Int -> Result Error ()           -- bytes, write timeout
    receive : Connection -> Int -> Int -> Result Error (Maybe Bytes)  -- max bytes, read timeout; Nothing at end
    release : Connection -> Bool -> Result Error ()                   -- reusable?
    currentConfig : () -> Config
```

The library opens and closes each connection with `Runtime.Scope.bracket`.
Inside it, `Writer.over` and `Reader.over` turn `transmit` and `receive` into
the buffered `Writer` and `Reader` that `writeRequest` and `withResponse` use.

Every operation returns a `Result Client.Error x`, and the public functions
apply `Fail.fromResult` where the caller runs. A failure raised inside a handler
clause would go to the handler outside `run`, past the caller's local
`attempt`. All operations are resumptive, since an effect can't mix resumptive
and abort-only operations.

Why handles rather than one operation that takes a callback:

- A resumptive clause must end with a tail `resume`, so it can't hold a
  connection open around the rest of the computation.
- An operation signature can't introduce its own row variable, so it can't
  take the caller's body writer or response callback.

With handles every operation is monomorphic. A streaming request body runs in
the caller's code between `transmit` calls, so it can use any effect in the
caller's row, such as a database cursor.

Why a byte transport rather than HTTP-level operations (open a request, write
body bytes, receive a head, pull body bytes):

- Framing, header merging, URL resolution, decompression, and redirects run
  once, in library code, under every handler. Under a mock they are tested
  exactly as they run in production.
- A handler only needs to move bytes, so a test handler is small: it answers
  `receive` with canned bytes.
- `run`'s handler needs no HTTP state, so it is stateless until the connection
  pool.
- With message-level operations, `run` would have to remember the framing
  state (buffered bytes, the rest of the current chunk, the remaining
  Content-Length) between one "read body bytes" call and the next. Fango has
  no ordinary state that outlives an operation: `Runtime.Local` cells last
  only for their own scope. The state would have to sit in a table in the
  handler, which concurrent tasks would race on, or in Go inside the handle,
  which would mean a second framing implementation beside the server's. With a
  byte transport it lives in `Reader.over` in the caller's code, which lasts
  exactly as long as the exchange.
- `mock` exercises the real server. It parses requests with
  `Server.withRequest` and writes responses with `Server.writeResponse`, so
  client tests also cover the server's framing.

The costs:

- A handler wrapping the client sees bytes, not requests, so it can't log or
  measure requests by itself. If interception becomes necessary, the library
  can send a monomorphic notification operation after each response, carrying
  the method, URL, status, and timing. `run` would ignore it and a wrapping
  handler could observe it.
- HTTP/1.1 framing is built into the library above the effect. Multiplexed
  HTTP/2 would need a different transport layer.

`Connection` is an opaque type with private constructors, never a raw `Int`:

```fango
type Connection = Socket Net.Connection | Memory Int
```

`run` uses `Socket`, which carries the live connection, so its handler keeps no
table of handles. That matters because spawned tasks share the handler's
activation, and the [effects reference](reference/effects.md#stateful-handlers)
leaves operation-level serialization to the handler. `Memory` exists so a
handler without `IO` can create handles. The stub test transport and the
future mock key their in-memory buffers by that `Int`, and must protect those
buffers with an explicit synchronized reference if tests run requests in
parallel.

**Config travels with each call, not in the handler.** The public functions
read `currentConfig` once at the start of a request and pass what the
transport needs explicitly: the endpoint and connect timeout to `connect`, and
the read or write timeout to each `transmit` and `receive`. Everything else
from the config (base URL, headers, user agent, limits, redirects,
decompression) is applied in library code. Only this way do `configure`
overrides reach the transport: `run`'s handler sits outside every `configure`
and sees only the global config.

Timeouts are per operation. `readTimeoutMs` bounds the wait for each
`receive`, and `writeTimeoutMs` each `transmit`. They are idle timeouts, not
a limit on the whole request. The handler sets the `Net` deadline before each
call.

## Public API

```fango
{-# scoped s #-}
send : Request e -> (Response s ->{s} a) ->{Http, Fail Error | e} a
fetch : Request e ->{Http, Fail Error | e} Reply

getText : String ->{Http, Fail Error, Fail Unexpected} String
getBytes : String ->{Http, Fail Error, Fail Unexpected} Bytes
getJson : Decode a => Type a -> String ->{Http, Fail Error, Fail Unexpected} a
postJson : (Encode b, Decode a) => Type a -> String -> b ->{Http, Fail Error, Fail Unexpected} a
expectSuccess : Reply ->{Fail Unexpected} Reply

url : String ->{Fail Error} Url
get : Url -> Request e
post : Url -> Body e -> Request e
withHeader : String -> String -> Request e -> Request e
withBearer : String -> Request e -> Request e

run : Config -> (() ->{Http, Fail Error | e} a) ->{IO, Fail Error | e} a
```

- `send` streams the response body inside its callback, the way `withRequest`
  scopes the server's request body. The `scoped` pragma binds `s` to a fresh
  local permission plus the residual row `{Http, Fail Error | e}`. The
  callback can make further requests and fail. The permission keeps the
  response `Reader` from escaping the callback, so it can't be read after the
  connection closes. A result that doesn't depend on the reader, such as
  decoded JSON, can leave. See [scoped callbacks](reference/functions.md#scoped-callbacks).
  HC2 prototypes this row first.
- `fetch` buffers the body up to `maxBodyBytes`. Beyond that it raises
  `Protocol BodyTooLarge`.
- `send` and `fetch` return any status.
- The helpers take a string, resolve it against `baseUrl`, and fail with
  `BadStatus` on a non-2xx status, after buffering the error body up to
  `maxBodyBytes`.
- `getJson` and `postJson` decode the response as it streams, through
  `Json.read` over a reader limited to `maxBodyBytes`, without buffering it
  first. A decoding failure is `BadBody`. They set `Accept: application/json`
  unless it's already set.
- `postJson` streams the encoded value into the request body with
  `Json.write` and sets `Content-Type: application/json` unless it's already
  set. The body is never built as a string.
- `expectSuccess` applies the helpers' status rule to a `fetch` result. A
  `send` callback can apply the same rule to a buffered reply.
- `url` parses a string and raises `InvalidUrl` when it doesn't parse;
  resolution and the scheme check happen when the request is sent.
- Record update works on any `Request`.

## Config and scoped overrides

```fango
type Config =
    { baseUrl : Maybe Url
    , headers : List Header
    , userAgent : String
    , decompress : Bool
    , connectTimeoutMs : Int
    , readTimeoutMs : Int
    , writeTimeoutMs : Int
    , maxHeaderBytes : Int
    , maxBodyBytes : Int
    , maxRedirects : Int
    }

defaultConfig : Config
configure : (Config -> Config) -> (() ->{Http | e} a) ->{Http | e} a
```

`defaultConfig` has no base URL and no headers, the user agent `fango`,
decompression on, a 10 s connect timeout, 30 s read and write timeouts, a
64 KiB header limit, a 10 MiB body limit, and 10 redirects. These are starting
values to revisit in HC2.

Policy comes in three layers:

1. `run` or `mock` sets the global config.
2. A module wraps its calls in `configure`.
3. A single request adds its own headers.

A per-request timeout is `configure` around that one call, so `Request` has no
configuration fields.

```fango
withGithub action =
    Client.configure
        { c ->
            { c | baseUrl = Url.parse "https://api.github.com"
                , headers = Http.setHeader "Accept" "application/vnd.github+json" c.headers }
        }
        action
```

`configure` installs an inner `Http` handler. Its `currentConfig` clause
answers `adjust` applied to the outer config, and its other clauses hand their
operations to the outer handler unchanged. A clause may use an enclosing
handler of the same effect; see [handler clauses](reference/effects.md#handler-clauses-and-abort-routing).
Overrides nest, spawned tasks inherit them, and they behave the same under
`mock`, so tests also cover the base URL and headers a module sets.

Header sources merge in this order, each one replacing same-named headers
from the ones before it, with names compared ignoring ASCII case:

1. Headers the client adds by default: `User-Agent` from `userAgent`, and
   `Accept-Encoding: gzip` when `decompress` is on.
2. `Config.headers`.
3. The request's own headers.

A relative request URL is resolved against `baseUrl`. Without a base URL, a
relative URL fails with `InvalidUrl`.

## Request and response bodies

Both directions use one body type:

```fango
type Body e = Empty
            | BytesBody Bytes
            | StreamBody (Writer e ->{e} ())
            | SizedBody Int (Writer e ->{e} ())
```

A streaming body is pushed. The body's code writes into a `Writer` as it
produces data, and `e` is the row of whoever built the body. For a client
request that is the caller of `send`; for a server response it is the handler.
The body can therefore run database queries, read files, or perform any other
effect allowed there between writes. Writes block on the socket, so a slow
peer also slows the producer, and memory stays at one buffer whatever the
payload size.

That covers two main cases without holding the payload in memory.

**Generated content.** A body writes its output as it produces it. For
example, it can stream database rows as a JSON array:

```fango
rows = StreamBody { w ->
    Json.withWriter w {
        Json.array { item -> Db.eachRow query { row -> item (toRecord row) } }
    } |> Fail.fromResult
}
Client.send (Client.post (Client.url "https://example.com/import") rows) { r -> r.status }
```

This needs JSON changes, part of HC2:

- `Json.withWriter` and `Json.withTextWriter` currently take an action of type
  `() ->{Emit, Fail Error} a`, which is closed, so the action can't read the
  next row. They change to `() ->{Emit, Fail Error | e} a`, and `write` and
  `writeText` keep their types. With a `Fail` in `e` the two-`Fail` question
  from [errors](#errors) comes up again, and HC2 checks it here too.
- `Json.array` emits a JSON array whose length isn't known in advance:

  ```fango
  array : Encode a => ((a ->{Emit, Fail Error | e} ()) ->{Emit, Fail Error | e} b) ->{Emit, Fail Error | e} b
  ```

  The callback receives an `item` function that writes the separators and
  encodes one element.
- `Json.object` does the same for objects: the callback receives
  `field : String -> (() ->{Emit, Fail Error | e} ()) ->{Emit, Fail Error | e} ()`,
  so `{ "rows": [...], "count": n }` can stream its array before writing the
  count.

**Another IO handle.** A body copies from a source chunk by chunk:

```fango
fileBody : String ->{IO, Fail File.Error} Body {IO, Fail File.Error | e}
```

`Http.fileBody path` reads the file's size and returns a `SizedBody` whose
writer opens the file with `File.withFile` and copies it chunk by chunk. The
file stays open only while the body is written. This needs a new
`File.size : String ->{IO} Result Error Int`. If the file changes size between
that call and the copy, the length check below catches it. Using `fileBody`
puts `IO` in the row of the code that sends the request, which is consistent
with the rule that `IO` appears only where code touches the host.

A general copy helper, `Writer.copy : Reader e -> Writer e ->{e} ()`, serves
any reader. `Writer` imports `Reader` for it, which is safe because `Reader`
doesn't import `Writer`.

**Known and unknown length, chunk sizes, and failures.** `SizedBody`, chunk
collection, and the rule that a body failing partway leaves its message
truncated are [implemented](reference/library-http.md#messages-and-bodies) for
the server. The client follows the same rules through `Http.Wire`. A request
body that raises any abort (a `Fail` in the caller's row, for instance) has the
connection closed by the bracket, and the abort reaches the caller of `send`
unchanged, through `e`, not wrapped in `Client.Error`. Retrying, or keeping a
database consistent, is the caller's responsibility.

**Proxying.** A server handler can stream its request body into a client
request and the client's response back:

```fango
{ request ->
    Client.send (Client.post target (StreamBody { w -> Writer.copy request.body w })) { response ->
        Response { status = response.status, headers = forwarded response.headers
                 , body = StreamBody { w -> Writer.copy response.body w } }
    }
}
```

The response body here reads the client's reader after `send`'s callback has
returned. So as written this example is rejected, and correctly: the client
connection would be closed by then. A working proxy writes the response from
inside the callback. The server then needs a way to write a response while
the handler is still running, which it doesn't have today. HC2 adds a proxy
fixture that streams the request direction with bounded memory and buffers
the response, and records what the response direction would need. It also
checks that the server's request-reader permission and `send`'s callback
permission can be combined in one handler.

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
- `release conn reusable` returns a connection to the pool when the response
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

The client side runs its real `writeRequest` and `withResponse`, so framing is
tested in both directions without sockets or `IO`. The mock buffers whole
messages, which is fine for tests. `Http.Server.Route.dispatch` tables work as
mocks unchanged. A test records the requests it receives through its own
effects in the handler's row, such as State or Writer. A `Protocol` error from
the server side (a request the parser rejects) becomes the error response the
real server would send, so the client sees a 4xx just as it would against a
real server.

The mock waits for a test framework (see HC3). HC2 keeps it possible: the
transport is byte-level, `Connection` has a `Memory` case, framing doesn't
depend on `Net`, and policy runs above the effect. HC2 also adds a minimal
stub transport fixture.

## Milestones

The milestones are listed in the order they are expected to land. The IDs
follow allocation order, not that order.

| Milestone | Depends on |
| --- | --- |
| HC1 Module split | — (DONE) |
| HC8 Runner-handled effects in scoped callbacks | HC1 (DONE) |
| HC6 URL | — |
| HC2 Client core over plain HTTP | HC1, HC6 |
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

This milestone covers:

- the `Url` module from [URLs](#urls), with fixtures for parsing, round trips,
  the RFC 3986 §5.4 resolution examples, IPv6 literals, and percent-encoding
  each component;
- `Http.Server.Route` decoding through `Url.percentDecode`, keeping its
  rejection of encoded slashes;
- a reference page for `Url`.

### HC2 Client core over plain HTTP

Prototype the `send` row first, then check two language questions before
building on them: whether a caller can catch either of two `Fail` effects,
and whether the `Http` effect name clashes with the `Http` qualifier.

This milestone covers:

- `writeRequest` and `withResponse`, with memory fixtures for each framing
  case, interim responses, truncated bodies, and limits;
- `Client.Error` and `Unexpected`;
- the byte-transport `Http` effect, `Connection`, `Config`, `configure`, and
  `run`, with config passed to each transport call;
- `Net.dialTimeout`, a dial with a connect timeout (`Net.dial` has none), and
  mapping an expired deadline to `Timeout`;
- URL resolution against `baseUrl`, header merging, and rejecting managed
  headers;
- `send`, `fetch`, `url`, the helpers, and `expectSuccess`;
- one connection per request, sent with `Connection: close`;
- the JSON streaming changes: the open row on `withWriter` and
  `withTextWriter`, `Json.array`, and `Json.object`;
- `File.size`, `Http.fileBody`, and `Writer.copy`.

Tests:

- A stub transport fixture: a hand-written `Http` handler with `Memory`
  handles and no `IO` that answers `receive` with canned bytes and can return
  any `Client.Error`. It covers policy and failure paths (refused connection,
  timeout, truncated response) without sockets.
- A loopback test against `Http.Server`.
- A streaming fixture that sends a large generated JSON body and a file body,
  checking that memory stays bounded.
- The proxy fixture from [bodies](#request-and-response-bodies).

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
- Writing a server response from inside a handler while it is still running,
  which a streaming proxy needs.
