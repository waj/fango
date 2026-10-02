# Encodings and text I/O

Strict text decoding and encoding over the [byte readers and
writers](library-readers.md). Text counts use Unicode scalars; transport framing
continues to use bytes.

[Reference index](../reference.md). Sources: [Encoding](../../stdlib/Encoding.fango),
[Text.Reader](../../stdlib/Text/Reader.fango), [Text.Writer](../../stdlib/Text/Writer.fango),
[Text.Builder](../../stdlib/Text/Builder.fango).

## Encoding

`Encoding.Encoding` is opaque. The bundled values are `Encoding.utf8` and
`Encoding.latin1`; adapters without an explicit encoding use UTF-8.

```fango
type Error = InvalidSequence Int | IncompleteSequence Int | Unrepresentable Char
    deriving (Eq, Show)

encode : Encoding -> String -> Result Error Bytes
decode : Encoding -> Bytes -> Result Error String
message : Error -> String
decodeAt : Encoding -> Bytes -> Int -> Result Error (Maybe (Char, Int))
matchAt : Encoding -> Bytes -> Int -> String -> Maybe Int

type AsciiSet -- opaque
asciiSet : (Char -> Bool) -> AsciiSet
spanUntil : Encoding -> AsciiSet -> Bytes -> Int -> Int
```

UTF-8 decoding rejects malformed sequences, including overlong forms,
surrogates, and values above U+10FFFF. A valid encoded U+FFFD is ordinary text.
`InvalidSequence offset` identifies the first malformed sequence;
`IncompleteSequence offset` identifies a valid but unfinished prefix at the
end of input. Offsets are zero-based bytes relative to the supplied input.

Latin-1 decoding maps each byte to the scalar with the same code, so every
byte sequence is valid. Encoding accepts U+0000–U+00FF and returns
`Unrepresentable char` for the first scalar outside that range. UTF-8 encoding
always succeeds because a `String` is valid text.

Neither encoding normalizes text, detects an encoding, removes a BOM, or
converts newlines. A UTF-8 BOM decodes to U+FEFF. Lossy whole-value UTF-8
decoding remains available explicitly as [`Bytes.toStringLossy`](library-bytes.md).

`decodeAt encoding bytes offset` inspects one scalar starting at a byte index,
returning that character and its encoded byte width without consuming input.
An index outside the bytes returns `Ok Nothing`; an index inside a UTF-8
continuation sequence is invalid. Incomplete input can be retried after more
bytes are appended. The text reader uses this operation to distinguish a
refill from a decoding failure.

`matchAt encoding bytes offset text` returns the encoded byte width of `text`
when its encoding occurs in `bytes` starting at `offset`, and `Nothing`
otherwise, including for text the encoding cannot represent or an offset
outside the bytes. Empty text matches at any offset from zero through the
length. Bytes after the match are not inspected.

`asciiSet member` records which ASCII characters satisfy `member`, calling it
once per ASCII code when the set is built; characters at or above U+0080 are
never members. `spanUntil encoding stops bytes offset` returns the end of the
longest run of complete, valid scalars starting at `offset` that contains no
member of `stops`. A malformed or incomplete sequence ends the run, as does
the end of the bytes; an offset outside the bytes is returned unchanged.

## Text.Reader

`Text.Reader.Reader e` is opaque. Its scopes introduce a fresh local permission
and propagate source and consumer effects, just like the byte reader scopes.

```fango
{-# scoped s #-}
over : Reader.Reader e -> (Text.Reader.Reader s ->{s} a) ->{e} a
{-# scoped s #-}
overWith : Encoding -> Reader.Reader e -> (Text.Reader.Reader s ->{s} a) ->{e} a
{-# scoped s #-}
withString : String -> (Text.Reader.Reader s ->{s} a) ->{e} a

type Read = Found String | Ended String | Overflowed deriving (Eq, Show)

peekChar : Text.Reader.Reader e ->{Fail Encoding.Error | e} Maybe Char
readChar : Text.Reader.Reader e ->{Fail Encoding.Error | e} Maybe Char
readUpTo : Text.Reader.Reader e -> Int ->{Fail Encoding.Error | e} String
readExactly : Text.Reader.Reader e -> Int ->{Fail Encoding.Error | e} Maybe String
readUntil : Text.Reader.Reader e -> String -> Int ->{Fail Encoding.Error | e} Read
readLine : Text.Reader.Reader e -> Int ->{Fail Encoding.Error | e} Read
atEnd : Text.Reader.Reader e ->{Fail Encoding.Error | e} Bool
position : Text.Reader.Reader e ->{e} Int
chunks : Text.Reader.Reader e -> Stream () String {Fail Encoding.Error | e}

tryPeekChar : Text.Reader.Reader e ->{e} Result Encoding.Error (Maybe Char)
tryReadChar : Text.Reader.Reader e ->{e} Result Encoding.Error (Maybe Char)
tryReadScalar : Text.Reader.Reader e ->{e} Result Encoding.Error (Maybe (Char, Int))
tryReadWhile : Text.Reader.Reader e -> (Char -> Bool) ->{e} Result Encoding.Error String
tryReadSpan : Text.Reader.Reader e -> (Char -> Bool) ->{e} Result Encoding.Error (String, Int)

type Window -- opaque
window : Text.Reader.Reader e ->{e} Window
windowPosition : Window -> Int
peekWindow : Window -> Result Encoding.Error (Maybe (Char, Int))
readWindow : Window -> Result Encoding.Error (Maybe (Char, Int, Window))
asciiAt : Window -> Int
skipAscii : Window -> Window
spanWindow : Window -> (Char -> Bool) -> Result Encoding.Error (String, Int, Window)
spanWindowUntil : Window -> Encoding.AsciiSet -> Result Encoding.Error (String, Int, Window)
windowText : Window -> Window -> Result Encoding.Error String
commitWindow : Text.Reader.Reader e -> Window ->{e} ()
refillWindow : Text.Reader.Reader e -> Window ->{e} (Window, Bool)
```

