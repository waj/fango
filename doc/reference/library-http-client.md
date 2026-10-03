# HTTP/1.1 client

Sending requests over HTTP/1.1 and HTTPS, streaming bodies in both directions,
and answering requests without sockets in tests.

[Reference index](../reference.md). Source: [Http.Client](../../stdlib/Http/Client.fango).
The client shares [bodies, headers, and protocol errors](library-http.md) with
the server and takes [URLs](library-url.md). The bundled API is experimental.

## Running requests

Code that makes requests performs the `Http` effect and `Fail Client.Error`,
and needs no `IO`. `run` connects `Http` to sockets where the program starts
it. Failures pass through `run` to whichever handler the program installs:

```fango
import Fail
import Http.Client as Client

main() =
    print (Fail.attempt { Client.run { Client.getText "http://example.com/" } })
```

```fango
run : (() ->{Http | e} a) ->{IO | e} a
configure : (Config -> Config) -> (() ->{Http | e} a) ->{Http | e} a
stub : (Bytes ->{e} Result Error Bytes) -> (() ->{Http | e} a) ->{e} a
```

`run` starts from `defaultConfig`.

`run` keeps idle connections for reuse, up to 8 per scheme, host, and port,
for up to 60 s each, and closes them when it returns. Tasks that inherit the
handler share them. A connection returns to the pool when its response was
read to the end, both sides speak HTTP/1.1, neither said `Connection: close`,
and the body was framed by length or chunks. A body the caller leaves unread is
drained if 64 KiB or less remains, and otherwise its connection is closed. An
idle connection the server has closed is detected before reuse; if one still
fails before any response byte arrives, an idempotent request whose body can
be sent again is retried once on a new connection.

`configure adjust action` runs `action` with `adjust` applied to the
configuration in effect. Overrides nest and apply under every transport,
`stub` included. Program-wide settings, such as a base URL, are a `configure`
just inside `run`; a per-request timeout is a `configure` around that request.

`stub respond action` answers each request without sockets: `respond` receives
the request's bytes exactly as the client wrote them and returns the bytes of a
response, or an `Error` to fail the request with. It may use the caller's
effects, for example to record requests. Under `stub`, requests run one at a
time and the configuration starts from `defaultConfig`.

## Configuration

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
    , caFile : Maybe String
    }
```

`defaultConfig` has no base URL and no headers, the user agent `fango`,
decompression on, a 10 s connect timeout, 30 s read and write timeouts, a 64 KiB header limit, and a
10 MiB body limit, up to 10 redirects, and no extra certificate authorities.

A request URL without a scheme is resolved against `baseUrl` with
[`Url.resolve`](library-url.md#resolution). Without a base URL it fails with
`InvalidUrl`, as does a URL carrying user information, an unsupported scheme,
or a missing host; only `http` and `https` are accepted. Credentials belong in
a header (`withBearer`).

Headers come from three sources, each replacing same-named headers (compared
ignoring ASCII case) from the ones before it: `User-Agent` from `userAgent`
and, with `decompress`, `Accept-Encoding: gzip`; then `Config.headers`; then
the request's own. The client writes `Host`,
`Content-Length`, `Transfer-Encoding`, and `Connection` itself; setting one in
the configuration or a request fails with `Protocol (InvalidMessage …)`.

`https` URLs use TLS. The client sends the host name for SNI and verifies the
server's certificate for it against the system's roots, plus the certificates
in the PEM file named by `caFile` when it is set; a failed verification is a
`Transport` error. The connect timeout covers the handshake. The read and
write timeouts bound each wait for the socket, not the whole request. `maxHeaderBytes` limits the status line and the header block.
`maxBodyBytes` limits a body the client reads into memory.

## Requests and responses

```fango
type Request e  = { method : String, url : String, headers : List Header, body : Body e }
type Response e = { url : Url, status : Int, headers : List Header, body : Reader e }
type Reply      = { url : Url, status : Int, headers : List Header, body : Bytes }

get : String ->{Http, Fail Error} Reply
post : String -> Body {Http, Fail Error | e} ->{Http, Fail Error | e} Reply

request : String -> String -> Request e
withBody : Body e -> Request e -> Request e
withHeader : String -> String -> Request e -> Request e
withBearer : String -> Request e -> Request e

