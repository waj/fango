# Roadmap: IO interception API catalogue

This is the detailed API companion to the
[IO interception proposal](roadmap-io-interception.md), not a standard-library
reference. All new declarations and changed rows are candidates. Implemented
APIs are linked to their authoritative source comments. Examples use `text`
because the proposed API is not available today.

The catalogue preserves current public call names where practical. It does not
promise that preserving a name preserves its type or behavior. Driver modules
are candidate declaration locations; effects reexported through facade modules
retain their original nominal identity. `Bytes.Source` and `Bytes.Sink` below
mean the existing row-indexed records from [Bytes](../stdlib/Bytes.fango).

## Common runner conventions

```text
runSystem : (() ->{Domain | e} a) ->{IO | e} a
runMock : Fixture -> (() ->{Domain | e} a) ->{e} Report a
```

These are schematic conventions, not generic declarations over a type named
`Domain`. Each real runner must have concrete handled effect labels. A runner
removes only the labels it supplies and preserves the full residual row `e`.
It must expose effects performed by its own clauses.

Mocks return immutable observations on normal completion. To observe state
after an application failure, put the relevant `Fail.attempt` inside the mock
runner. An abort escaping the runner does not also return a report. Capture
unrelated aborts only through an explicitly typed boundary; there is no
universal untyped exception bucket.

## Console

Current source: [Console](../stdlib/Console.fango). The proposed facade has the
same three functions, but requires `Console.Access` instead of `IO`:

```text
write : String ->{Access} ()
print : Display a => a ->{Access} ()
readLine : () ->{Access} Maybe IO.Line

print value = write (display value ++ "\n")
```

The driver has two operations:

```text
-- Candidate module Console.Driver, reexported by Console.
effect Access
    writeText : String -> ()
    readInput : () -> Maybe IO.Line
```

Keep `print`'s formatting in Fango. No operation-local `Display` dictionary or
polymorphic native operation is needed. `write` adds no newline. Input retains
the line ending as the current `IO.Line` contract specifies; `Nothing` means
EOF. The candidate does not add an error-returning overload to the console
convenience API. Recoverable handle IO remains a separate boundary.

```text
runSystem : (() ->{Access | e} a) ->{IO | e} a

runTest : List IO.Line
       -> (() ->{Access | e} a)
       ->{e} Transcript a

type Transcript a =
    { value : a
    , output : String
    , remaining : List IO.Line
    }
```

`runSystem` preserves the current fatal handling of terminal errors unless a
separate API change is approved during design. Its clauses use private system
helpers rather than recursively calling the new public facade. A Fango handler
around public Console operations does not catch Go panics merely by existing.

`runTest` consumes one scripted line per read, returns EOF when exhausted,
concatenates writes exactly, and reports unconsumed input. It neither inserts
nor normalizes newlines. The initial list, returned output, and final remaining
list are immutable values. Repeated runs start independently.

```text
greet() =
    print "Your name?"
    case readLine() of
        Nothing -> print "No input"
        Just line -> print ("Hello, " ++ line.text)

captured = Console.runTest [{ text = "Ana", ending = "\n" }] { greet() }
captured.output == "Your name?\nHello, Ana\n"
```

This boundary covers the Prelude helpers that delegate to Console. It does
not capture `IO.write IO.stdout`, stderr, or output performed directly in a
native sidecar. Raw standard-stream capture is described below. Decide whether
two interception levels are useful enough to keep before implementing both;
their overlap must be documented rather than relying on user intuition.

## Files and directories

Current source: [File](../stdlib/File.fango). The baseline keeps all eight public
functions and the current distinction between whole-file `Result` APIs and
scoped helpers that raise `Fail`:

```text
read : String ->{Access} Result IO.Error String
writeAll : String -> String ->{Access} Result IO.Error ()
isDirectory : String ->{Access} Result IO.Error Bool
size : String ->{Access} Result IO.Error Int
listDirectory : String ->{Access} Result IO.Error (List String)

withFile : String
        -> (IO.Handle ->{Access, IO.Stream, Fail IO.Error | e} a)
        ->{Access, IO.Stream, Fail IO.Error | e} a

withOutput : String
          -> (IO.Handle ->{Access, IO.Stream, Fail IO.Error | e} a)
          ->{Access, IO.Stream, Fail IO.Error | e} a

withAppend : String
          -> (IO.Handle ->{Access, IO.Stream, Fail IO.Error | e} a)
          ->{Access, IO.Stream, Fail IO.Error | e} a
```

