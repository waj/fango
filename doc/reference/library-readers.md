# Buffered readers and writers

Byte sources and sinks, the buffered `Reader` and `Writer` scopes over them,
and the read and write operations they carry.

For `String` and `Char` operations with UTF-8 or Latin-1 encoding, put
[Text.Reader and Text.Writer](library-text-io.md) over these byte interfaces.

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
file or socket. Native operations reject use of a closed handle.

`e` is what the leaf performs, so a memory leaf is pure and a socket leaf is
not. The row is an ordinary [row-kinded
argument](functions.md#row-kinded-parameters): `Source {IO, Fail IO.Error}` and
`Source {}` are both writable.

## Reader

A `Reader e` is a record whose operations perform exactly `e`. Parsing helpers
propagate that row without adding IO. The `withBytes`, `over`, `overBytes`, and
`limited` constructors use scoped local state. Separate readers advance independently.

```fango
import Reader exposing (Read(..), Reader)

type Reader e =
    { buffered : () ->{e} Bytes
    , refill : () ->{e} Bool
    , skip : Int ->{e} Int
    }

type Read = Found Bytes | Ended Bytes | Overflowed deriving (Eq, Show)

{-# scoped s #-}
withBytes : Bytes -> (Reader s ->{s} a) ->{e} a

{-# scoped s #-}
over : Source e -> (Reader s ->{s} a) ->{e} a
{-# scoped s #-}
overBytes : Bytes -> (Reader s ->{s} a) ->{e} a
{-# scoped s #-}
limited : Reader e -> Int -> (Reader s ->{s} a) ->{e} a
{-# scoped s #-}
limitedRemaining : Reader e -> Int -> (Reader s ->{s} a) ->{e} (a, Int, Bool)
ensure : Reader e -> Int ->{e} Bool
atEnd : Reader e ->{e} Bool
readUpTo : Reader e -> Int ->{e} Bytes
readExactly : Reader e -> Int ->{e} Maybe Bytes
readUntil : Reader e -> Bytes -> Int ->{e} Read
readLine : Reader e -> Int ->{e} Read
forEachChunk : Reader e -> (Bytes ->{e} ()) ->{e} ()
chunks : Reader e -> Stream () Bytes {e}
```

The three fields are the primitives and carry no policy. `buffered()` answers
what is in hand, using the effects in `e`. `refill()` pulls until the buffer grows,
answering `True`, or the source ends, answering `False`; it never answers
`True` without growing, so a loop on it makes progress. `skip n` consumes from
the buffer and answers how many bytes it took, which is `n` clamped to what was
there. A caller projects them off the reader — they are operation names, so
they are not module functions.

`withBytes contents action` creates a private advancing cursor over memory. Its
[scoped callback](functions.md#scoped-callbacks) may read it and return parsed
data. A consumer with no other effects gives a pure result:

```fango
prefix : Bytes -> Bytes
prefix contents = Reader.withBytes contents { reader -> Reader.readUpTo reader 4 }
```

The callback row `s` extends the remaining row `e` with a fresh local permission.
Database, network, IO, or other consumer effects remain in `e`; the runner only
discharges its own cursor permission. Readers, their operation callbacks, and
streams that advance them cannot escape this callback. Nested `withBytes` calls
can pass independent readers to one parser; see the executable
[two-reader example](../../testdata/run/reader_scoped_memory.fango).

`over source action` runs `action` with a reader over `source`. `overBytes contents
action` starts with the contents in its buffer over an exhausted source. These
constructors follow the same scoped callback rule as `withBytes`: the local
permission is discharged, while source and consumer effects remain visible.
A `Reader {}` has pure operations, such as an exhausted reader with
constant fields. Domain-specific readers can expose a domain effect without IO.
Parsing code written against `Reader e` works with all these implementations;
its effect row describes the reader operations, not just the underlying source.

`limited parent n action` stages a reader over a parent, clamping every answer to
a remaining allowance held in its own scoped cell. Every byte it hands out
is skipped through the parent, so when the scope ends the parent is positioned
after what was consumed rather than after the allowance; a caller that wants
the rest of a frame discarded skips it before leaving. Because a reader is a
value the parent stays reachable, so both the framed and the raw reader can be
held at once.
`limitedRemaining` also returns the unused allowance and whether the parent
reported end of input before that allowance was consumed.

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
[`Stream`](library-streams.md) with unit state whose step advances the reader.
Repeated traversals share the current reader position.

## Writer

A `Writer e` is a record whose operations perform exactly `e`. Its scoped buffer
combines several emits into one underlying write per flush window.

```fango
import Writer exposing (Writer)

type Writer e =
    { emit : Bytes ->{e} ()
    , flush : () ->{e} ()
    }

{-# scoped s #-}
over : Sink e -> Int -> (Writer s ->{s} a) ->{e} a
{-# scoped s #-}
collecting : (Writer s ->{s} a) ->{e} (a, Bytes)
write : Writer e -> Bytes ->{e} ()
writeString : Writer e -> String ->{e} ()
copy : Reader e -> Writer e ->{e} ()
```

`emit chunk` accepts bytes for eventual writing and `flush()` pushes everything
accepted so far; both are operation names, so neither is also a module
function, and a caller writes `writer.flush()`. `write` and `writeString` are
`emit` with a `Bytes` and with a `String`'s UTF-8 bytes. `copy reader writer`
writes everything left in the reader, chunk by chunk.

`over sink size action` writes whenever the buffer reaches `size`, and once more
when `action` returns normally. An emit is never split, so a single chunk larger
than the window is one oversized write, and a window below one byte writes
every emit straight through. The final write happens on normal completion only:
a body that fails emits nothing further, so no reader receives a truncated
message it would have to guess at.

`collecting action` answers the body's value together with everything written,
using scoped local storage. A consumer with no other effects gives a pure
result. Its bytes become available only on successful completion. Its `flush()` has nowhere to push to and does nothing.

## Lifetimes

Readers and writers created by these constructors must stay inside their scoped
callbacks. Parsed values, collected bytes, and other permission-independent
results may leave. Their streams and operation callbacks retain the same local
permission and cannot escape through containers or storage.

A source or sink backed by a file or socket still depends on that handle being
open; operations after its scope closes fail at runtime. Consume resource-backed
streams inside the resource's cleanup scope. Each task constructs its own scoped
buffered objects.
