# Roadmap: IO interception API catalogue

This is the detailed API companion to the
[IO interception proposal](roadmap-io-interception.md), not a standard-library
reference. All new declarations and changed rows are candidates. Implemented
APIs are linked to their authoritative source comments. Examples use `text`
because the proposed API is not available today.

The catalogue preserves current public call names where practical. It does not
promise that preserving a name preserves its type or behavior. Effects are
declared in their domain module with public operations, as `Console` is;
the "Driver" module names below predate that rule. `Bytes.Source` and `Bytes.Sink` below
mean the existing row-indexed records from [Bytes](../stdlib/Bytes.fango).

## Common runner conventions

```text
runSystem : (() ->{Domain | e} a) ->{IO | e} a
runMock : Fixture -> (() ->{Domain | e} a) ->{e} Report a
```

These are schematic conventions, not generic declarations over a type named
`Domain`. Each real runner must have concrete handled effect labels. A system
runner with this shape is declared as its effect's default handler, which
[entry assembly](roadmap-io-interception.md#entry-assembly) installs for
effects in `main`'s row. Mock runners are never defaults. A runner
removes only the labels it supplies and preserves the full residual row `e`.
It must expose effects performed by its own clauses.

Mocks return immutable observations on normal completion. To observe state
after an application failure, put the relevant `Fail.attempt` inside the mock
runner. An abort escaping the runner does not also return a report. Capture
unrelated aborts only through an explicitly typed boundary; there is no
universal untyped exception bucket.

## Console

Implemented: see [Console](../stdlib/Console.fango) and the
[standard streams design](design/effects.md#standard-streams). The effect is
declared in the `Console` module itself, with its operations public, rather
than in a separate driver module; later drivers follow the same rule. The
standard-stream `IO` declarations remain until IOI4 retires them.

## Files and directories

Whole-file and directory calls, `FileSystem`'s five public operations,
`runSystem`, `runMemory`, and the immutable `Image`/`Snapshot` API are
implemented in [File](../stdlib/File.fango). Their boundary is specified in the
[design](design/effects.md#whole-file-services). `listDirectory` is itself an
operation; custom handlers need no directory resource. Fixture constructors
return `Result IO.Error Image`, with path normalization and conflict behavior
documented in File's source.

The remaining proposal moves scoped helpers from `IO.Handle` to a file-specific
handle and extends the same effect with streaming operations:

```text
type Handle

withFile : String
        -> (Handle ->{FileSystem, Fail IO.Error | e} a)
        ->{FileSystem, Fail IO.Error | e} a

withOutput : String
          -> (Handle ->{FileSystem, Fail IO.Error | e} a)
          ->{FileSystem, Fail IO.Error | e} a

withAppend : String
          -> (Handle ->{FileSystem, Fail IO.Error | e} a)
          ->{FileSystem, Fail IO.Error | e} a

readLine : Handle ->{FileSystem, Fail IO.Error} Maybe IO.Line
readBytes : Handle -> Int ->{FileSystem, Fail IO.Error} Maybe Bytes
write : Handle -> String ->{FileSystem, Fail IO.Error} ()
writeBytes : Handle -> Bytes ->{FileSystem, Fail IO.Error} ()
source : Handle -> Bytes.Source {FileSystem, Fail IO.Error}
sink : Handle -> Bytes.Sink {FileSystem, Fail IO.Error}
```

One effect covers both acquisition and handle IO, so file code has one
service row. A file handle is not interchangeable with a standard stream.
Generic parsing and output code take a `Bytes.Source`, `Bytes.Sink`,
`Reader`, or `Writer` instead.

The remaining driver inventory follows the existing private streaming seams:

```text
-- Additional operations of File.FileSystem.
openRead : String -> Result IO.Error Handle
openWrite : String -> Result IO.Error Handle
openAppend : String -> Result IO.Error Handle
closeHandle : Handle -> Result IO.Error ()
readHandleLine : Handle -> Result IO.Error (Maybe IO.Line)
readHandleBytes : Handle -> Int -> Result IO.Error (Maybe Bytes)
writeHandleBytes : Handle -> Bytes -> Result IO.Error ()
```

A structured open mode could replace the three acquisition operations, but
needs an exhaustive mode type and well-defined invalid mode handling. The
selected public driver must expose the checked types custom handlers need.

### File runners and fixtures

The implemented whole-file fixture constructors, lookup, and runner are
[File's API](../stdlib/File.fango). Streaming interpretation must extend the
same image and preserve its immutable snapshot contract. The current runner
has no live resource state, models no permissions or symlinks, and supports
sequential use; this does not settle provider identity or shared handler state.

Mock semantics must cover read offsets, EOF, independent opens, append versus
truncate, permissions or explicit unsupported permission behavior, text/byte
conversion, metadata, enumeration, close, and failures. Size should use the
same unit as the real API. Path and error rendering must preserve the public
error vocabulary. Pick a deterministic listing policy only if compatible with
the promised real API, or explicitly limit assertions to order-independent
results.

```text
fixtureResult = File.withTextFile "config.txt" "port=8080\n" File.empty
fixture = Fail.fromResult fixtureResult -- inside an enclosing Fail boundary

readFirstLine() =
    use handle <- File.withFile "config.txt"
    File.readLine handle

snapshot = File.runMemory fixture {
    Fail.attempt { readFirstLine() }
}
```

Failure injection needs a further typed fixture vocabulary: fail opening a
particular path, fail after a byte count, fail close, and distinguish read from
write failures. Snapshot state must represent partial progress accurately.
Whole-file convenience calls must agree with streaming observations over the
same virtual files.

## Retiring the shared handle

Current source: [IO](../stdlib/IO.fango). Standard streams move to Console and
file handles to File, so IO no longer has a handle type or handle operations.
There is no generic handle-IO effect; the
[resource proposal](roadmap-io-interception-resources.md#resource-representation)
explains why. IO keeps the real-native marker and the data shared by every
domain:

```text
effect IO

type Error
type Kind
type Line

describeError : Error -> String
lineText : String -> String
lineEnding : String -> String
```

The existing error and line data contracts remain authoritative in source.
Errors on standard streams keep the paths `stdin`, `stdout`, and `stderr`.
`IO.Handle`, `stdin`, `stdout`, `stderr`, `readLine`, `write`, `readBytes`,
`writeBytes`, `source`, and `sink` are removed, together with the Prelude's
reexports of the three standard handles.

The Console system runner and File share the conversion from a raw native read
to `Maybe Line` or `Maybe Bytes` and the chunk size of their sources, rather
than copying them. Place these helpers in IO or a library-internal module.

Code that chose between a standard stream and a file through a handle takes a
sink or writer instead:

```text
-- Before
report out = IO.write out "done\n"
report (if toFile then file else IO.stdout)

-- After
report : Bytes.Sink {e} ->{e} ()
report out = out.write (Bytes.fromString "done\n")
report (if toFile then File.sink file else Console.sink Stdout)
```

The sink's row records which service each choice uses. Both branches must
agree on one row, so the caller's row includes both services.

## Networking

Current source: [Net](../stdlib/Net.fango). Net already has domain-specific
handles: connection reads and writes are Net functions on `Net.Connection`,
not generic handle IO. A complete interception boundary must cover the entire
existing surface, not only `withClient`:

```text
type Listener
type Connection
type Pool
type Error
type Kind

withListener : Int
            -> (Listener ->{Network, Fail Error | e} a)
            ->{Network, Fail Error | e} a
accept : Listener
      -> (Connection ->{Network, Fail Error | e} a)
      ->{Network, Fail Error | e} a
acceptAsync : Listener
           -> (Connection ->{Network, Fail Error, Async.Async | e} a)
           ->{Network, Fail Error, Async.Async | e} a
withClient : String -> Int
          -> (Connection ->{Network, Fail Error | e} a)
          ->{Network, Fail Error | e} a

dialTimeout : String -> Int -> Int ->{Network} Result Error Connection
dialTls : String -> Int -> Int -> String ->{Network} Result Error Connection
closeConnection : Connection ->{Network} Result Error ()
readConnectionBytes : Connection -> Int ->{Network} Result Error Bytes
writeConnectionBytes : Connection -> Bytes ->{Network} Result Error ()
setReadDeadline : Connection -> Int ->{Network} Result Error ()
setWriteDeadline : Connection -> Int ->{Network} Result Error ()

stopListener : Listener ->{Network} Result Error ()
listenerStopped : Listener ->{Network} Bool

source : Connection -> Bytes.Source {Network, Fail Error}
sink : Connection -> Bytes.Sink {Network, Fail Error}
sourceAsync : Connection ->{Async.Async} Bytes.Source {Network, Fail Error}
sinkAsync : Connection ->{Async.Async} Bytes.Sink {Network, Fail Error}

newPool : Int -> Int ->{Network} Pool
poolConnect : Pool -> String -> Int -> Int -> Bool -> String -> Bool
           ->{Network} Result Error Connection
poolRelease : Connection -> Bool ->{Network} Result Error ()
connectionReused : Connection ->{Network} Bool
closePool : Pool ->{Network} Result Error ()
```

These signatures deliberately expose today's awkward parameter-heavy calls
instead of hiding them behind an unspecified ergonomic facade. An Endpoint or
connection-options record could improve the API, but is a distinct migration
decision. Preserve the source's meanings and units for timeouts, TLS hostnames,
certificate inputs, and pooling options.

### Network operation inventory

| Operation family | Existing native seams to cover |
| --- | --- |
| Listener lifecycle | `listen`, `closeListener`, `stopListener`, `listenerStopped` |
| Accept | `acceptConnection`, `acceptConnectionAsync` |
| Connection creation | `dial`, `dialTimeout`, `dialTls` |
| Connection lifecycle | `closeConnection`, `connectionHasInput` |
| Byte transport | `readConnectionBytes`, `writeConnectionBytes`, `readConnectionBytesAsync`, `writeConnectionBytesAsync` |
| Deadlines | `setReadDeadline`, `setWriteDeadline` |
| Pools | `newPool`, `poolConnect`, `poolRelease`, `connectionReused`, `closePool` |

The public driver signatures are not closed. Async native seams currently take
a private runtime context token. A checked cancellation capability must replace
that detail at a public operation boundary. Merely copying the native type
`Runtime.Native.Any` into the driver would not constitute a finished API.

Options include explicit synchronous/cancellable operation pairs, a checked
opaque cancellation capability, or a runner whose clauses have a declared
Async dependency. Each choice must account for the runner's residual effects
and handler inheritance. Do not silently make a synchronous runner require
Async because an unused clause references it.

One `Network` effect is the catalogue baseline. Splitting it into listener,
connection, and pool effects may reduce custom-handler size but creates more
effect rows and runner dependencies. Review real server and client examples
before choosing. Keep the error distinctions used by HTTP transport mapping.

### Network runners

```text
runSystem : (() ->{Network | e} a) ->{IO | e} a
```

The shape above covers the synchronous candidate. Cancellable variants may
need a different signature once their capability is selected.

The standard library provides the seam and the system runner, not simulated
networks. Its own transport tests keep real loopback sockets, and HTTP
application tests keep `Client.stub` and `Client.mock`. Scripted network
fixtures (expected endpoints, inbound chunks, outbound expectations, listener
and pool scripts) are left to libraries. A library can write one as an
ordinary `Network` handler once the driver provides:

- Public operation names and argument/result types.
- A checked way to construct `Connection`, `Listener`, and `Pool` values owned
  by a non-native provider, as specified by IOI3.
- A public cancellation contract that a custom handler can observe.

These are requirements for any custom Net handler, including small
hand-written test handlers, not extra work for scripts alone.

## Process arguments and exit

Implemented: see [Process](../stdlib/Process.fango) for the `Arguments`
effect, its runners, `Status`, and `exit`, and the
[entry points reference](reference/commands.md#entry-points) for `main`'s
shape and status. `Process.runSystem` is the `Arguments`
[default handler](reference/commands.md#default-handlers).

## Clock and randomness

[Time.Clock](../stdlib/Time/Clock.fango) and [Random](../stdlib/Random.fango)
already implement the desired domain pattern. Keep their API rather than
introducing parallel IO-interception variants.

| Existing effect | Primitive operations | Ordinary helpers | Existing runners |
| --- | --- | --- | --- |
| `Clock` | `instant`, `zone` | `now`, `nowIn`, `today`, `todayIn` | `runSystem`, `runSystemIn`, `runFixed`, `runStepped`, `runOffset`, `withZone` |
| `Random` | `int` | — | `runSystem`, `runSeeded` |

System clock and entropy runners introduce IO. Fixed/stepped clocks and seeded
randomness do not introduce it. Neither effect has an ambient default handler.
Clock offsets still observe real elapsed time. The seeded generator retains
its documented non-cryptographic semantics.

```text
roll = Random.runSeeded 7 { Random.int 1 6 }
Random.runSeeded 7 { Random.int 1 6 } == roll
```

Do not claim that freezing wall-clock observations makes `Async.sleep` virtual,
changes socket deadline implementation, or establishes deterministic ordering
between concurrent random draws. Those are separate execution contracts.

## HTTP client and server

Current source: [Http.Client](../stdlib/Http/Client.fango). Preserve its public
types and operations:

- `Config`, `Connection`, `Error`, `Http`, `Reply`, `Request`, `Response`.
- Request construction: `request`, `get`, `post`, `withHeader`, `withBearer`,
  `withBody`, `compressRequest`.
- Execution and consumption: `send`, `withResponse`, `fetch`, `getBytes`,
  `getText`, `getJson`, `postJson`, `expectSuccess`.
- Configuration and interpretation: `configure`, `defaultConfig`, `run`,
  `stub`, `mock`.

The existing internal Http effect has `connect`, `transmit`, `receive`,
`release`, and `currentConfig` operations. That does not make its Endpoint and
driver operations a new public authoring API. Existing `stub` and `mock` remain
the supported compact entry points for application tests.

```text
-- Existing public runner shape:
run : (() ->{Http | e} a) ->{IO | e} a

-- Proposed additional interpretation:
runNet : (() ->{Http | e} a) ->{Network | e} a

-- Candidate implementation of the existing convenience runner:
run action = Net.runSystem { runNet action }
```

A transport-level test installs a custom `Network` handler, such as one
from a scripted-fixture library, around `Client.runNet`, so the real HTTP
parser sees its bytes. Application tests can keep
using `Client.stub` or `Client.mock`, avoiding unnecessary transport fixtures.
`Client.run` explicitly selects real Net service and therefore should not be
used when the intention is to test through an outer fake Net handler.

Keep the scoped response-reader/body contract, configuration behavior,
redirects, decompression, pooling, and error translation. Route the server's
transport through the chosen Net driver while leaving HTTP routing and framing
as ordinary library work. Protocol additions remain in the
[HTTP roadmap](roadmap-io.md); unresolved request-level transport policy remains
in the [client roadmap](roadmap-http-client.md#open-questions).

## Tasks, channels, and adapters

Current source: [Async](../stdlib/Async.fango). The IO proposal does not turn
every public task function into an algebraic effect operation. Preserve the
existing API families:

- Lifetimes: `run`, `runOutcome`, `scope`.
- Creation: `spawn`, `spawnAttempt`, `completed`.
- Observation: `await`, `awaitOutcome`, `wait`, `awaitAll`, `awaitAny`,
  `awaitAnyOutcome`.
- Combinators: `all`, `both`, `race`.
- Cancellation and timing: `cancel`, `cancelled`, `checkpoint`, `sleep`.
- Channels: `channel`, `send`, `receive`, `close`.
- Native integration: `contextToken`; pure parallel work: `parMap`.
- Public types/effect: `Task`, `Outcome`, `Channel`, `Async`.

Mocks installed outside `Async.run` can be inherited where the current task
contract permits. Application aborts must still be handled inside child
callbacks.

```text
captured = Console.runTest Bytes.empty {
    Async.run {
        a = Async.spawn { print "A" }
        b = Async.spawn { print "B" }
        Async.await a
        Async.await b
    }
}
```

Under the current handler-state rule this can lose a write: each task's clause
appends to a snapshot of the captured output, and a later commit can replace
an earlier one. The stdlib test runners are therefore sequential fixtures
until the [concurrency question](roadmap-io-interception-resources.md#mutable-fixtures-and-concurrency)
is decided. The computation still requires IO, and the fixture does not own a
new scheduler.

[Reader](../stdlib/Reader.fango), [Writer](../stdlib/Writer.fango), and text
adapters should continue to operate over their existing row-indexed inputs.
Changing the effect of a source/sink must propagate through those types rather
than creating a dedicated effect for each helper. `Console.source`,
`Console.sink`, `File.source`, `File.sink`, and the existing Net adapters
supply those inputs in place of `IO.source` and `IO.sink`.
