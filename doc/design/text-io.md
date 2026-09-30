# Text over byte streams

[Encoding](../../stdlib/Encoding.fango), [Text.Reader](../../stdlib/Text/Reader.fango),
and [Text.Writer](../../stdlib/Text/Writer.fango) layer text over the existing
byte interfaces. Their observable contracts are in the
[text I/O reference](../reference/library-text-io.md).

Encoding selection belongs to an adapter, not the byte buffer. Byte readers
continue to frame files and protocols without knowing whether their payload
is text. Text operations count Unicode scalars; positions count consumed
bytes. The opaque encoding type selects UTF-8 or Latin-1 behind a common
prefix decoder and whole-string encoder. The native UTF-8 scalar probe
distinguishes end, incomplete input, malformed input, and a valid U+FFFD;
the sentinel codes stay private to Encoding. JSON has no encoding native.

A text reader owns a scoped consumed-byte counter and borrows its parent
buffer. It stages characters and byte widths without skipping the parent,
committing one skip only after a successful read. Exact-read refusal,
delimiter overflow, and decoding failure therefore preserve the parent
position. Incomplete characters stay in the byte buffer across refills.
There is no separate decoded buffer to reconcile when the adapter ends.
The parent must not be advanced independently during an adapter scope.

Decoder callbacks return error values. Public read operations raise the
decoding failure at the operation's call site, so a locally installed handler
can catch it without aborting the adapter scope. Result-returning character
operations let domain parsers translate decoding failures while preserving
unrelated source effects. The reader's row parameter describes borrowed
cursor effects; read signatures explicitly add the decoding failure.

A text writer either encodes into a borrowed byte writer or collects immutable
strings. The whole string is encoded before each emit, preserving per-write
atomicity for encoding failures. The UTF-8 constructor is total with respect
to encoding; explicitly selected encodings carry their failure in the writer
row. Result-returning writes let domain encoders translate those failures.
Scopes retain a local permission even when no additional buffer is needed,
so text adapters and their callbacks cannot outlive their consumer.
Nested UTF-8 encoders can borrow a text writer without a final flush, preserving
the byte writer owner's buffering boundary.

String construction from character lists and string chunks uses shared
runtime helpers for typed and interpreter-erased lists. Both helpers size the
output before building it. String.span slices at a validated byte boundary
after scanning characters, avoiding a second scan of the unvisited suffix.
