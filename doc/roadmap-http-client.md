# Roadmap: HTTP client

A client for HTTP/1.1, and later HTTPS, that is consistent with the
[server](reference/library-http.md). It provides helpers for common requests
and can be mocked in tests. This is a proposal: none of it is implemented
yet. The [HTTP roadmap](roadmap-io.md) tracks the server follow-ups.

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
| `Http` | `Header`, `Version`, `Body e = Empty \| BytesBody Bytes \| StreamBody (Writer e ->{e} ())` (renamed from `ResponseBody`), the header helpers (`getHeader`, `getHeaders`, `hasHeader`, `setHeader`, `removeHeader`, `equalAscii`), and the `Protocol` effect with its error |
| `Http.Wire` | Low-level framing pieces both directions use: header-block parsing, the chunked reader and writer, field validation, and the choice between Content-Length and chunked framing |
| `Http.Server` | `Request e` (`body : Reader e`) and `Response e` (`body : Body e`), `withRequest`, `writeResponse`, `Config`, `serve` |
| `Http.Client` | Client `Request e`, `Response e`, `Reply`, `Error`, `Config`, the `Http` effect, `writeRequest`, `withResponse`, `run`, `configure`, `mock` |
| `Http.Route`, `Http.GZip` | Server middleware, typed over `Http.Server.Request` and `Http.Server.Response` |

Server handlers change from `Http.Request e ->{e} Http.Response e` to
`Server.Request e ->{e} Server.Response e`. Server behaviour does not change.
The modules are layered without cycles: `Http`, then `Http.Wire`, then
`Http.Server`, then `Http.Client`. The client imports the server module only
for `mock`.

Should `Http.Error` also become a plain union like the client `Error` below?
That would touch the server's status mapping and `Http.Route`.

## Client types

```fango
type Request e  = { method : String, url : String, headers : List Header, body : Body e }
type Response e = { status : Int, headers : List Header, body : Reader e }   -- streamed, scoped
type Reply      = { status : Int, headers : List Header, body : Bytes }      -- buffered
type Error      = InvalidUrl String | Connect Net.Error | Timeout | InvalidResponse String
                | TooLarge | BadStatus Int | BadBody Json.Error
```

`Error` is a plain union. Each constructor carries only the data its case needs.
The caller already knows which URL it requested, so the error doesn't repeat it.
As in the server, the method is a `String` and the status an `Int`. The client
manages `Host`, `Content-Length`, and `Transfer-Encoding` itself and rejects
those headers from callers, the way `writeResponse` does.

`withResponse` frames the response as follows:

- HEAD requests and 1xx, 204, and 304 responses have no body.
- Any other response is framed by Content-Length, by chunked encoding, or by
  the connection closing.
- Interim 1xx responses are skipped.
- The status line, header block, and body have limits, as requests do.

## The `Http` effect

User code has the row `{Http, Fail Client.Error}` and no `IO`. `IO` appears
only where `run` is called, the same rule [Async](reference/library-async.md)
follows. Spawned tasks inherit the handler, so parallel requests need no setup.
The effect is declared in `Http.Client` and exposed under the name `Http`. HC2
must confirm that this name doesn't clash with the `Http` module qualifier, as
in `Http.Header`. If it does, the effect moves into the `Http` module.

The effect's operations are private and work through handles:

- open a request, given its head
- write request body bytes
- finish the request and receive the response head
- pull response body bytes
- close
- read the current `Config`

Every operation returns a `Result Client.Error x`, and the public functions
apply `Fail.fromResult` where the caller runs. A failure raised inside a handler
clause would go to the handler outside `run`, past the caller's local
`attempt`.

Why handles rather than one operation that takes a callback:

- A resumptive clause must end with a tail `resume`, so it can't hold a
  connection open around the rest of the computation.
- An operation signature can't introduce its own row variable.

With handles every operation is monomorphic. The library opens and closes each
connection with `Runtime.Scope.bracket`. Handles are opaque types, never raw
`Int`s.

## Public API

