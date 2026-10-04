# Roadmap: HTTP and a concurrent server

[HTTP/1.1 framing, the concurrent server, routing, and streaming GZip](../stdlib/Http.fango)
are implemented over the existing [stream interfaces](../stdlib/Reader.fango)
and [socket layer](../stdlib/Net.fango). Memory fixtures and loopback
checks cover malformed framing, truncated bodies, oversized headers, chunked
requests, keep-alive, simultaneous clients, and GZip.

Server-side TLS and HTTP/2 are deferred; the
[client](../stdlib/Http/Client.fango) supports TLS. A policy for request bodies that an application
leaves unread is also deferred: the current server closes a connection when
its handler leaves the request body incomplete. A future API may expose a
deliberate drain policy.

The [echo example](../examples/echo.fango) uses buffered line IO. The
grep-lite comparison remains in the [example-program roadmap](roadmap-examples.md);
record its read throughput against Go on an idle host when it is built.
