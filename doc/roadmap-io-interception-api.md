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
standard streams have no handle values; raw native access stays private.

## Files and directories

FileSystem, all streaming and whole-file operations, scoped and raw handles,
provider construction, memory images, faults, snapshots, and the runners are
documented in [File](../stdlib/File.fango). [File services](design/effects.md#file-services)
and [resource identity](design/ownership.md#file-provider-identity) own the
implemented architecture. There is no separate File.Driver module.

### File runners and fixtures

File's source owns fixture path normalization, directory conflicts, append and
truncate rules, offsets, EOF, partial progress, failure injection, and snapshot
semantics. The memory runner is sequential and does not model permissions or
symlinks. Concurrent consistency remains in the
[shared-state question](roadmap-io-interception-resources.md#mutable-fixtures-and-concurrency).

## Retiring the shared handle

Completed: [IO](../stdlib/IO.fango) keeps the native-interaction marker and
shared error/line data. Console owns standard streams; File owns file handles.
There is no shared Handle or standard-handle value. Source/Sink and Reader/Writer
values carry the row of their backing service, and Runtime.IO shares raw
line/byte conversions and the read bound.

## Networking

Implemented: see [Net](../stdlib/Net.fango),
[network services](design/effects.md#network-services), and
[resource ownership](design/ownership.md#file-provider-identity). Dialing takes
an `Endpoint` record rather than positional host, timeout, and TLS arguments,
and `Net.Cancel` is the checked cancellation argument of accepts, reads, and
writes. The standard library ships only the system runner; scripted network
fixtures are left to libraries, written as ordinary `Network` handlers.

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
supply those inputs for standard streams, files, and connections.
