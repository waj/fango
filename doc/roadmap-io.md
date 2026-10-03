# Roadmap: HTTP and a concurrent server

[HTTP/1.1 framing, the concurrent server, routing, and streaming GZip](reference/library-http.md)
are implemented over the existing [stream interfaces](reference/library-readers.md)
and [socket layer](reference/library-io.md#net). Memory fixtures and loopback
checks cover malformed framing, truncated bodies, oversized headers, chunked
requests, keep-alive, simultaneous clients, and GZip.

Server-side TLS and HTTP/2 are deferred. Client-side TLS is part of the
[HTTP client roadmap](roadmap-http-client.md#hc4-tls). A policy for request bodies that an application
leaves unread is also deferred: the current server closes a connection when
its handler leaves the request body incomplete. A future API may expose a
deliberate drain policy.

The [echo example](../examples/echo.fango) uses buffered line IO. The
grep-lite comparison remains in the [example-program roadmap](roadmap-examples.md);
record its read throughput against Go on an idle host when it is built.
