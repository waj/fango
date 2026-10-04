# JSON implementation

The JSON implementation is three modules, each importing the ones before it:

- [Json.Field](../../stdlib/Json/Field.fango) interprets `FieldOption`
  attributes for both derivers and owns the string escaping rule, so the
  decoder matches a key in exactly the bytes the encoder writes.
- [Json.Pull](../../stdlib/Json/Pull.fango) holds everything the pull handler
  touches: `Error`, `Token`, the `Pull` effect, `Scan`, the `Decode` class,
  its primitive instances and deriver, and `Value`, since `readValue` builds it.
  Keeping the `Decode` deriver here leaves the scan helpers its generated code
  calls private, because quoted code resolves in the module that wrote it.
- [Json](../../stdlib/Json.fango) holds whole-document entry points and the
  `Encode`/`Emit` side, and re-exports `Error`, `Decode`, `Value`, `Number`,
  and `FieldOption` through the [module re-export rule](../reference/modules.md#exports-and-visibility).

`parse` runs the pull handler, which returns `Result Error a`, and `Decode`'s
only method is the scan protocol, so those types cannot sit above `Json.Pull`;
re-exporting them is what keeps the everyday spelling `Json.Error`.

The parser uses
UTF-8 [text adapters](text-io.md) over the existing `Reader` and `Writer`
interfaces for byte I/O, or explicitly supplied text readers and writers.
The pull handler owns a cursor over an immutable `Text.Reader.Window`. A
window contains a shared buffer description and a byte index, so token scans
and handler updates copy positions without copying the encoding, byte slice,
and snapshot base. The reader capability is captured once by the handler.
Whitespace, literals, string escapes, surrogate pairs, and number grammar run
in pure Fango over available text, without reader state dispatch per scalar.
Buffered whitespace, punctuation, element and field separators, and typed
integer digits branch on `Text.Reader.asciiAt` codes and fall back to scalar reads only for a
non-ASCII byte, so the common path builds no decoded-character result.
Consumption is published at refill boundaries, explicit `at()` calls, and
scope exit. Cleanup reconciles the cursor on normal return and on exiting
consumer or source effects. Scanner failures publish their consumed prefix
before raising; idempotent window commits prevent cleanup from skipping it
again. The parent and text reader must not be advanced independently while
the pull handler owns the cursor.

Ordinary strings return one buffered span without fragment-list allocation or
concatenation. The span stops at a quote, backslash, or control character
through a precomputed `Encoding.AsciiSet`, so locating and validating it is a
single pass with no per-character predicate call. From a string's first
escape, its spans and decoded escapes accumulate in a `Text.Builder`, which a
stopped scan hands to the source-aware driver; the builder copies once into
the result. Buffered
numbers use a compact grammar-phase loop and materialize one source slice,
preserving their original lexeme. Buffered typed integers instead accumulate a negative
magnitude directly while validating digits, leading zeros, and signed 64-bit
limits; the negative accumulator permits the minimum Int without overflow.
They produce no decimal string or second conversion pass. A token boundary
resumes the number phase over the next window without reparsing consumed digits.
Strings retain their spans and fragments, literals retain their remaining
suffix, and typed integers retain their sign, grammar phase, and negative
accumulator across refills. The source-aware drivers run the same pure window
loops after each refill. There is one lexer dispatch; it does not restart a
token because its initial window was incomplete. Escape handling can retry the
current escape to preserve its exact diagnostic site, without rescanning the
completed string prefix. Empty windows
are distinct from source EOF; only a refill returning `False` proves EOF.

JSON owns escape syntax and surrogate-pair assembly, while Encoding owns byte
decoding and Char owns scalar construction. Window loops and source drivers use opaque
text windows and encoded byte widths; JSON never inspects raw bytes or selects
a byte decoder. Cursor offsets and columns advance by those widths, preserving
byte-based diagnostics with Latin-1 as well as UTF-8. Decoding errors are
translated at the current cursor with its path; unrelated source effects
propagate.

The `Pull` effect retains one optional lookahead token for custom token users.
Typed String, Int, and Float decoding reads buffered scalars directly, and a
record key operation consumes its separator, string, and colon together.
These paths avoid intermediate tokens and repeated handler calls. Existing
lookahead is honored through the common token consumer.
Array iteration consumes separators and closing brackets but leaves the next
value for its decoder, rather than tokenizing it in advance. Lexical errors
in a typed array element therefore occur inside that element's index path.

`Decode` has one parser method, `scanValue`, pure over an opaque `Scan`
containing one text window, line/column, and the remaining container-depth
allowance. The cursor stays the
same size as the original optional scanner: a negative allowance marks pending
lookahead, which must be handled through Pull before buffered scanning can
continue. It does not carry offset, source EOF, or a path stack; the handler
owns those fields. `acceptScan` advances offset by the window-index difference
and updates depth from the allowance, while a pending view leaves state intact.

A scan returns either `Ok (value, rest)` or `Err` containing a suspended action.
The action returns `ScanDone value rest` or `ScanContinue` with the next scan
result. `runScan` is a tail loop over these steps. Suspension publishes exactly
the cursor needed by the source-aware operation, then resumes pure scanning
from the handler's new window. Completed record fields and list elements stay
in continuation arguments; a boundary does not restart the enclosing value.
The common `decodeValue` driver always starts this parser at `currentScan`,
runs its steps, and publishes the completed position. Pending lookahead uses
the same parser entry and continuation flow. There is no separate streaming
record/list parser or container restart fallback.
Mismatch diagnostics point at the start of the offending token. The handler
keeps that start alongside its cursor while the token is current, and forgets
it when the cursor moves on by a container step or a published scan, so the
reported position never depends on whether a value took the buffered or the
token path. Pure scans hand a start to the handler only before a source-aware
tail or on failure; successful buffered scans carry no extra position.
Nested scans build no diagnostic path on success. Only executing a child's
suspended action enters its key/index path, and cleanup restores it on normal
or exiting completion. Source effects propagate through the same boundary.

The typed String scanner retains its current span, completed fragments, and
stopped window when a string cannot finish in that window. `stringTail` resumes
the source-aware string phase from that position, including a split escape or
encoded character. It does not rescan the initial span. Numeric scans pass the
completed window run to their driver; the driver preserves grammar diagnostics,
refills, and runs the next window from that phase. Typed integers avoid decimal
string construction on success. A decimal, exponent, or overflowing magnitude
continues number validation before returning the integer type mismatch.

Record derivation emits straight-line scanning for required fields in
declaration order. It matches serialized keys through the text reader's bulk
`matchWindow`, then scans values directly into the final record. On the first
unexpected order it continues in a named local key loop, passing decoded fields
as `Just` arguments and the remaining fields as `Nothing`. This loop keeps bulk
encoded matches for known keys and tail-calls itself with the matching field
argument replaced. A child suspension returns to that loop with the new value
and saved fields. Split, escaped, or unknown keys use the source-aware key
operation, dispatch or validate/skip the value, then return the next pure scan
step. Defaults and skipped fields use the same loop; final construction defers
effectful defaults until needed. Duplicate and missing fields retain their
source-aware diagnostics. Union and generic Value scanners defer to their
token decoders, and custom instances can use `scanFromPull` or `mapScan`.

List decoding constructs up to eight elements directly in source order, so
short lists need one spine. Longer lists switch to a tail loop with an
accumulator and reversal, keeping stack use bounded for large documents.
Field dispatch and record construction are generated in Fango, preserving
duplicate, required-field, and default checks. Path tracking uses a
cleanup region to restore the enclosing path after normal or exiting decoding.
The path is a stack of key and index segments, rendered as the slash-separated
string only when an error is built, so entering a field or element costs one
cons and no string construction.

The generic `Json.Value` parser uses the same token stream and is explicitly
opted into. Numbers in this tree retain their validated source lexeme.
Container depth is counted when a token is consumed, including through
lookahead, and capped before entering level 257. Pure scans inherit that budget
and suspend for the source-aware diagnostic if a nested opener would exceed it.
The handler retains one lookahead token and the Reader's current buffer. A
suspended parent can retain its entry window until its child completes;
continuation steps release prior windows. Scanner memory is bounded by nesting,
the short-list budget, and the current token's fragments, in addition to the
decoded result.

Encode derivation emits straight-line record and union output through the
`Emit` effect. The effect handler writes string chunks to a `Text.Writer`.
String escaping scans character runs rather than UTF-8 bytes; stringify
collects text directly without a byte validation round trip. The declarative
field options are ordinary `Json.FieldOption` values in each `Meta.Field`
attribute collection. `Json.Field` interprets and validates options for
both derivers; the compiler has no JSON-specific schema fields or rules. Both interpreter and Go compilation see
the same generated syntax tree. The observable format and errors are in the
[Json module documentation](../../stdlib/Json.fango).