The callback rows conservatively include shared handle IO. Precise inferred
rows may be smaller in individual helpers. Two effect families are visible
because File acquires a resource while IO operates on the shared `IO.Handle`.
Changing to file-specific operations or Source/Sink callbacks is an alternative,
not an already settled simplification.

The first driver inventory mirrors all current private native seams, including
whole-file fast paths:

```text
-- Candidate module File.Driver.
effect Access
    openRead : String -> Result IO.Error IO.Handle
    openWrite : String -> Result IO.Error IO.Handle
    openAppend : String -> Result IO.Error IO.Handle
    closeHandle : IO.Handle -> Result IO.Error ()
    readFileResult : String -> Result IO.Error String
    writeFileResult : String -> String -> Result IO.Error ()
    openDirectory : String -> Result IO.Error Directory
    readDirectoryEntry : Directory -> Result IO.Error String
    closeDirectory : Directory -> Result IO.Error ()
    isDirectoryPath : String -> Result IO.Error Bool
    fileSize : String -> Result IO.Error Int
```

This is a coverage inventory, not an argument that these eleven operations
must all become permanent public API. In particular:

- A structured open mode could replace three acquisition operations, but needs
  an exhaustive mode type and well-defined invalid mode handling.
- Whole-file text read/write could be derived from streaming, if error,
  malformed-text, cleanup, allocation, and performance behavior remain suitable.
- A higher-level directory-listing operation avoids exposing a Directory
  handle to custom handlers, at the cost of committing to materialization.
- If directory iteration stays primitive, define its EOF representation and
  ordering explicitly. Do not accidentally promote a private sentinel into a
  public contract just because the existing native returns `String`.

The selected public driver must expose the types custom handlers require.
Today's private Directory representation is not automatically such an API.

### File runners and fixtures

```text
runSystem : (() ->{Access, IO.Stream | e} a) ->{IO | e} a

runMemory : Image
         -> (() ->{Access, IO.Stream | e} a)
         ->{e} Snapshot a

type Image
type Snapshot a = { value : a, image : Image }

empty : Image
withDirectory : String -> Image -> Image
withTextFile : String -> String -> Image -> Image
withByteFile : String -> Bytes -> Image -> Image
textAt : String -> Image -> Result IO.Error String
```

`Image` is an opaque immutable fixture value, not a native handle table that
survives execution. `Snapshot.image` is an immutable observation after the
action and its scoped cleanup. The signature does not authorize references to
live mock state in either the image or an apparently pure returned callback.

Fixture constructors need a final decision about invalid paths and conflicting
entries. The shorthand above assumes a validated fixture policy; if validation
can fail, return a typed `Result` rather than silently dropping entries. Define
whether parents are created automatically, how directories and files conflict,
whether an existing entry is replaced, and how the virtual current directory
is selected. Do not use the host filesystem to resolve a pure fixture.

Mock semantics must cover read offsets, EOF, independent opens, append versus
truncate, permissions or explicit unsupported permission behavior, text/byte
conversion, metadata, enumeration, close, and failures. Size should use the
same unit as the real API. Path and error rendering must preserve the public
error vocabulary. Pick a deterministic listing policy only if compatible with
the promised real API, or explicitly limit assertions to order-independent
results.

```text
fixture = File.withTextFile "config.txt" "port=8080\n" File.empty

readFirstLine() =
    use handle <- File.withFile "config.txt"
    IO.readLine handle

snapshot = File.runMemory fixture {
    Fail.attempt { readFirstLine() }
}
```

Failure injection needs a further typed fixture vocabulary: fail opening a
particular path, fail after a byte count, fail close, and distinguish read from
write failures. Snapshot state must represent partial progress accurately.
Whole-file convenience calls must agree with streaming observations over the
same virtual files.

## Shared handles and standard streams

Current source: [IO](../stdlib/IO.fango). If the baseline preserves generic
handles, introduce `IO.Stream` while retaining `IO` as the real-native marker:

