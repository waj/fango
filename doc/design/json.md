# JSON implementation

The JSON implementation is in [Json.fango](../../stdlib/Json.fango). It uses
UTF-8 [text adapters](text-io.md) over the existing `Reader` and `Writer`
interfaces for byte I/O, or explicitly supplied text readers and writers.
The pull handler owns a cursor over an immutable `Text.Reader.Window`. A
window contains a shared buffer description and a byte index, so token scans
and handler updates copy positions without copying the encoding, byte slice,
and snapshot base. The reader capability is captured once by the handler.
Whitespace, literals, string escapes, surrogate pairs, and number grammar run
in pure Fango over available text, without reader state dispatch per scalar.
Consumption is published at refill boundaries, explicit `at()` calls, and
scope exit. Cleanup reconciles the cursor on normal return and on exiting
consumer or source effects. Scanner failures publish their consumed prefix
before raising; idempotent window commits prevent cleanup from skipping it
again. The parent and text reader must not be advanced independently while
the pull handler owns the cursor.

Ordinary strings return one buffered span without fragment-list allocation or
concatenation; escaped strings collect fragments and join them once. Buffered
numbers use a compact grammar-phase loop and materialize one source slice,
preserving their original lexeme. Buffered typed integers instead accumulate a negative
magnitude directly while validating digits, leading zeros, and signed 64-bit
limits; the negative accumulator permits the minimum Int without overflow.
They produce no decimal string or second conversion pass. A token boundary
resumes the number phase over the next window without reparsing consumed digits.
Strings and literals that
cannot finish in the initial window fall back to the source-aware scanner;
the token scanner examines its initial window at most twice, then continues across
chunks. The source-aware scanner also preserves grammar-error positions and
consumption when the buffered path declines malformed input. Empty windows
are distinct from source EOF; only a refill returning `False` proves EOF.

JSON owns escape syntax and surrogate-pair assembly, while Encoding owns byte
decoding and Char owns scalar construction. Both scanning paths use opaque
text windows and encoded byte widths; JSON never inspects raw bytes or selects
a byte decoder. Cursor offsets and columns advance by those widths, preserving
byte-based diagnostics with Latin-1 as well as UTF-8. Decoding errors are
translated at the current cursor with its path; unrelated source effects
propagate.

The `Pull` effect retains one optional lookahead token for custom token users.
Typed String, Int, and Float decoding reads buffered scalars directly, and a
record key operation consumes its separator, string, and colon together.
These paths avoid intermediate tokens and repeated handler calls. Existing
lookahead is honored, and boundary or error cases use the common token scanner.
Array iteration consumes separators and closing brackets but leaves the next
value for its decoder, rather than tokenizing it in advance. Lexical errors
in a typed array element therefore occur inside that element's index path.

`Decode.scanValue` is a pure optional decoder over an opaque `Scan`, containing
one text window, line/column, and the remaining container-depth allowance.
Success returns one complete value and a derived position with the same
allowance. Failure or a boundary returns `Nothing` without publishing any
consumption, refilling, or raising effects. Generated records and lists attempt
this path once, then either publish the completed cursor through `acceptScan`
or execute their streaming decoder. Existing lookahead disables the attempt.
Nested values use pure scan methods rather than entering the Pull handler or
constructing path segments; only the enclosing success updates handler state.
The streaming path owns all failures and retains their paths and consumption.

Record derivation emits straight-line scanning for required fields in
declaration order. It matches the serialized key spelling through the text
reader's bulk `matchWindow`, then scans the value directly into the final
record. This avoids key decoding, field dispatch callbacks, and optional slots.
Other key orders, extra fields, alternative escape spellings, unsupported child
scanners, and incomplete records decline directly to the streaming decoder;
there is no second speculative record pass. Schemas with defaults or skipped
fields always decline, preserving invocation of effectful default expressions.
Union and generic Value instances also decline. Custom instances can decline
or compose bundled scan methods while preserving their decode semantics.

The streaming record decoder passes a typed tuple of
optional field slots through its sequential key loop. Field dispatch returns
an updated immutable tuple after successful decoding; unknown keys call
`skipValue` and retain the tuple. These privately owned slots need no local
handler activation. The decoder never constructs a generic value tree.
List decoding constructs up to eight elements directly in source order, so
short lists need one spine. Longer lists switch to a tail loop with an
accumulator and reversal, keeping stack use bounded for large documents.
Tuple projections and updates are generated in Fango, preserving duplicate,
required-field, and default checks. Path tracking uses a
cleanup region to restore the enclosing path after normal or exiting decoding.
The path is a stack of key and index segments, rendered as the slash-separated
string only when an error is built, so entering a field or element costs one
cons and no string construction.

The generic `Json.Value` parser uses the same token stream and is explicitly
opted into. Numbers in this tree retain their validated source lexeme.
Container depth is counted when a token is consumed, including through
lookahead, and capped before entering level 257. Pure scans inherit that budget
and decline if a nested opener would exceed it. The scanner retains one
lookahead token and the Reader's current buffer; typed parsing therefore has no
memory cost proportional to the entire input except for the result itself.

Encode derivation emits straight-line record and union output through the
`Emit` effect. The effect handler writes string chunks to a `Text.Writer`.
String escaping scans character runs rather than UTF-8 bytes; stringify
collects text directly without a byte validation round trip. The declarative
field options are ordinary `Json.FieldOption` values in each `Meta.Field`
attribute collection. Shared Fango helpers interpret and validate options for
both derivers; the compiler has no JSON-specific schema fields or rules. Both interpreter and Go compilation see
the same generated syntax tree. The observable format and errors are in the
[JSON reference](../reference/library-json.md).
