# Roadmap: bytes, buffered readers and writers, and sockets

This document owns the proposed byte-oriented IO layer and its delivery
sequence: three compiler defects on its path, an immutable `Bytes` value,
`Reader` and `Writer` as handler activations over abstract byte sources, and
the file, memory, and socket adapters beneath them. Its motivating consumer is
an HTTP server written in Fango, so acceptance is stated in terms of what a
server needs rather than library breadth.

Implemented contracts remain in [design](design.md) and
[reference](reference.md); the main
[roadmap](roadmap.md#byte-io-buffered-readers-and-writers) summarizes
priorities. Everything below is **proposed**. Fango blocks are acceptance
specifications rather than fixtures that compile today.

The commitments are: one immutable byte value with cheap slicing and native
scanning; buffering that lives in a handler's own state rather than in any new
mutable primitive; readers and writers that are addressable values, so two
byte sources can be driven at once; operations with one-line contracts and no
hidden policy; hot loops that scan inside a native and cross the Fango
boundary once per line or per chunk; and lifetimes proven by the existing
capture checker.

**`Bytes` is the only new runtime primitive in the whole plan.** Everything
else is ordinary Fango over the effect system, which is the point.

## What today's library cannot do

`String` is the only sequence of characters and it is valid UTF-8 by contract:
the native boundary validates it, `File.read` replaces malformed input with
U+FFFD, and `String`'s operations index by Unicode scalar. An HTTP body is
arbitrary bytes, so no amount of `String` work reaches it, and a layer that
silently rewrites a PNG is worse than no layer.

`File.Handle` is the right lifetime model at the wrong granularity. Reading is
`readLine`, whose contract is a `String` and its terminator; there is no
counted read, no delimiter other than a newline, and no lookahead. Writing
goes straight to the `*os.File`, so a response assembled from a status line,
several headers, and a body costs that many syscalls.

`Stream` composes well but pulls one element per operation and its cursors
exhaust once. A parser needs lookahead over a retained buffer. The right
relationship is that a reader *offers* a `Stream Bytes`, not that it is built
from one.

And an activation cannot be named, so two readers cannot be driven at once —
see [handler instances](roadmap-instances.md), which this layer depends on.

## Step 1 — three compiler defects

All three are independent of this layer and all three were found on its path.
The third blocks step 4.

### Core lint rejects a widened effect-indexed record field

```fango
effect Cell
    get : () -> Int

type Ops e = { read : () ->{Cell | e} Int }

withCell : (Ops e ->{Cell | e} a) ->{e} a
withCell use =
    handle use ({ read = \_ -> get() }) of
        get () -> resume 7

main() = print (withCell (\o -> o.read()))
```

```
fango: internal compiler error: Core invariants violated in module <entry>:
  def main: reference `_field1` changes its binding type from () ->{Cell} Int to () ->{IO, Cell} Int
```

Inference accepts the covariant widening that
[row inclusion](reference/effects.md#row-inclusion-and-callback-compatibility)
promises for effect-indexed values; Core records the projected field at the
rigid instantiation, and lint compares the two. The defect is a crash on
plausible source. Establish which side is right — whether Core should carry
the widened binding or the projection should be adapted — before changing
either. `Source e` below is an effect-indexed value, so this decides whether
it can be a record or must stay a single-constructor wrapper.

### `EFFECT MISMATCH` can print two identical types

```fango
leak : Iterator Int e -> Maybe Int
leak cursor = Stream.withCursor (Stream.fromList [9, 9, 9]) (\_ -> next cursor)
```

```
The annotation says:

    Iterator Int e -> Maybe Int

but the body requires:

    Iterator Int e -> Maybe Int
```

The rejection is correct — the body's row is the cursor's latent `e`, which
the pure annotation does not list — but the rendering elides exactly the
difference it reports. The note beneath carries the real information. This
belongs with the diagnostic work in
[product polish](roadmap.md#product-polish); it is recorded here because row
diagnostics are what a user of this layer meets most often.

### Codegen segfaults on a row-polymorphic handler at a cross-module effect

```fango
import State exposing (State(..))
type Source e = { pull : () ->{e} String }
effect Reading
    peek : () -> String
over : Source e -> (() ->{Reading | e} a) ->{e} a
over source use =
    handle use() of
        peek () -> resume (source.pull())
stated = Source { pull = \_ -> get() }
main() =
    r = State.run "S" (\_ -> over stated (\_ -> peek()))
    print r.value
```

`fango check` passes. `fango run` panics with a nil dereference in
`internal/codegen.emitUnitWithMachine` (`internal/codegen/gen.go:330`).

The trigger is a row-polymorphic handler wrapper whose clause performs the
abstract row, instantiated at a concrete effect declared in **another module**.
The same shape at `IO` runs. The same shape at a user effect declared in the
same file runs. The stdlib `State` crashes it.

This is a hard blocker: `Reader.over : Source e -> ... ->{e} a` used with a
source that is not `IO` is exactly the pure-fixture path this design exists
for.

## Step 2 — handler instances

[Handler instances](roadmap-instances.md) owns the design. This layer needs
one thing from it: a handler can hand out a value standing for its activation,
so that operations addressed to that value reach it rather than the innermost
handler. Two readers become two values.

Nothing else is required. Both effects here are unparameterized, so the
row-label rule change discussed in that document is not on this path.

## Step 3 — Bytes

`Bytes` is an immutable sequence of bytes with a compiler-known runtime
representation, recognized at its declaration by canonical symbol exactly as
the bundled `List` is (see [backend](design/backend.md#list-representation)
and `internal/infer/list.go`). It is a Go `[]byte` that nothing writes to after
construction, so slicing shares backing and costs nothing.

```fango
Bytes.empty : Bytes
Bytes.length : Bytes -> Int
Bytes.byteAt : Int -> Bytes -> Maybe Int
Bytes.slice : Int -> Int -> Bytes -> Bytes
Bytes.append : Bytes -> Bytes -> Bytes
Bytes.concat : List Bytes -> Bytes
Bytes.indexOf : Bytes -> Bytes -> Maybe Int
Bytes.indexOfFrom : Int -> Bytes -> Bytes -> Maybe Int
Bytes.startsWith : Bytes -> Bytes -> Bool
Bytes.fromString : String -> Bytes
Bytes.toString : Bytes -> Maybe String
Bytes.toStringLossy : Bytes -> String
```

`slice` clamps half-open byte indices and shares storage. `indexOf` is where an
HTTP parser spends its time and is a native over Go's `bytes.Index`, so
scanning a header block never crosses the boundary per byte; `indexOfFrom` is
what keeps a growing-window delimiter search linear. `toString` validates
UTF-8 and answers `Nothing` for a body that is not text; `toStringLossy`
substitutes U+FFFD and is the reporting path. `Bytes` derives `Eq`, `Ord`, and
`Show`, with `Show` producing a quoted escaped form.

Core operations are inline templates over `fangort` helpers, the form `Basics`
already uses (`intShow = native "fangort.ShowInt($1)"`), so they need no
extension to the sidecar ABI. Natives that *produce* `Bytes` from outside —
counted file reads, socket reads — do need the bundled boundary to accept
`[]byte`, which is part of step 5.

Three alternatives were rejected. `List Int` costs eight bytes per byte and
forfeits `bytes.Index`. An opaque handle into a native table, the mechanism
`File.Handle` uses, is never collected and would leak one entry per chunk.
Relaxing `String` to admit invalid UTF-8 would invalidate every existing
`String` contract and the boundary validation protecting the Go side.

**Runtime invariant.** A `Bytes` never aliases a buffer that will be written
again; a native reading into a scratch slice copies on the way out. This is
what makes `slice` free everywhere else, and it is the one rule a reviewer of
the native code must check.

## Step 4 — Reader and Writer

### The source

A source is the leaf, and it is a plain effect-indexed value so that a memory
source is pure and a socket source is not:

```fango
type Source e = Source (() ->{e} Maybe Bytes)
```

`pull` answers `Nothing` at end of input and otherwise whatever is available,
which may be short; empty is not end of input. It carries no close: closing
belongs to the scope owning the file or socket, and a source outliving that
scope is already rejected because it captures the handle. Whether this can be
a record rather than a single-constructor wrapper depends on the first defect.

### The effects

Three operations for reading, two for writing, each with a one-line contract
and no hidden policy:

```fango
effect Reader
    buffered : () -> Bytes      -- what is in hand; performs no IO
    refill : () -> Bool         -- pull once; False means end of input
    skip : Int -> Int           -- consume from the buffer, answer how many

effect Writer
    emit : Bytes -> ()          -- accept bytes for eventual writing
    flush : () -> ()            -- push everything accepted so far
```

The buffer is the handler's own state cell — the language already has exactly
one mutable cell per parameterized activation, clauses see an immutable
snapshot, and the commit happens on `resume`. No new mutable primitive is
introduced, and none is wanted.

The earlier `peek`-based design is rejected: `peek n` has to decide how many
times it refills, and either choice breaks one of its two consumers. With
"at most one refill", `readExactly` must loop until peek stops growing, whose
termination condition is a length comparison rather than a fact from the
source; with "refill until satisfied", `readUpTo` blocks waiting for bytes the
caller said were optional. Splitting `buffered` from `refill` removes the
question.

### Scopes and stages

```fango
Reader.over    : Source e -> (Reader ->{Reader | e} a) ->{e} a
Reader.limited : Reader -> Int -> (Reader ->{Reader | e} a) ->{Reader | e} a
Writer.over    : Sink e -> Int -> (Writer ->{Writer | e} a) ->{e} a
Writer.collecting : (Writer ->{Writer | e} a) ->{e} (a, Bytes)
```

`Reader.over` is the buffering handler. Its clauses answer from the activation
state and, when the buffer is short, call the source — which is how the
source's effects `e` reach the result row without appearing on any operation.

`Reader.limited` is a handler over a parent reader: three delegating clauses,
each one line, clamping to a remaining allowance held in its own activation
state. Chunked transfer decoding, a decompressor, and a TLS layer later are the
same shape. Because the reader is a value, a stage's parent stays reachable —
you can hold both the framed and the raw reader.

### Derived operations

Everything above the primitives is ordinary Fango:

```fango
type Read = Found Bytes | Ended Bytes | Overflowed

Reader.ensure : Reader -> Int ->{Reader} Bool
Reader.readUpTo : Reader -> Int ->{Reader} Bytes
Reader.readExactly : Reader -> Int ->{Reader} Maybe Bytes
Reader.readUntil : Reader -> Bytes -> Int ->{Reader} Read
Reader.readLine : Reader -> Int ->{Reader} Read
Reader.atEnd : Reader ->{Reader} Bool
Reader.chunks : Reader -> Stream Bytes {Reader}

Writer.write : Writer -> Bytes ->{Writer} ()
Writer.writeString : Writer -> String ->{Writer} ()
```

`ensure` loops on a real terminator rather than on a length comparison, and
everything else is built from it:

```fango
Reader.ensure reader n =
    if Bytes.length (reader.buffered()) >= n then True
    else if reader.refill() then Reader.ensure reader n
    else False

Reader.readUpTo reader n =
    ready = Reader.ensure reader 1
    window = reader.buffered()
    take = if Bytes.length window < n then Bytes.length window else n
    consumed = reader.skip take
    Bytes.slice 0 take window

Reader.readExactly reader n =
    if Reader.ensure reader n then
        text = Bytes.slice 0 n (reader.buffered())
        consumed = reader.skip n
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

Those `ready =` and `consumed =` bindings are a wart: a result cannot be
discarded — `_ = expr` reports `PATTERN BINDING` and a bare non-Unit statement
is a type error — so every ignored answer needs an invented name. Either
`skip` and `ensure` gain Unit-returning forms, or `_ =` becomes legal. Decide
before writing the library, because this shape recurs throughout it.

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

## Step 5 — sockets, HTTP, and a concurrent server

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

Same lifetime shape as `File`, with the same private handle table. Two
compiler-side questions come with it. Fallible natives that the boundary turns
into `Result IO.Error a` are spelled for the bundled `File` module alone, and
the [general case is deferred](roadmap-effects.md#deferred-topics) pending
resolved error identities; admitting a second bundled module is the smallest
form of that. And socket failures — connection refused, reset by peer, address
in use — have no member in `IO.Error`'s `Kind`.

HTTP is then ordinary Fango over two readers and a writer:

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

A proxy holds two readers at once, which is what step 2 bought:

```fango
Reader.over (Net.source client) \downstream ->
    Reader.over (Net.source upstream) \up ->
        relay downstream up
```

A connection per task depends on
[cooperative structured async](roadmap-effects.md#4-cooperative-structured-async).
A server handling one connection at a time needs none of it and is this
document's acceptance program.

## Compiler and runtime boundary

| Compiler or runtime | Ordinary Fango library |
| --- | --- |
| `Bytes` representation, slicing, scanning, UTF-8 validation | `Bytes` combinators |
| Handler activations, their state cells, and reification | `Reader` and `Writer` and every stage above them |
| Scope ownership and resource escape proofs | `Reader.over`, `Writer.over`, `Net` scopes |
| Counted file and socket reads and writes | `Source` and `Sink` adapters |
| — | Framing, limits, chunked decoding, HTTP |

`Bytes` and the adapters are library names and need no grammar change. The
reification form in step 2 does, and requires a TextMate update with
representative tokenization. Both backends must agree on the `Bytes`
representation; the interpreter uses the same `fangort` value the compiled
program does, as it does for `List`.

## Delivery and acceptance

Each step is usable without the ones after it.

**1. Defects.** The widening crash is fixed with a regression fixture on both
sides of the decision it forces. The diagnostic prints the differing rows. The
segfault repro above compiles and runs, with fixtures covering a
row-polymorphic handler instantiated at same-module, cross-module, and `IO`
effects.

**2. Handler instances.** Acceptance is in
[that document](roadmap-instances.md#a-reification).

**3. `Bytes`.** Differential interpreter/compiler coverage for every operation,
including empty, out-of-range, and invalid-UTF-8 inputs. A scan over a large
input allocates nothing per match. No native returns a slice of a buffer it
will write again.

**4. `Reader`/`Writer`.** The same parsing code passes over a memory source
and a file source, and the memory path performs no effects. Two readers are
driven alternately and neither disturbs the other. A writer emits one
underlying write per flush window. Reading a file through `Reader.chunks`
matches `File.read` byte for byte. Short pulls, a delimiter straddling a
refill, limit overflow, end of input mid-delimiter, a failed `readExactly`
consuming nothing, and `limited` leaving the parent correctly positioned are
all covered. A reader cannot outlive its source's scope.

**5. Sockets and HTTP.** A client fetches over a loopback connection; a
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
- How a discarded result is spelled, per the wart above.
- Whether a concrete effect row can be written as a type argument. It cannot
  today, so a source at a known row — `Source {IO, Fail IO.Error}` — is
  unspellable and such values must go unannotated, which collides with the
  rule that every library function carries a signature.

## Verification

Retain the [repository gates](../AGENTS.md) and
[verification contracts](design/verification.md): the Core linter, the
interpreter/compiler differential suite, functional tests, `go vet`, and
updated goldens for intentional language changes. Both backends must produce
identical bytes for every `Bytes` and reader fixture. Capture, escape, and
cursor fixtures must be rerun rather than assumed, because reification is the
first library use of an activation as a value.