`over parent use` selects UTF-8; `overWith encoding parent use` selects an
encoding once for the scope. `withString` starts with valid text in memory.
Its reads use the common failure-effect signatures even though that input
cannot have a decoding error. Handle decoding failures around the read or
around the whole consumer. The `try` operations return decoding errors as
values; underlying source effects still propagate.

For example, this helper distinguishes encoding failures from any file/socket
failures carried by `e`:

```fango
readPair : Text.Reader.Reader e ->{e} Result Encoding.Error (Maybe String)
readPair text = Fail.attempt { Text.Reader.readExactly text 2 }
```

`peekChar` consumes nothing, but may refill. `readChar` consumes one complete
scalar; both answer `Nothing` at EOF. A character split across source chunks
is completed by refilling. An incomplete sequence at EOF fails rather than
becoming EOF or a replacement character.

`tryReadScalar` also returns the character's encoded byte width. `tryReadWhile`
consumes the available prefix satisfying a pure predicate; `tryReadSpan` returns
the same text together with the number of source bytes consumed. Byte widths
refer to the selected encoding, so they can differ from the returned String's
UTF-8 byte length. Prefix reads wait for the first complete scalar, then stop
at the current buffer boundary or the first rejected scalar. An empty success
can mean EOF or a rejected first scalar. A valid prefix before malformed or
incomplete input succeeds; a later call reports that error without consuming
the offending bytes. No later source chunks are pulled merely to extend an
already nonempty prefix.

`window` snapshots currently buffered bytes without consuming or refilling.
`peekWindow`, `readWindow`, and `spanWindow` are pure: they inspect only that
snapshot. `readWindow` returns the scalar, its encoded width, and a derived
window advanced past it. `spanWindow` returns a valid matching prefix, its byte
width, and the derived window; a later malformed or incomplete scalar ends that
prefix, and the next window read reports the error. `windowPosition` counts bytes
from the original snapshot start. Decoding error offsets are also relative to
that start. An empty window is not necessarily source EOF; incomplete sequences
are reported without pulling more input.
`spanWindowUntil snapshot stops` has the `spanWindow` contract for a predicate
that rejects exactly the members of `stops`; it finds and validates the run in
one pass without calling a predicate per character.

`asciiAt snapshot` returns the code of a one-byte ASCII character at the
window position, `-1` at the end of the window, and `-2` for any other byte,
whether it begins a valid scalar or not. Every bundled encoding maps bytes
below 128 to the same ASCII scalars, so a scanner for an ASCII-spelled grammar
can branch on these codes and use `readWindow` only for the `-2` case.
`skipAscii snapshot` advances one byte past the character `asciiAt` just
returned a code for; it repeats no check, so applying it anywhere else derives
a window whose next read can report a malformed sequence.

`matchWindow snapshot text : Maybe Window` compares an exact text prefix in the
snapshot's selected encoding and returns the position after a match. It is pure
and never refills or consumes. A mismatch, insufficient buffered bytes, or text
unrepresentable in that encoding returns `Nothing`; an empty text matches at
the original position. It validates only the matched prefix, so malformed
bytes after it do not prevent a match. Refusal does not diagnose malformed or
incomplete source text; use scalar reads to report those errors.

`windowText start end` decodes the bytes between two positions derived from the
same snapshot, with `start` preceding or equal to `end`. `commitWindow reader end`
publishes consumption through `end` on the originating reader. It consumes only
the prefix beyond the reader's last committed position: repeated commits of
the same or an earlier derived position consume nothing. Positions can be
committed progressively without obtaining a new snapshot. Abandoning an
uncommitted window consumes nothing. Do not advance the reader through ordinary
reads, another scanner, or its parent while using these derived positions.