{-# scoped s #-}
send : Request {Http, Fail Error | e} -> (Response s ->{s} a) ->{Http, Fail Error | e} a
fetch : Request {Http, Fail Error | e} ->{Http, Fail Error | e} Reply
```

`get url` and `post url body` send a request and read the whole response.
Anything else starts from `request method url`, which has no headers and an
empty body, and goes to `send` or `fetch`:

```fango
Client.request "PUT" "/items/7"
    |> Client.withBody (BytesBody payload)
    |> Client.withBearer token
    |> Client.fetch
```

The URL is parsed when the request is sent; one that does not parse fails with
`InvalidUrl`.

`send request use` writes the request and streams the response body to `use`,
which may read it only while it runs: the reader cannot escape the callback, so
it is never read after the connection closes. A value that does not depend on
the reader, such as decoded JSON, can leave. `fetch`, `get`, and `post` read
the whole body, up to `maxBodyBytes`, beyond which they fail with
`Protocol BodyTooLarge`. All of them return any status.

A request body is a [`Body`](library-http.md#messages-and-bodies). Its row
includes `Http` and `Fail Error` because it runs between the client's writes;
it may also use any other effect of the caller, such as a database cursor or a
file, so a large payload is produced while it is sent. `Empty` sends no framing
headers for GET, HEAD, OPTIONS, TRACE, and DELETE, and `Content-Length: 0`
otherwise. A body that fails partway leaves the request truncated, closes the
connection, and its failure reaches the caller of `send` unchanged.
[`Http.fileBody`](library-http.md#messages-and-bodies) streams a file as a
sized body, and [`Json.withWriter`](library-json.md#text-adapters-and-custom-encoders)
with `Json.array` streams generated JSON.

The response is framed as follows. A response to HEAD, and a 204 or 304, has
no body. Any other is framed by Content-Length, by chunked encoding, or by the
connection closing. Interim 1xx responses are skipped; `101 Switching
Protocols` fails with `Unsupported`. A body that ends early fails with
`Malformed` when the reader reaches the gap. HTTP/1.0 status lines are
accepted. `withResponse` parses a response from any reader, the way
[`withRequest`](library-http.md#parsing-and-framing) parses a request.

## Redirects

`send` follows 301, 302, 303, 307, and 308 responses before its callback runs,
so the callback sees only the final response, whose `url` says where it came
from. Following one more than `maxRedirects` fails with `TooManyRedirects`;
`maxRedirects = 0` returns redirects as they are.

- `Location` is resolved against the current URL. A redirect without a usable
  `Location` is returned as it is.
- A 303, and a 301 or 302 answering a method other than GET or HEAD, continues
  as a GET without a body. Other redirects repeat the method and body.
- A request whose body must be repeated but was a stream, which cannot be sent
  twice, gets the redirect response itself.
- A redirect from `https` to another scheme is returned, not followed.
- Once a redirect leaves the original scheme, host, and port, `Authorization`
  and `Proxy-Authorization` are no longer sent, whether they came from the
  request or the configuration.

## Compression

With `decompress` on, a response with `Content-Encoding: gzip` is decoded as it
streams with [`Http.GZip.decoding`](library-http.md#routing-and-gzip), and its
`Content-Encoding` and `Content-Length` headers are removed, since they no
longer describe the body. A HEAD, 204, or 304 response is left alone, as is any
other content coding. When the configuration or the request sets
`Accept-Encoding` itself, the caller asked for specific encodings and gets the
body as sent. `maxBodyBytes` counts decoded bytes wherever the client reads a
body into memory; a `send` callback reading the stream itself has no limit.

```fango
compressRequest : Request e -> Request e
```

`compressRequest` sends the body gzip-compressed with `Content-Encoding: gzip`.
Few servers accept that unasked, so it is never automatic.

## Helpers

```fango
getText : String ->{Http, Fail Error} String
getBytes : String ->{Http, Fail Error} Bytes
getJson : Decode a => Type a -> String ->{Http, Fail Error} a
postJson : (Encode b, Decode a) => Type a -> String -> b ->{Http, Fail Error} a
expectSuccess : Reply ->{Fail Error} Reply
```

The helpers return the body as data, so they expect a 2xx status and fail with
`BadStatus` otherwise. `getText` decodes the
body as UTF-8, replacing invalid sequences. `getJson` and `postJson` set
`Accept: application/json` unless the request has it and decode the response
while it streams, within `maxBodyBytes`; `postJson` streams the encoded value
as a chunked body with `Content-Type: application/json`. `expectSuccess`
applies the same status rule to a `fetch` result.

## Errors

```fango
type Error = InvalidUrl String | Transport Net.Error | Timeout | Protocol Http.Error
           | TooManyRedirects | BadStatus Reply | BadBody Json.Error
```

`Error` is what can go wrong with a request: a URL the client cannot use, a
[`Net.Error`](library-io.md#net) from connecting, reading, or writing, an
expired timeout (never a `Transport` of kind `TimedOut`), a
[protocol error](library-http.md#errors) in the request or response, or too
many redirects. Only the helpers and `expectSuccess` raise `BadStatus`, which
carries the whole reply, body included, since an error response's body often
explains it, and `BadBody`, which carries the JSON decoding error.

Requests fail through `Fail Error`, handled like any other failure, with
`Fail.attempt` or a handler of the program's own. Where the code also performs
another `Fail`, such as `Fail IO.Error`, the handler must name the one it
takes; see [effect rows](effects.md#row-inclusion-and-callback-compatibility).