```text
type Handle
type Error
type Kind
type Line

stdin : Handle
stdout : Handle
stderr : Handle

readLine : Handle ->{Stream, Fail Error} Maybe Line
write : Handle -> String ->{Stream, Fail Error} ()
readBytes : Handle -> Int ->{Stream, Fail Error} Maybe Bytes
writeBytes : Handle -> Bytes ->{Stream, Fail Error} ()
source : Handle -> Bytes.Source {Stream, Fail Error}
sink : Handle -> Bytes.Sink {Stream, Fail Error}

lineText : String -> String
lineEnding : String -> String
describeError : Error -> String
```

The existing error and line data contracts remain authoritative in source.
`Stream` here is an effect label owned by IO, distinct from the existing
[Stream](../stdlib/Stream.fango) module and its traversal type. Review the name
for confusion before publication.

```text
-- Candidate module IO.Driver.
effect Stream
    handleHasInput : Handle -> Result Error Bool
    readHandleLine : Handle -> Result Error String
    readHandleBytes : Handle -> Int -> Result Error Bytes
    writeHandle : Handle -> String -> Result Error ()
    writeHandleBytes : Handle -> Bytes -> Result Error ()
```

As with File, this mirrors existing seams rather than declaring the inventory
minimal. A direct `Maybe`-returning read operation may avoid a separate
availability/read pair and make concurrent EOF behavior easier to specify.
Evaluate the behavioral and ABI consequences before selecting it. Do not
assume that locking each native call makes two adjacent calls atomic.

```text
runSystem : (() ->{Stream | e} a) ->{IO | e} a

runStandardTest : Bytes
               -> (() ->{Stream | e} a)
               ->{e} StandardTranscript a

type StandardTranscript a =
    { value : a, stdout : Bytes, stderr : Bytes }
```

