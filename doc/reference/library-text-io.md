# Encodings and text I/O

Strict text decoding and encoding over the [byte readers and
writers](library-readers.md). Text counts use Unicode scalars; transport framing
continues to use bytes.

[Reference index](../reference.md). Sources: [Encoding](../../stdlib/Encoding.fango),
[Text.Reader](../../stdlib/Text/Reader.fango), [Text.Writer](../../stdlib/Text/Writer.fango).

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

`collecting` joins written strings on successful completion and has a no-op
flush. It is pure when its consumer has no other effects. Writers and their
operation callbacks cannot escape their scopes.
