# Standard services

Library calls that reach the host go through small domain effects with
replaceable runners. `IO` marks only the code that reaches the host directly:
the system runners themselves and the boundaries listed under
[direct native boundaries](#direct-native-boundaries). An `{IO}` annotation
alone never makes a call interceptable.

A program names the services it uses in `main`'s row, and
[entry assembly](commands.md#default-handlers) installs their system runners.
A test or program installs another runner around the code it wants to
simulate. Each runner removes only its own effect and keeps every other effect
in the callback's row: a simulated service can make a computation pure only
when nothing else in its row needs `IO`.

## Interpreted services

| Service | Effect | Public calls | System runner | Other runners |
| --- | --- | --- | --- | --- |
| Standard streams | `Console.Console` | `print`, `write`, `writeTo`, `readLine`, `Console.source`/`sink`, and Prelude helpers over them | `Console.runSystem` | `Console.runTest` |
| Files and directories | `File.FileSystem` | Scoped and raw handles, line/byte reads and writes, `File.source`/`sink`, whole-file reads and writes, directory tests, sizes, listings | `File.runSystem` | `File.runMemory`, `File.runMemoryWith`, custom handlers through `File.withProvider` |
| Process arguments | `Process.Arguments` | `Process.args` | `Process.runSystem` | `Process.withArgs` |
| Randomness | `Random.Random` | `Random.int` | `Random.runSystem` | `Random.runSeeded` |
| Clock | `Time.Clock.Clock` | `now`, `nowIn`, `today`, `todayIn`, `withZone` | `runSystem`, `runSystemIn`, `runOffset` | `runFixed`, `runStepped` |
| HTTP client | `Http.Client.Http` | Request functions of [Http.Client](../../stdlib/Http/Client.fango) | `Http.Client.run` | `stub`, `mock` |

The source comments of each module own the operation lists, failure policies,
and fixture semantics. Generic helpers such as Reader, Writer, Text, and Json
streaming take a `Bytes.Source`, `Bytes.Sink`, `Reader`, or `Writer` whose row
names the service behind it; they add no effect of their own.

A program's exit status is not a service: `main` returns a `Process.Status`,
and [`Test.run`](../../stdlib/Test.fango) answers one for it to return.

## Direct native boundaries

These calls require `IO` and have no interceptable operation. Replacing them
means not calling them, or wrapping them in an application-level effect.

| Boundary | Calls | Reason |
| --- | --- | --- |
| Networking | Every function of [Net](../../stdlib/Net.fango): listeners, connections, TLS, pools, deadlines, sources and sinks | Interception is [unfinished](../roadmap-io-interception.md#ioi6-network-interpretation) |
| HTTP transport | `Http.Server.serve`, and the socket path inside `Http.Client.run` | Waits on the Net interpretation; [roadmap](../roadmap-io-interception.md#ioi7-http-and-task-integration) |
| Task runtime | `Async.run`, `Async.runOutcome`, and the waits they make | Tasks are real goroutines; no runner simulates scheduling |
| Time zone database | `Time.Zone.load`, `Time.Zone.system` | Host zone lookup; pass a `Zone` value instead to keep code pure |
| Process exit | `Process.exit` | Ends the process at once, skipping cleanup |
| Clock and entropy natives | Inside `Time.Clock.runSystem`/`runOffset` and `Random.runSystem` | Reached only by those runners |

Natives inside a Go sidecar are not visible to handlers. A sidecar call is
interceptable only where its module exposes an effect operation for it, as
listed above.