```fango
{-# scoped s #-}
send : Request e -> (Response s ->{s} a) ->{Http, Fail Error | e} a
fetch : Request e ->{Http, Fail Error | e} Reply

getText : String ->{Http, Fail Error} String
getBytes : String ->{Http, Fail Error} Bytes
getJson : Decode a => Type a -> String ->{Http, Fail Error} a
postJson : (Encode b, Decode a) => Type a -> String -> b ->{Http, Fail Error} a

get : String -> Request e
post : String -> Body e -> Request e
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
- `fetch` buffers the body up to `maxBodyBytes`.
- `send` and `fetch` return any status. The `get…` and `post…` helpers fail
  with `BadStatus` on a non-2xx status.
- Record update works on any `Request`.

## Config and scoped overrides

```fango
type Config =
    { baseUrl : Maybe String
    , headers : List Header
    , userAgent : String
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
            { c | baseUrl = Just "https://api.github.com"
                , headers = Http.setHeader "Accept" "application/vnd.github+json" c.headers }
        }
        action
```

`configure` installs an inner `Http` handler. Its config clause answers
`adjust` applied to the outer config, and its other clauses hand their
operations to the outer handler. A clause may use an enclosing handler of the
same effect; see [handler clauses](reference/effects.md#handler-clauses-and-abort-routing).
Overrides nest, spawned tasks inherit them, and they behave the same under
`mock`, so tests also cover the base URL and headers a module sets.

Headers are merged by name, ignoring ASCII case. A request header replaces a
configured header with the same name, and configured headers replace the
defaults. `userAgent` is sent unless the request sets `User-Agent`. A relative
request URL is resolved against `baseUrl`. Without a base URL, a relative URL
fails with `InvalidUrl`.

## Mocking

```fango
{-# scoped s #-}
mock : Config -> (Server.Request s ->{s} Server.Response s)
    -> (() ->{Http, Fail Error | e} a) ->{e} a
```

The mock takes a server handler. For each request it:

1. serializes the client request with `writeRequest`
2. parses it with `Server.withRequest` and runs the handler
3. serializes the handler's response with `Server.writeResponse`
4. parses that with `withResponse`

This exercises the real framing in both directions without sockets or `IO`.
`Http.Route.dispatch` tables work as mocks unchanged. A test records the
requests it receives through its own effects in the handler's row, such as
State or Writer.

## Milestones

### HC1 Module split

Rename `ResponseBody` to `Body`, add `Http.Wire`, and move the server message
types and codec into `Http.Server`. Update Route, GZip,
`examples/http_server.fango`, and the `testdata/run/http_*.fango` fixtures.
Server goldens don't change. Update the HTTP reference and design pages.

### HC2 Client core over plain HTTP

This milestone covers:

- `writeRequest` and `withResponse`, with memory fixtures
- parsing `http://` URLs and resolving them against the base URL
- the `Http` effect, `Config`, `configure`, and `run`
- `send`, `fetch`, and the helpers
- one connection per request, sent with `Connection: close`

A loopback test runs the client against `Http.Server`. Prototype the `send`
row first.

### HC3 Mock

`mock`, plus a test that uses Route handlers as the mock and checks the
requests they receive.

### HC4 TLS

A `Net` native over Go's `crypto/tls` that uses the system roots and SNI,
together with `https://` URLs. Also add a dial with a connect timeout. TLS for
the server stays deferred.

### HC5 Redirects and keep-alive

Follow redirects up to `maxRedirects`. A 303 is retried as GET, and a 307 or
308 is not followed when the request body is a stream. Add a connection pool
for each `run`.

## Open questions

- Should sockets close when an `Async` task is cancelled (using
  `Async.contextToken`), or should synchronous `Net` calls with deadlines be
  enough? The answer decides whether `run` requires `Async`.
- How should tests simulate transport failures such as a refused connection or
  a timeout? One option is a lower-level stub returning `Result Error Reply`.
- Automatic gzip decoding needs a decompressing reader in `Http.GZip`.
