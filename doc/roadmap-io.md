# Roadmap: buffered readers and writers, and sockets

This document owns the proposed buffered IO layer and its delivery sequence:
`Reader` and `Writer` as records bound to handler activations over abstract
byte sources and sinks, and the file, memory, and socket adapters beneath
them. Its motivating consumer is an HTTP server written in Fango, so
acceptance is stated in terms of what a server needs rather than library
breadth.

Implemented contracts remain in [design](design.md) and
[reference](reference.md); the main
[roadmap](roadmap.md#byte-io-buffered-readers-and-writers) summarizes
priorities. Everything below is **proposed**. Fango blocks are acceptance
specifications rather than fixtures that compile today.

The commitments are: buffering that lives in a handler's own state rather than
in any new mutable primitive; readers and writers that are ordinary values, so
two byte sources can be driven at once; operations with one-line contracts and
no hidden policy; hot loops that scan inside a native and cross the Fango
boundary once per line or per chunk; and lifetimes proven by the existing
capture checker.

`Bytes` was the whole plan's one new runtime primitive and it is now
implemented — [byte sequences](reference/library-bytes.md) owns its contracts
and [backend](design/backend.md#bytes-representation) its representation.
Everything left is ordinary Fango over the effect system, which is the point.

## What today's library cannot do

`File.Handle` is the right lifetime model at the wrong granularity. Reading is
`readLine`, whose contract is a `String` and its terminator; there is no
counted read, no delimiter other than a newline, and no lookahead. Writing
goes straight to the `*os.File`, so a response assembled from a status line,
several headers, and a body costs that many syscalls.

`Stream` composes well but pulls one element per operation and its cursors
exhaust once. A parser needs lookahead over a retained buffer. The right
relationship is that a reader *offers* a `Stream Bytes`, not that it is built
from one.

And a reader has to reach its own handler rather than the innermost one, so
that two readers can be driven at once.
[Binding a closure to an activation](reference/effects.md#binding-a-closure-to-a-handler-activation)
does that, including at the row-indexed record shape `Reader e` needs: a reader
is an ordinary record of closures bound to one activation, and two readers are
two records. The `reader_over_*` fixtures drive `over` at a same-module effect,
at `State`, and at `IO`, and `reader_two_readers` drives two at once. Both
effects here are unparameterized, so keying row labels by their arguments,
[deferred in the effects roadmap](roadmap-effects.md#deferred-topics), is not
on this path.

The remaining prerequisites are done. A row literal stands as a type argument
at a [row-kinded parameter](reference/functions.md#row-kinded-parameters), so
`Source {IO, Fail IO.Error}` is writable; `ignore : a -> ()` in `Basics`
discards an answer the caller does not want, which the readers below do
constantly; and the accumulator `Writer` module is gone, freeing the name for
the byte writer. An accumulator is a `State` handler with a combine function,
which anyone needing one writes in a few lines.

## Step 1 — Reader and Writer

### The leaves

A source and a sink are the leaves, and they are plain effect-indexed values so
that a memory leaf is pure and a socket leaf is not:

```fango
type Source e = Source (() ->{e} Maybe Bytes)
type Sink e = Sink (Bytes ->{e} ())
```

`pull` answers `Nothing` at end of input and otherwise whatever is available,
which may be short; empty is not end of input. A sink accepts a chunk and
writes all of it before returning. Neither carries a close: closing belongs to
the scope owning the file or socket, and a leaf outliving that scope is
already rejected because it captures the handle. These may equally be records:
a record's effect-indexed field is projected and widened like any other value.

### The effects and the records

Three operations for reading, two for writing, each with a one-line contract
and no hidden policy:

```fango
effect Reading
    buffered : () -> Bytes      -- what is in hand; performs no IO
    refill : () -> Bool         -- pull once; True only if the buffer grew
    skip : Int -> Int           -- consume from the buffer, answer how many

effect Writing
    emit : Bytes -> ()          -- accept bytes for eventual writing
    flush : () -> ()            -- push everything accepted so far
```

Both effects are private to their modules and named apart from the public
types, because a module may not declare a type and an effect of one name and
the labels never reach a signature. `refill` answers `False` at end of input,
and never `True` without growing the buffer, so a loop on it makes progress.
The buffer is the handler's own state
cell — the language already has exactly one mutable cell per parameterized
activation, clauses see an immutable snapshot, and the commit happens on
`resume`. No new mutable primitive is introduced, and none is wanted.

The values a consumer holds are ordinary records of closures bound to one
activation, indexed by the activation's residual row:

```fango
type Reader e =
    { buffered : () ->{e} Bytes
    , refill : () ->{e} Bool
    , skip : Int ->{e} Int
    }

type Writer e =
    { emit : Bytes ->{e} ()
    , flush : () ->{e} ()
    }
```

`e` is what a call actually performs: nothing for a memory reader, `IO` and
`Fail IO.Error` for a socket reader. It is the same index `Iterator a e`
carries, and for the same reason: the compiler selects Direct, Exit, or
Machine transport per arrow from its row, so the memory path compiles to
direct calls and the socket path carries exits. Calls are field projection,
`reader.refill()`, with nothing new in call syntax.

The earlier `peek`-based design is rejected: `peek n` has to decide how many
times it refills, and either choice breaks one of its two consumers. With
"at most one refill", `readExactly` must loop until peek stops growing, whose
termination condition is a length comparison rather than a fact from the
source; with "refill until satisfied", `readUpTo` blocks waiting for bytes the
caller said were optional. Splitting `buffered` from `refill` removes the
question.

### Scopes and stages

```fango
Reader.over    : Source e -> (Reader e ->{e} a) ->{e} a
Reader.limited : Reader e -> Int -> (Reader e ->{e} a) ->{e} a
Writer.over    : Sink e -> Int -> (Writer e ->{e} a) ->{e} a
Writer.collecting : (Writer e ->{e} a) ->{e} (a, Bytes)
```

`Reader.over` is the buffering handler, and it is where the binding rule is
used:

```fango
Reader.over source use =
    handle use { buffered = \_ -> buffered(), refill = \_ -> refill(), skip = \n -> skip n }
        with pending = Bytes.empty of
        buffered () -> resume pending with pending
        refill () -> ...
        skip n -> ...
```

Its clauses answer from the activation state and, when the buffer is short,
call the source, which is how the source's effects `e` reach both the result
row and the record's fields. The `Reading` and `Writing` labels appear on no signature in
this layer: it is discharged inside `over`, and consumers see only `e`.

`Reader.limited` is a handler over a parent reader: three delegating clauses,
each one line, clamping to a remaining allowance held in its own activation
state. Chunked transfer decoding, a decompressor, and a TLS layer later are the
same shape. Because the reader is a value, a stage's parent stays reachable —
you can hold both the framed and the raw reader.

### Derived operations

Everything above the primitives is ordinary Fango:

```fango
type Read = Found Bytes | Ended Bytes | Overflowed

Reader.ensure : Reader e -> Int ->{e} Bool
Reader.readUpTo : Reader e -> Int ->{e} Bytes
Reader.readExactly : Reader e -> Int ->{e} Maybe Bytes
Reader.readUntil : Reader e -> Bytes -> Int ->{e} Read
Reader.readLine : Reader e -> Int ->{e} Read
Reader.atEnd : Reader e ->{e} Bool
Reader.chunks : Reader e -> Stream Bytes e

Writer.write : Writer e -> Bytes ->{e} ()
Writer.writeString : Writer e -> String ->{e} ()
```

`ensure` loops on a real terminator rather than on a length comparison, and
everything else is built from it:

```fango
Reader.ensure reader n =
    if Bytes.length (reader.buffered()) >= n then True
    else if reader.refill() then Reader.ensure reader n
    else False

Reader.readUpTo reader n =
    ignore (Reader.ensure reader 1)
    window = reader.buffered()
    take = if Bytes.length window < n then Bytes.length window else n
    ignore (reader.skip take)
    Bytes.slice 0 take window

Reader.readExactly reader n =
    if Reader.ensure reader n then
        text = Bytes.slice 0 n (reader.buffered())
        ignore (reader.skip n)
        Just text
    else
        Nothing
```

`readUntil` grows its window with `ensure` and scans with `indexOfFrom`,
resuming the scan at `previousLength - delimiterLength + 1` so a delimiter
straddling a refill is still found; doubling the window keeps total scanning
linear. It takes a byte limit, because a server reading an unbounded header
line from an untrusted peer has a memory bug, and returns the three genuinely
distinct answers: the delimited run, the unterminated remainder at end of
input, and a limit reached before either. A failed `readExactly` consumes
nothing.

`Reader.chunks` bridges to [`Stream`](reference/library-streams.md);
`Stream`'s rule that a yielded value may not retain producer-local resources
holds trivially because `Bytes` is capture-free.

### Adapters

```fango
File.source : File.Handle -> Source {IO, Fail IO.Error}
File.sink : File.Handle -> Sink {IO, Fail IO.Error}
Memory.source : Bytes -> Source e
IO.stdout : Sink {IO}
IO.stdin : Source {IO}
```

`Memory.source` is pure, so a fixture-driven test performs no effects at all
and the same parsing code runs over it and over a socket. `File` gains counted
byte natives beside its existing line natives, which keep their contracts; the
unbuffered `File.write` stays as it is.

## Step 2 — sockets, HTTP, and a concurrent server

```fango
{-# resource #-}
type Listener
{-# resource #-}
type Connection

Net.withListener : Int -> (Listener ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
Net.accept : Listener -> (Connection ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
Net.withClient : String -> Int -> (Connection ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
Net.source : Connection -> Source {IO, Fail IO.Error}
Net.sink : Connection -> Sink {IO, Fail IO.Error}
```

Same lifetime shape as `File`, with the same private handle table. Three
compiler-side questions come with it. The bundled native boundary accepts only
scalars, so a native answering `Bytes` — a counted file read, a socket read —
is the first that needs `[]byte` admitted to it. Fallible natives that the
boundary turns into `Result IO.Error a` are spelled for the bundled `File`
module alone, and the
[general case is deferred](roadmap-effects.md#deferred-topics) pending
resolved error identities; admitting a second bundled module is the smallest
form of that. And socket failures — connection refused, reset by peer, address
in use — have no member in `IO.Error`'s `Kind`.

HTTP is then ordinary Fango over a reader and a writer:

```fango
serve : Net.Connection ->{IO, Fail IO.Error} ()
serve connection =
    Reader.over (Net.source connection) \input ->
        Writer.over (Net.sink connection) 8192 \output ->
            request = Http.readRequest input
            Http.writeResponse output (respond request)
```

and the same code over fixtures:

```fango
Reader.over (Memory.source fixture) \input ->
    Writer.collecting \output ->
        Http.writeResponse output (respond (Http.readRequest input))
```

A proxy holds two readers at once, which is what a reader being a value
bought:

```fango
Reader.over (Net.source client) \downstream ->
    Reader.over (Net.source upstream) \up ->
        relay downstream up
```

`downstream` was bound in the outer activation and is driven from the inner
scope; each `refill` reaches its own socket.

A connection per task depends on
[cooperative structured async](roadmap-effects.md#4-cooperative-structured-async).
A server handling one connection at a time needs none of it and is this
document's acceptance program.

## Compiler and runtime boundary

| Compiler or runtime | Ordinary Fango library |
| --- | --- |
| Handler activations, their state cells, and closure binding | `Reader` and `Writer` and every stage above them |
| Scope ownership and resource escape proofs | `Reader.over`, `Writer.over`, `Net` scopes |
| Counted file and socket reads and writes | `Source` and `Sink` adapters |
| — | Framing, limits, chunked decoding, HTTP |

`Reader`, `Writer`, and the adapters are library names and need no grammar
change, as `Bytes` needed none. Everything they are built from — activations,
rows, scopes — already exists.

## Delivery and acceptance

Each step is usable without the ones after it.

**1. `Reader`/`Writer`.** The same parsing code passes over a memory source
and a file source, and the memory path performs no effects. Two readers are
driven alternately and neither disturbs the other. A writer emits one
underlying write per flush window. Reading a file through `Reader.chunks`
matches `File.read` byte for byte. Short pulls, a delimiter straddling a
refill, limit overflow, end of input mid-delimiter, a failed `readExactly`
consuming nothing, and `limited` leaving the parent correctly positioned are
all covered. A reader cannot outlive its source's scope, and a reader cannot
outlive its own `over`.

**2. Sockets and HTTP.** A client fetches over a loopback connection; a
listener serves one connection at a time; a peer closing mid-read and
mid-write is an ordinary typed failure; no handle outlives its scope. A
scripted request set covers a malformed request, an oversized header block, a
body shorter than its declared length, and a keep-alive sequence on one
connection — each exercised against a memory source in a fixture as well as
over a socket. A loopback proxy drives two readers at once.

Alongside these, move a line-oriented example to the buffered path and add the
[unimplemented grep-lite comparison against Go](roadmap-examples.md), with
identical output and a read-throughput measurement recorded on an idle host.

## Open decisions

- Whether socket failures extend `IO.Error`'s `Kind` or introduce `Net.Error`,
  and whether a limit overflow should ever be raised rather than returned.
- Whether `Writer.over`'s bracketed release should flush after a failed body.
  Flushing risks emitting a truncated response; not flushing loses bytes the
  caller believed were written. `Writer.collecting` sidesteps it for callers
  needing all-or-nothing, which may be the answer.
- Whether `Bytes.fromString` and `Bytes.toString` may share storage rather than
  copy. Both directions are safe for immutable values, but the Go spelling
  needs `unsafe`, which no part of the runtime uses today.
- Whether `Read`'s three answers are the right shape, or whether the limit
  belongs on the reader rather than on each call.

## Verification

Retain the [repository gates](../AGENTS.md) and
[verification contracts](design/verification.md): the Core linter, the
interpreter/compiler differential suite, functional tests, `go vet`, and
updated goldens for intentional language changes. Both backends must produce
identical bytes for every reader and writer fixture. Capture, escape, and
cursor fixtures must be rerun rather than assumed, because a bound record is
the first library use of an activation as a value.
