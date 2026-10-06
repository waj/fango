# Text over byte streams

[Encoding](../../stdlib/Encoding.fango), [Text.Reader](../../stdlib/Text/Reader.fango),
and [Text.Writer](../../stdlib/Text/Writer.fango) layer text over the existing
byte interfaces. Their observable contracts are in those modules'
documentation comments.

Encoding selection belongs to an adapter, not the byte buffer. Byte readers
continue to frame files and protocols without knowing whether their payload
is text. Text operations count Unicode scalars; positions count consumed
bytes. The opaque encoding type selects UTF-8 or Latin-1 behind a common
prefix decoder and whole-string encoder. The native UTF-8 scalar probe
distinguishes end, incomplete input, malformed input, and a valid U+FFFD;
ASCII bytes return their scalar value directly, while non-ASCII input uses
the strict UTF-8 decoder. The sentinel codes stay private to Encoding.
JSON has no encoding native.

A text reader owns a scoped consumed-byte counter and borrows its parent
buffer. It stages characters and byte widths without skipping the parent,
committing one skip only after a successful read. Exact-read refusal,
delimiter overflow, and decoding failure therefore preserve the parent
position. Incomplete characters stay in the byte buffer across refills.
There is no separate decoded buffer to reconcile when the adapter ends.
The parent must not be advanced independently during an adapter scope.

Scalar reads can return their encoded byte width alongside the character.
Prefix reads snapshot one immutable byte window and run a pure scalar predicate
over it, committing a single skip. They return a complete valid prefix before
a later malformed or incomplete sequence; the next read diagnoses that sequence
at its original offset. They refill only to obtain the first complete scalar,
then stop at the current window boundary. This permits domain scanners to
retain their own grammar and diagnostic cursor without per-character state
dispatch. The text reader decodes bytes below 128 itself, since both bundled
encodings map them to the same one-byte scalars; any other byte goes through
`Encoding.decodeAt`. An encoding that is not ASCII-compatible would have to
revisit this shortcut.

An opaque `Window` snapshots the current immutable bytes and encoding without
refilling. Its buffer description also records the consumed-byte base and is
shared across derived windows; only a byte index changes during scanning.
Pure scalar and span operations advance across validated complete scalars. A
bulk `matchWindow` compares the requested valid text's encoding directly with
the immutable source bytes through `Encoding.matchAt`; for UTF-8 the text's own
bytes are compared in place, so a match allocates nothing. An exact
match is already valid encoded text, so it requires no scalar decoding or
source-string construction. Mismatch, insufficient bytes, or an unrepresentable
request declines without inspecting the suffix or advancing the reader. A
scanner can abandon a derived window without changing the reader, or extract
text between two positions from the same snapshot. `commitWindow` compares the
snapshot's absolute position with the reader's consumed-byte counter and skips
only the new prefix. Equal or earlier commits consume nothing. This permits
incremental commits against one snapshot without resnapshotting each token.

`refillWindow` first commits the supplied position, then asks the parent to
grow and returns a fresh snapshot with a source-growth flag. Unconsumed bytes,
including an incomplete scalar, remain in the parent and precede the new
chunk. A false flag proves source EOF, even if unconsumed bytes remain; the
pure window operations alone cannot establish EOF. A source failure during
refill leaves the committed prefix consumed. Subsequent scanning and commits
use the new snapshot, while older snapshots remain immutable text values.
Consumers must not independently advance the reader while using a window
cursor. [JSON](json.md) keeps a small window in its pull state and publishes
consumption at boundaries and scope exit.
Capability records holding the reader callbacks are shared by pointer in
compiled code; see [backend layouts](backend.md#representations-and-abi).

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

`Text.Builder` is foundational, importing only Runtime.Native so Basics can
declare builder-based Show and Display methods without a cycle. Versions carry an
opaque buffer and byte length as an ordinary product, so appending allocates
no version object. Storage and scalar formatting belong to its ordinary Go
sidecar. Both backends call that sidecar; compile-time adapters import the same implementation. Integer
and float rendering use bounded stack scratch rather than intermediate strings.
String and Char literal appends size their escaped output and then write directly
into reserved storage. Direct String conversions share the same formatting rules.

Show instances append through `showTo` and, for constructor arguments,
`showArgTo`. Derived instances, lists, tuples, and dictionaries thread the builder
through their children. The String-returning methods default to an empty builder
and one final copy, while scalar instances override `show` to avoid builder storage.
The blanket Display instance delegates both methods to Show, preserving those
fast paths. Bytes retains its byte-specific renderer and appends its resulting
String.

Each native buffer has fixed backing storage and an atomic append frontier.
An append claims tail space with compare-and-swap only when the frontier equals
its version's length; otherwise it copies that version's prefix into a fresh
buffer. Exhausted storage grows geometrically in a fresh buffer. Slice headers
never mutate, and bytes below a published version's length are never rewritten,
so reads require no lock even while other tasks append.

A reservation advances the frontier before writing, but the resulting version
is published only after its tail is complete. Other callers possess only
earlier published lengths, so they cannot claim or read unfinished bytes. The
private buffer-length helper is called only between a nonempty append and
construction of its resulting Builder, before that new frontier length escapes.
Empty appends keep their original version and length. `toString` copies only the
version's prefix and returns a stable immutable string.

Growing fixed storage allocates a new buffer header as well as backing bytes.
This trades a small object per growth for removing mutex operations; appending
within available capacity allocates neither a header nor intermediate text.
The focused storage benchmark in `stdlib/Text/Builder_native_benchmark_test.go`
compares the atomic implementation with mutex baselines; timings are manual
measurements, outside correctness gates.
