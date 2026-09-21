# Buffered readers and writers

Byte sources and sinks, the buffered `Reader` and `Writer` scopes over them,
and the read and write operations they carry.

[Reference index](../reference.md). Sources: [Reader](../../stdlib/Reader.fango), [Writer](../../stdlib/Writer.fango), [Bytes](../../stdlib/Bytes.fango).

## Source and Sink

A source and a sink are the leaves the buffered layer is built on, and they
live in [Bytes](library-bytes.md) with the value they carry, because everything
that produces or consumes bytes has to name them:

```fango
import Bytes exposing (Sink(..), Source(..))

type Source e = { pull : () ->{e} Maybe Bytes }
type Sink e = { write : Bytes ->{e} () }
```

`pull()` answers `Nothing` at end of input and otherwise whatever is available,
which may be short; an empty chunk is not end of input, and a reader pulls
through it. `write chunk` accepts a chunk and writes all of it before
returning. Neither carries a close: closing belongs to the scope owning the
file or socket, and a leaf outliving that scope is already rejected because it
captures the handle.

`e` is what the leaf performs, so a memory leaf is pure and a socket leaf is
not. The row is an ordinary [row-kinded
argument](functions.md#row-kinded-parameters): `Source {IO, Fail IO.Error}` and
`Source {}` are both writable.

## Reader

A `Reader e` is a record of closures bound to one `over` activation, so two
readers are two records and each `refill` reaches its own source rather than
whichever handler is innermost when it is called. The buffer is the
activation's own state cell, which is why nothing here needs a mutable
primitive.

```fango
import Reader exposing (Read(..), Reader)

type Reader e =
    { buffered : () ->{e} Bytes
    , refill : () ->{e} Bool
    , skip : Int ->{e} Int
    }

type Read = Found Bytes | Ended Bytes | Overflowed deriving (Eq, Show)

over : Source e -> (Reader e ->{e} a) ->{e} a
overBytes : Bytes -> (Reader e ->{e} a) ->{e} a
limited : Reader e -> Int -> (Reader e ->{e} a) ->{e} a
ensure : Reader e -> Int ->{e} Bool
atEnd : Reader e ->{e} Bool
readUpTo : Reader e -> Int ->{e} Bytes
readExactly : Reader e -> Int ->{e} Maybe Bytes
readUntil : Reader e -> Bytes -> Int ->{e} Read
readLine : Reader e -> Int ->{e} Read
forEachChunk : Reader e -> (Bytes ->{e} ()) ->{e} ()
chunks : Reader e -> Stream Bytes e
```

The three fields are the primitives and carry no policy. `buffered()` answers
what is in hand and performs no IO. `refill()` pulls until the buffer grows,
answering `True`, or the source ends, answering `False`; it never answers
`True` without growing, so a loop on it makes progress. `skip n` consumes from
the buffer and answers how many bytes it took, which is `n` clamped to what was
there. A caller projects them off the reader — they are operation names, so
they are not module functions.

`over source use` runs `use` with a reader over `source`. `overBytes contents
use` reads a value already in memory: the activation's cell is the buffer, so
the whole value is the starting buffer over a source that has already ended.
Nothing in that path performs anything, so a memory reader stands at any row,
including the pure `Reader {}`, and parsing code written against `Reader e`
runs unchanged over memory and over a file.

`limited parent n use` stages a reader over a parent, clamping every answer to
a remaining allowance held in its own activation state. Every byte it hands out
is skipped through the parent, so when the scope ends the parent is positioned
after what was consumed rather than after the allowance; a caller that wants
the rest of a frame discarded skips it before leaving. Because a reader is a
value the parent stays reachable, so both the framed and the raw reader can be
held at once.

`ensure reader n` grows the buffer until it holds `n` bytes, answering whether
it does; it consumes nothing. `atEnd` is `ensure` for one byte, inverted.
`readUpTo reader n` answers what is in hand once there is anything at all, so
it never waits for bytes the caller said were optional — a short answer is not
end of input and an empty one is. `readExactly reader n` answers `Nothing`
unless `n` bytes are available, and consumes nothing when it does, so the
caller still holds what it could not frame.

`readUntil reader delimiter limit` consumes the delimiter and does not answer
it. `limit` bounds the run together with its delimiter, which is the number of
bytes the call will ever buffer: a server reading an unbounded header line from
an untrusted peer has a memory bug, so there is no unlimited spelling. Its
three answers are the genuinely distinct ones — `Found run` for a delimited
run, `Ended rest` for the unterminated remainder at end of input, both
consumed, and `Overflowed` for a limit reached before either, which consumes
nothing. A delimiter straddling a refill is found. The empty delimiter is found
at the start of everything, as [`Bytes.indexOf`](library-bytes.md) is.
`readLine reader limit` is `readUntil` at `"\n"`, and a CRLF terminator leaves
the carriage return on the run, because whether that byte is framing or content
is the caller's question.

`forEachChunk reader action` hands each buffered chunk to `action` until the
source ends, so a consumer sees the source's own chunking rather than a size
this module invented. `chunks` is the same thing as a
[`Stream`](library-streams.md); `Stream`'s rule that a yielded value may not
retain producer-local resources holds trivially, because `Bytes` captures
nothing.

## Writer

A `Writer e` is a record of closures bound to one `over` or `collecting`
activation, so a response assembled from a status line, several headers, and a
body costs one underlying write per flush window rather than one per part.

```fango
import Writer exposing (Writer)

type Writer e =
    { emit : Bytes ->{e} ()
    , flush : () ->{e} ()
    }

over : Sink e -> Int -> (Writer e ->{e} a) ->{e} a
collecting : (Writer e ->{e} a) ->{e} (a, Bytes)
write : Writer e -> Bytes ->{e} ()
writeString : Writer e -> String ->{e} ()
```

`emit chunk` accepts bytes for eventual writing and `flush()` pushes everything
accepted so far; both are operation names, so neither is also a module
function, and a caller writes `writer.flush()`. `write` and `writeString` are
`emit` with a `Bytes` and with a `String`'s UTF-8 bytes.

`over sink size use` writes whenever the buffer reaches `size`, and once more
when `use` returns normally. An emit is never split, so a single chunk larger
than the window is one oversized write, and a window below one byte writes
every emit straight through. The final write happens on normal completion only:
a body that fails emits nothing further, so no reader receives a truncated
message it would have to guess at.

`collecting use` answers the body's value together with everything written,
and performs nothing of its own, so the same writing code runs in a test with
no effects at all and a caller that wants all-or-nothing gets it for the whole
body. Its `flush()` has nowhere to push to and does nothing.

## Lifetimes

A reader and a writer are [bound to their
activation](effects.md#binding-a-closure-to-a-handler-activation), so the
handler lifetime rules apply unchanged: reporting one as a result, storing it
in an ADT, capturing it in a returned closure, or storing it in an outer
handler is rejected with `STATE RESULT ESCAPES`. A `Stream` from `chunks`
retains the reader and obeys the same rule — it may be consumed inside the
scope and not carried out of it. A `Source` built over an open file captures
the handle and cannot outlive the scope that owns it, which reports
`RESOURCE ESCAPES`. Passing a reader inward, including into an inner activation
of the same effect, is permitted.
