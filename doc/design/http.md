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
