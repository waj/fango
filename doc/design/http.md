# HTTP over streams

[Design index](../design.md). The [HTTP reference](../reference/library-http.md)
owns the API and observable framing rules.

`Http` holds the types both directions share. `Http.Wire` holds the framing
they share: header blocks, body framing, and serialization after the start
line. `Http.Server` adds request lines and status handling. The same parser and
serializer run over memory or sockets through `Reader` and `Writer`. A scoped `withRequest` keeps the body reader inside its
callback. The concurrent server builds a reader and writer over each socket,
then parses, handles, and writes each request inside the `withRequest` callback.
The handler can return a `Response` whose streaming body reads the request;
serialization consumes that body before the request scope ends. HTTP parser
state and body framing remain in Fango. Requests without body framing use a
shared exhausted reader, so they do not allocate a per-request body cursor.
The socket adapter peeks into its reusable read buffer before allocating the
immutable result. Short requests therefore allocate for bytes received rather
than the maximum read size.

A streaming body is called with a writer whose row is the body's own, so it
has no room for local state. Chunk collection and the sized-body byte count
therefore keep their state in a native batch buffer owned by `Http.Wire`, like
the GZip compressor. Each batch operation's result is consumed, so no call can
be dropped. Serialization checks a message completely before writing its
first byte; the server uses that split to answer an invalid response with a
500 and to close the connection, without completing the message, when a body
fails after the head.

HTTP framing belongs to `Http.Wire`. The server owns socket deadlines, connection
persistence, and the worker lifetime. It accepts several connections using
`Async` tasks. Cancellation-aware `Net` operations close blocking sockets when
their task is cancelled; listener stop prevents new accepts, then active tasks
are given a grace period before cancellation.

`Http.Server.Route` and `Http.GZip` are ordinary handlers over `Request` and
`Response`. They do not change the server's application error contract. The
application's handler must discharge its own effects, apart from the network
and protocol effects that the server provides.

## Message types per direction

A request the server reads and a request the client writes carry opposite
bodies: the server pulls from a `Reader`, the client pushes a `Body`. A single
message type parameterized by its body would turn every server signature into
`Request (Reader s)`, since Fango has no type aliases. So `Http.Server` and
`Http.Client` each define their own `Request` and `Response`, and `Http` keeps
what both share: headers, `Body`, `Error`, and `Protocol`.

## The client transport

`Http.Client` performs one effect, `Http`, whose operations move bytes:
connect to an endpoint, transmit, receive, release, and read the
configuration. Framing, header policy, URL resolution, and response parsing run
in library code in the caller's context, so every transport (`run` over
sockets, `stub` in memory, a future mock) runs the same HTTP code.

The operations take a connection handle rather than a callback. A resumptive
clause must end in a tail `resume`, so it cannot hold a connection open around
the rest of a request, and an operation signature cannot introduce its own
row variable for the caller's body or response callback. With handles every
operation is monomorphic, and a streaming request body runs in the caller's
code between `transmit` calls with the caller's effects.

The operations are byte-level rather than message-level because framing has
state between reads: the buffered bytes, the rest of a chunk, the remaining
Content-Length. A handler has no ordinary state that outlives an operation;
keeping it in the handler would race between tasks, which share the handler's
activation, and keeping it in Go would duplicate the server's framing. At the
byte level that state lives in `Reader.over` in the caller, for exactly one
exchange. The cost is that a handler wrapping the client sees bytes, not
requests, and that HTTP/1.1 framing sits above the effect.

The `run` handler keeps no state: a `Socket` handle carries the live
connection. Each operation returns a `Result`, and the caller's code raises it,
so a failure reaches the caller's own `attempt` rather than the handler outside
`run`. Configuration is read once per request and passed to each operation
(the endpoint and connect timeout to `connect`, a timeout to each read or
write); only then do `configure` overrides, which sit inside `run`, reach the
transport.

The request is written through an unbuffered writer over `transmit`. A
`Writer.over` buffer would carry a local permission that the caller's body,
typed before the request runs, cannot accept. The shared chunked and sized
writers batch a body into 8 KiB writes instead, and the head goes out in one
write. The response is read through `Reader.over`, whose permission keeps the
reader inside `send`'s callback.
