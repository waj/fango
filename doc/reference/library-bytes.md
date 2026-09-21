# Byte sequences

The immutable `Bytes` value: its operations, its indexing rules, and how it
converts to and from text.

[Reference index](../reference.md). Source: [Bytes](../../stdlib/Bytes.fango).

## Bytes

`Bytes` is an immutable sequence of bytes. It is what
[String](library-text.md#string) is not: a `String` is valid UTF-8 by
contract, so arbitrary input — a response body, an image, a framed record —
has no `String` to live in. `Bytes` indexes and slices by byte rather than by
Unicode scalar, and never rewrites what it holds.

The module exposes the type but not its constructor, so a value comes from
`empty`, `fromString`, `fromList`, or an operation below.

```fango
import Bytes exposing (Bytes)

empty : Bytes
length : Bytes -> Int
byteAt : Int -> Bytes -> Maybe Int
slice : Int -> Int -> Bytes -> Bytes
append : Bytes -> Bytes -> Bytes
concat : List Bytes -> Bytes
indexOf : Bytes -> Bytes -> Maybe Int
indexOfFrom : Int -> Bytes -> Bytes -> Maybe Int
startsWith : Bytes -> Bytes -> Bool
fromList : List Int -> Bytes
toList : Bytes -> List Int
fromString : String -> Bytes
toString : Bytes -> Maybe String
toStringLossy : Bytes -> String
```

`length` counts bytes. `byteAt index bytes` answers the byte at a 0-based
index as an `Int` in `0..255`, and `Nothing` when the index is out of range.

`slice start end bytes` uses clamped half-open byte indices: negative bounds
clamp to 0, bounds past the end clamp to the length, and an end not greater
than its start gives `empty`. A slice shares the storage it was cut from, so
it costs nothing however large the original is.

`append` joins two values and `concat` joins a list of them; `concat`
allocates once for the whole result rather than once per part. Neither ever
writes into what it was given — appending to a slice cannot disturb the value
it shares storage with.

`indexOf needle bytes` answers the first index at which `needle` occurs, or
`Nothing`. `indexOfFrom start needle bytes` is the same search beginning at a
clamped `start`, which is what keeps a repeated search over a growing window
linear in that window. The empty needle is found at the clamped start of every
value, including at index 0 of `empty`. Both scans run inside one native, so
searching a block never crosses the language boundary once per byte, and a
scan allocates nothing per match. `startsWith prefix bytes` tests an exact
prefix, and the empty prefix is a prefix of everything.

`fromList` packs a list of `Int`, truncating each element to its low eight
bits, so `fromList [256, -1]` is the two bytes `0` and `255`. It exists for
fixtures and binary framing, not for bulk data — a `List Int` costs eight
bytes per byte. `toList` unpacks the other way.

`fromString` takes a `String`'s UTF-8 bytes. `toString` answers `Nothing`
for a value that is not valid UTF-8, so nothing silently rewrites input that
is not text; `toStringLossy` substitutes U+FFFD for each malformed byte, as
[File.read](library-io.md) does, and is the reporting path rather than a
conversion.

`Bytes` has `Eq`, `Ord`, and `Show`. Ordering is lexicographic on unsigned
byte values, so a prefix orders before what extends it. `show` produces a
quoted escaped form at every position, nested or not: printable ASCII passes
through, `\\`, `\"`, `\n`, `\t`, and `\r` are spelled as escapes, and every
other byte is `\xNN` with uppercase hex. A byte is not a Unicode scalar, so
this is a display form rather than a literal the lexer reads back — there is
no `Bytes` literal syntax.

## Source and Sink

`Bytes` also declares the two leaves the buffered IO layer is built on:

```fango
type Source e = { pull : () ->{e} Maybe Bytes }
type Sink e = { write : Bytes ->{e} () }
```

They live here, with the value they carry, because everything that produces or
consumes bytes has to name them — the buffered `Reader` and `Writer`, the
`File` adapters beneath those, and later a socket. Their contracts belong with
their consumers, in [buffered readers and writers](library-readers.md).
