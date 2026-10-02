# JSON pull parser

`Json.Pull` is the token-level layer under [`Json`](library-json.md): the
`Pull` effect and its handlers, helpers for walking containers, and the
buffered scan protocol that `Decode` instances implement. Everyday parsing
and encoding need only `Json`; import this module to write a `Decode`
instance by hand or to consume a document token by token.

```fango
import Json
import Json.Pull exposing (Decode(..), Pull, decodeValue)
```

`Json.Pull.Error`, `Decode`, `Value`, and `Number` are the same declarations
`Json` re-exports, so `Json.Error` and `Json.Pull.Error` name one type.

[Reference index](../reference.md). Source: [Json.Pull](../../stdlib/Json/Pull.fango).

## Custom decoders

`Decode` has one method:

```fango
class Decode a
    scanValue : Json.Pull.Scan -> Result (() ->{Json.Pull.Pull, Fail Json.Error} Json.Pull.ScanStep a) (a, Json.Pull.Scan)
```

`Json.Pull.decodeValue : Decode a => () ->{Pull, Fail Json.Error} a` is the
common driver for every instance. Import it separately from `Decode(..)` when
using it unqualified. It starts `scanValue` at the current cursor, runs suspended
work, and publishes the completed position. `scanValue` starts a
pure buffered scan. `Ok (value, rest)` represents the same complete value and
remaining nesting allowance as `decodeValue`, at the position immediately
after that value. `Err action` suspends work; it is not a JSON error. Creating
that action consumes nothing, refills nothing, and performs no consumer effects.
`runScan result` executes suspended work until it returns a value and
position, or raises a JSON or source failure. A suspended action returns
`ScanDone value rest` or `ScanContinue nextResult`. The driver runs
continuations with bounded stack use.

`Scan` is opaque. Manual instances provide only `scanValue`. A decoder
written with Pull operations can use `scanValue cursor = Json.Pull.scanFromPull cursor { ... }`,
with its token-consuming body in the suspended action. The helper defers
publication of the supplied cursor and execution of the
decoder until `runScan` reaches it. Custom wrappers can compose a bundled scan
with `Json.Pull.mapScan transform result`, which transforms both buffered and resumed
results while preserving the decoder's semantics. Calling `decodeValue`
for a child invokes that child's single parser through the common driver.

Generated records retain previously decoded fields across buffer boundaries,
extra keys, alternative key escapes, and children that defer scanning. After
handling the suspended part, they continue scanning the current window. Pending
lookahead at entry is honored by that same parser. There is no second record
or list decoder selected at a buffer boundary.
Required fields in declaration order take a straight-line path; other orders
use a key loop. Defaults and skipped fields share the resumable loop, and
default expressions execute only when required. Lists likewise retain their
completed elements. JSON errors preserve their positions, paths, and consumed
input across these transitions.
Strings retain completed fragments, numeric tokens retain their grammar phase,
integers retain their accumulator, and literals retain their remaining suffix
across refills. A refill does not restart a completed scalar prefix.

## Pull handler

`withReader reader { ... }` installs the `Pull` effect over a byte `Reader`,
decoding UTF-8; `withTextReader` takes a `Text.Reader.Reader e`, whose adapter
selects the encoding. Both return `Result Json.Error a`. `next()` consumes a
`Token`, which derives `Eq` and `Show`; `peek()` reads ahead without consuming
it, and `at()` returns the current source position as a `Json.Error` with an
empty message, publishing buffered consumption. `fail message` raises a
`Json.Error` with that message at the current path. Its position is the start
of the token most recently returned by `next()` or `peek()` or consumed by a
`decoded*` operation, so a decoder that rejects a token reports where that
token begins. Once the cursor moves on by another route (`nextElement`,
`nextKey`, `decodeValue`, or `acceptScan`), `fail` uses the current position,
as `at()` does; pending lookahead keeps its start.

`beginArray`, `nextElement`, `beginObject`, `nextKey`,
and `skipValue` help decoders consume a container. `nextElement first` consumes
an array separator or closing bracket, returning whether another value follows;
it leaves that value unscanned for the decoder. `nextKey first` consumes an
object separator, key, and colon, returning the key or `Nothing` at the end.
Both honor an existing token from `peek()`. `withKey key { ... }`
adds a key segment, and `withIndex index { ... }` an element index, to errors
raised while a custom decoder handles a nested value.
`skipValue` validates and discards a value without building a tree.
`readValue()` builds the generic [`Value`](library-json.md#value-tree) tree
from the current token.

When `withReader` or `withTextReader`
returns, normally or with `Err`, the reader is positioned after the last token
the parser scanned, including a lookahead token obtained by `peek`. The same
reconciliation occurs when an unrelated effect exits the scope. Within the
scope, the underlying text and byte reader positions can lag behind the JSON
cursor until a refill or `at()`; advance them only after leaving the pull scope.

`decodedString()`, `decodedInt()`, and `decodedFloat()`
consume a scalar and return `Result String a`: a type or conversion mismatch
returns its diagnostic message after consuming the token. Lexical and encoding
failures are handled by the pull handler as usual. The primitive
`Decode` instances turn these mismatch messages into `Json.Error` at the
start of the scalar and the current path.

`bufferedScan()` obtains a speculative view of the current Pull cursor,
or `Nothing` when a lookahead token is pending. `currentScan()` also
represents pending lookahead so that resumed scans can honor it. Neither
operation consumes input. Abandoning a scan result without running it consumes
nothing.
`acceptScan rest` publishes the position returned by a completed scan or
`runScan`. Publish it immediately, before other parser operations. A
suspension may have already published its prefix and refilled the reader;
publication of the final position reconciles the remaining buffered work.
The reader is committed at the usual refill, `at()`, or scope boundary.

`Pull` exposes only these operations; the handler's resumption and path
operations are private to the module.