The standard-test runner maps its initial bytes to stdin and captures stdout
and stderr independently. Unsupported file handles are rejected according to
the selected foreign-handle contract. It does not silently fall through to the
OS. `File.runMemory` supplies the Stream interpretation for its own handles;
composition between standard streams and virtual files needs an explicit
ownership/forwarding policy, described in the
[resource proposal](roadmap-io-interception-resources.md#nested-providers-and-foreign-handles).

```text
captured = IO.runStandardTest Bytes.empty {
    Fail.attempt { IO.write IO.stderr "warning\n" }
}
captured.stderr == Bytes.fromString "warning\n"
```

No general custom virtual-handle constructor is selected. Today's native
`IO.Handle` cannot be manufactured by ordinary application Fango code. A
public construction API must carry a real ownership and effect contract,
rather than exposing `Runtime.Native.Any` as an escape hatch.

## Networking

Current source: [Net](../stdlib/Net.fango). A complete interception boundary must
cover the entire existing surface, not only `withClient`:

```text
type Listener
type Connection
type Pool
type Error
type Kind

withListener : Int
            -> (Listener ->{Access, Fail Error | e} a)
            ->{Access, Fail Error | e} a
accept : Listener
      -> (Connection ->{Access, Fail Error | e} a)
      ->{Access, Fail Error | e} a
acceptAsync : Listener
           -> (Connection ->{Access, Fail Error, Async.Async | e} a)
           ->{Access, Fail Error, Async.Async | e} a
withClient : String -> Int
          -> (Connection ->{Access, Fail Error | e} a)
          ->{Access, Fail Error | e} a

dialTimeout : String -> Int -> Int ->{Access} Result Error Connection
dialTls : String -> Int -> Int -> String ->{Access} Result Error Connection
closeConnection : Connection ->{Access} Result Error ()
readConnectionBytes : Connection -> Int ->{Access} Result Error Bytes
writeConnectionBytes : Connection -> Bytes ->{Access} Result Error ()
setReadDeadline : Connection -> Int ->{Access} Result Error ()
setWriteDeadline : Connection -> Int ->{Access} Result Error ()

stopListener : Listener ->{Access} Result Error ()
listenerStopped : Listener ->{Access} Bool

source : Connection -> Bytes.Source {Access, Fail Error}
sink : Connection -> Bytes.Sink {Access, Fail Error}
sourceAsync : Connection ->{Async.Async} Bytes.Source {Access, Fail Error}
sinkAsync : Connection ->{Async.Async} Bytes.Sink {Access, Fail Error}

newPool : Int -> Int ->{Access} Pool
poolConnect : Pool -> String -> Int -> Int -> Bool -> String -> Bool
           ->{Access} Result Error Connection
poolRelease : Connection -> Bool ->{Access} Result Error ()
connectionReused : Connection ->{Access} Bool
closePool : Pool ->{Access} Result Error ()
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

One `Net.Access` effect is the catalogue baseline. Splitting it into listener,
connection, and pool effects may reduce custom-handler size but creates more
effect rows and runner dependencies. Review real server and client examples
before choosing. Keep the error distinctions used by HTTP transport mapping.

### Network runners and script design

```text
runSystem : (() ->{Access | e} a) ->{IO | e} a

runScript : Script
          -> (() ->{Access | e} a)
          ->{e} Report a

type Script
type Report a
```

The `runSystem` shape above covers the synchronous candidate. Cancellable
variants may need a different signature once their capability is selected.
`Script` and `Report` are design placeholders, not opaque types that excuse
omitting their construction and observation API.

The fixture vocabulary must represent:

- Expected endpoint, TLS mode and name, connection success/failure, and stable
  connection identity within a run.
- Inbound byte chunks, EOF, read failure, and timeout as distinct events.
- Expected outbound bytes, partial progress where supported, write failure,
  and how expectations match calls with different chunk boundaries.
- Listener binding, incoming connection ordering, stopping, and later accept
  attempts.
- Deadline updates and their observation; no claim that returning a timeout
  simulates elapsed scheduling time.
- Pool acquisition/reuse/release, unusable connections, capacity/expiry policy,
  and provider identity checks.
- Cancellation for supported operations, cleanup, unused expectations,
  unexpected operations, and mismatches with useful diagnostics.

The report needs the action result, consumed/remaining expectations, and an
operation trace with typed identifiers. Decide whether mismatches are typed
test failures, ordinary Net errors, or a separate fixture error effect. Do not
conflate a broken test script with a simulated network failure.

For concurrency, specify per-connection order and causal constraints rather
than assuming one global order of all task events. A deterministic scheduler
is not included. A script that answers reads immediately is useful without
simulating blocked task execution.

## Process arguments and exit

Current source: [Process](../stdlib/Process.fango). Arguments and exit have
different operation disciplines:

```text
effect Arguments
    args : () -> List String

effect Exit
    abort exit : Int -> a

withArgs : List String
         -> (() ->{Arguments | e} a) ->{e} a

captureExit : (() ->{Exit | e} a) ->{e} Result Int a

runSystem : (() ->{Arguments, Exit | e} ()) ->{IO | e} ()
```

The whole-argument operation can be interpreted by the current count/index
native helpers without making those helpers public. Arguments retain the
existing rule that the executable name is excluded.

`captureExit` answers `Ok value` for normal completion and `Err status` for a
language-level exit request. It does not catch unrelated `Fail` effects.

```text
choose() =
    case Process.args() of
        [] -> Process.exit 2
        _ -> 42

test = Process.withArgs [] { Process.captureExit { choose() } }
test == Err 2
```

This changes `Process.exit` from the current immediate native `Int ->{IO} ()`
to an explicitly non-returning language operation. The proposed runner would
let language cleanup unwind before requesting host termination. This requires
separate review of cleanup failures, the selected exit status, interpreter/REPL
behavior, and child-task exit requests. If immediate emergency termination is
retained, give it a separate explicit contract. Never implement exit as a
resumptive operation that returns `()` and continues the caller.

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
runNet : (() ->{Http | e} a) ->{Net.Access | e} a

-- Candidate implementation of the existing convenience runner:
run action = Net.runSystem { runNet action }
```

The transport-level test would install `Net.runScript` around `Client.runNet`,
so the real HTTP parser sees the scripted bytes. Application tests can keep
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
callbacks. Mutable mock operations need operation-level synchronization, not
only protected state-cell loads/stores.

```text
captured = Console.runTest [] {
    Async.run {
        a = Async.spawn { print "A" }
        b = Async.spawn { print "B" }
        Async.await a
        Async.await b
    }
}
```

Both writes should be captured without loss, but their order is unspecified.
The computation still requires IO. The fixture does not own a new scheduler.

[Reader](../stdlib/Reader.fango), [Writer](../stdlib/Writer.fango), and text
adapters should continue to operate over their existing row-indexed inputs.
Changing the effect of a source/sink must propagate through those types rather
than creating a dedicated effect for each helper. If the resource alternative
changes File's callback shape, document that migration explicitly.
