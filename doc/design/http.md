# HTTP over streams

[Design index](../design.md). The [HTTP reference](../reference/library-http.md)
owns the API and observable framing rules.

`Http` runs the same parser and serializer over memory or sockets through
`Reader` and `Writer`. A scoped `withRequest` keeps the body reader inside its
callback. The concurrent server builds a reader and writer over each socket,
then parses, handles, and writes each request inside the `withRequest` callback.
The handler can return a `Response` whose streaming body reads the request;
serialization consumes that body before the request scope ends. HTTP parser
state and body framing remain in Fango. Requests without body framing use a
shared exhausted reader, so they do not allocate a per-request body cursor.
The socket adapter peeks into its reusable read buffer before allocating the
immutable result. Short requests therefore allocate for bytes received rather
than the maximum read size.

HTTP framing belongs to `Http`. The server owns socket deadlines, connection
persistence, and the worker lifetime. It accepts several connections using
`Async` tasks. Cancellation-aware `Net` operations close blocking sockets when
their task is cancelled; listener stop prevents new accepts, then active tasks
are given a grace period before cancellation.

`Http.Route` and `Http.GZip` are ordinary handlers over `Request` and
`Response`. They do not change the server's application error contract. The
application's handler must discharge its own effects, apart from the network
and protocol effects that the server provides.