`refillWindow reader end` commits through `end`, refills the parent, and returns
`(fresh, grew)`. The fresh window starts at position zero over the remaining
bytes followed by any new source bytes. `grew == False` proves source EOF;
remaining bytes may still contain complete scalars or an incomplete sequence.
An incomplete scalar is retained for a later chunk rather than consumed.
Subsequent scanning and commits must use the fresh snapshot. Old snapshots
remain valid for pure inspection and text extraction. Source failures propagate
with the committed prefix consumed. These operations keep byte decoding inside
the selected text encoding while allowing domain scanners to batch consumption.

`readUpTo reader n` reads at most `n` scalars. It waits for the first complete
character, then returns complete characters already available without waiting
to fill the count. A short answer is not EOF; an empty answer is EOF unless
`n <= 0`. `readExactly` waits for the requested count, answering `Nothing`
without consuming when there are too few. Nonpositive counts produce `""`
or `Just ""` without pulling input.

`readUntil reader delimiter limit` counts scalars in the run and delimiter
together. It consumes and excludes the delimiter, returning `Found run`.
At EOF before a delimiter it consumes the remainder and returns `Ended rest`.
Reaching the limit first returns `Overflowed` without consuming. A nonpositive
limit always overflows; an empty delimiter is found immediately with a
positive limit. Delimiters may span source chunks and contain non-ASCII text.

`readLine` strips LF and an immediately preceding CR. Other carriage returns
are content, including a final CR at EOF. Its limit includes the actual
terminator: CRLF counts as two scalars. Use `readUntil` for exact delimiters.

Each read stages its work before advancing the parent. Decoding failure,
insufficient exact input, and overflow leave that call's bytes unconsumed,
although refills can have grown the parent buffer. Decode only the requested
prefix: malformed text farther ahead does not prevent reading an earlier
valid prefix. `position` counts consumed bytes since adapter construction,
not scalars or the parent's position before construction.

`chunks` advances the reader and yields nonempty strings of complete
characters. Its boundaries may differ from the byte source's boundaries.
Traversals share the current position. Readers, their streams, and callbacks
cannot escape their scoped consumer.

While using a text adapter, advance the parent through that adapter only.
Leaving the scope preserves all unconsumed parent bytes, including lookahead.
Text limits bound scalars scanned and returned; the underlying byte reader
can prefetch larger source chunks. For byte-framed bodies, construct
`Reader.limited` first and put the text adapter inside its scope. Neither
adapter closes the underlying file or socket.

## Text.Writer

`Text.Writer.Writer e` is opaque and exposes string operations over a byte
writer, or a scoped string collector:

```fango
{-# scoped s #-}
over : Writer.Writer e -> (Text.Writer.Writer s ->{s} a) ->{e} a
{-# scoped s #-}
borrow : Writer.Writer e -> (Text.Writer.Writer s ->{s} a) ->{e} a
{-# scoped s #-}
overWith : Encoding -> Writer.Writer e -> (Text.Writer.Writer s ->{s} a)
    ->{Fail Encoding.Error | e} a
{-# scoped s #-}
collecting : (Text.Writer.Writer s ->{s} a) ->{e} (a, String)

write : Text.Writer.Writer e -> String ->{e} ()
writeChar : Text.Writer.Writer e -> Char ->{e} ()
flush : Text.Writer.Writer e ->{e} ()
tryWrite : Text.Writer.Writer e -> String ->{e} Result Encoding.Error ()
```

`over` selects UTF-8 and adds no encoding failure effect. `overWith` selects
the supplied encoding and makes `Fail Encoding.Error` available to its writer;
handle that effect around the adapter scope. `tryWrite` instead returns
encoding errors as values, allowing a consumer to translate or recover from
an individual failed write. Source/sink effects remain in `e`.

Each write encodes the whole supplied string before emitting bytes, so an
unrepresentable character emits nothing from that write. Earlier writes are
not rolled back. `writeChar` writes the corresponding single-character string.
`flush` forwards to the byte writer. Adapter scopes flush on normal completion,
including a returned `Err` value; an aborting callback does not perform the
final flush. Existing byte-writer buffering still determines when bytes reach
the sink.

`borrow` is the UTF-8 scope for nested encoders that leave the flush boundary
to their caller. It performs no final flush; explicit `flush` still forwards
to the parent. JSON's byte-writer entry points use this scope.

`collecting` accumulates written strings in a `Text.Builder` and returns its
text on successful completion and has a no-op
flush. It is pure when its consumer has no other effects. Writers and their
operation callbacks cannot escape their scopes.

## Text.Builder

`Text.Builder.Builder` is an opaque, immutable value holding text assembled by
appending.

```fango
empty : Builder
append : Builder -> String -> Builder
appendChar : Builder -> Char -> Builder
isEmpty : Builder -> Bool
toString : Builder -> String
```

Appending returns a new builder and leaves its argument unchanged: every
builder keeps answering exactly the text it was built from, however many
builders extend it, in whatever order, and from whichever tasks. `toString`
copies the text once. Appending to the most recently extended builder reuses
its storage, so a sequence of appends in order costs amortized time
proportional to the text appended; extending an older builder first copies
its text. Builders have no equality or display.

