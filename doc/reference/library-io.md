# IO, console, process, files, and sockets

[Reference index](../reference.md). Sources: [IO](../../stdlib/IO.fango),
[Console](../../stdlib/Console.fango), [Process](../../stdlib/Process.fango),
[File](../../stdlib/File.fango), and [Net](../../stdlib/Net.fango).

## IO

`IO` is the ambient, operation-free effect for external IO. The module owns
an opaque `Handle` shared by open files and standard streams. `stdin`, `stdout`,
and `stderr` are process-owned handles, also available unqualified through
Prelude. Standard handles resolve the active interpreter or REPL session on
every operation; they do not refer to the native worker's own streams.

```fango
import Fail exposing (attempt)
import Result exposing (Result(..))

main() =
    case attempt { IO.write stderr "a diagnostic\n" } of
        Ok () -> ()
        Err error -> print (IO.describeError error)
```

Handle operations have these types:

```fango
readLine : Handle ->{IO, Fail Error} Maybe Line
readBytes : Handle -> Int ->{IO, Fail Error} Maybe Bytes
write : Handle -> String ->{IO, Fail Error} ()
writeBytes : Handle -> Bytes ->{IO, Fail Error} ()
source : Handle -> Bytes.Source {IO, Fail Error}
sink : Handle -> Bytes.Sink {IO, Fail Error}
```

`readLine` returns `Nothing` at clean end of input and otherwise a nominal
`Line = { text : String, ending : String }`. The exact terminator is `"\n"`,
`"\r\n"`, or `""` for an unterminated final line. Malformed UTF-8 is replaced
with U+FFFD. Pure `lineText` and `lineEnding` helpers split raw lines the same way.

`readBytes handle count` returns `Nothing` at EOF and otherwise up to `count`
bytes, capped at 64 KiB per read. A non-positive count returns an empty `Bytes`
while input remains, and `Nothing` at EOF. Line and byte reads share the same
input buffer and may interleave. Byte reads preserve arbitrary binary data.

`write` writes the string exactly as given; `writeBytes` writes all supplied
bytes. Neither adds buffering or a newline. Complete reads and output writes
are serialized per handle or standard endpoint. `source` and `sink` adapt handles to
the [buffered reader and writer](library-readers.md) interfaces without taking
ownership or closing them.

`stdin` is readable; `stdout` and `stderr` are writable. Using the wrong direction
raises `Fail Error` with kind `Other` and path `stdin`, `stdout`, or `stderr`.
Other standard-stream errors use the same endpoint names. File errors retain
the caller's supplied path. Handles have no public constructor, `Show`, or `Eq`.
There is no public close operation. Standard handles have process lifetime;
file handles remain valid only until their owning File scope closes.

Structured failures are values of `IO.Error`:

```fango
type Kind = NotFound | PermissionDenied | AlreadyExists | IsDirectory | NotDirectory | Other
    deriving (Eq, Show)

type Error = { kind : Kind, path : String, message : String } deriving (Eq, Show)

describeError : Error -> String
```

`kind` classifies what went wrong. Known kinds have stable messages:
`no such file or directory`, `permission denied`, `file exists`, `is a directory`,
and `not a directory`, respectively. `Other` preserves the underlying system
message. Match `kind` for portable behavior. `describeError` renders
`path: reason`, using stable text for known kinds.

## Console

Console supplies IO-only conveniences, available unqualified through Prelude:

```fango
print : Display a => a ->{IO} ()
write : String ->{IO} ()
readLine : () ->{IO} Maybe IO.Line
```

`write` writes to stdout; `print` writes `display value` followed by `"\n"`.
`readLine()` reads from stdin with the handle operation's line contract.
These functions catch `Fail IO.Error` and panic with `IO.describeError` on
failure. Use IO handle operations when failures should be handled as values.

## Process

Import Process explicitly for process arguments and exit:

```fango
args : () ->{IO} List String
exit : Int ->{IO} ()
```

`args()` returns the program arguments after the source path and optional
`--` when launched with `fango run`. `exit status` terminates with that status.
In the interpreter it reports program exit without terminating the hosting
compiler or REPL. Process is not imported by Prelude.

## File

File acquires scoped `IO.Handle` values and supplies whole-file and directory
operations. Relative paths resolve from the running program's working directory.

```fango
import Fail exposing (Fail, attempt)
import File
import IO exposing (Error)
import Result exposing (Result(..))

countLines : IO.Handle -> Int ->{IO, Fail Error} Int
countLines file count =
    case IO.readLine file of
        Nothing -> count
        Just _ -> countLines file (count + 1)

main() =
    case attempt { File.withFile "input.txt" { file -> countLines file 0 } } of
        Ok count -> print count
        Err error -> print (IO.describeError error)
```

```fango
withFile : String -> (IO.Handle ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
withOutput : String -> (IO.Handle ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
withAppend : String -> (IO.Handle ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
read : String ->{IO} Result IO.Error String
writeAll : String -> String ->{IO} Result IO.Error ()
listDirectory : String ->{IO} Result IO.Error (List String)
isDirectory : String ->{IO} Result IO.Error Bool
size : String ->{IO} Result IO.Error Int
```

`withFile` opens a readable handle. `withOutput` creates or truncates a file
and opens a writable handle. `withAppend` opens a writable handle for append,
creating the file if needed. Each is a [cleanup scope](resources.md): it closes
the file exactly once on normal return or language abort. A failed open raises
before acquiring anything. A failed close after a successful body becomes the
scope's failure; after a failed body it is recorded alongside the primary failure.
Closing interrupts a blocked file read without waiting for its read lock.
Returning a handle or adapter does not extend its lifetime: subsequent operations
report a closed-resource error. Named callbacks may perform fewer effects than
the wrapper permits.

`read` and `writeAll` operate on a whole file without a handle and return
`Result`. Reads replace malformed UTF-8 with U+FFFD; writes create or replace
the file. `listDirectory` returns sorted entry names, `isDirectory` checks the
path's kind, and `size` returns bytes. Missing paths return `Err` with kind
`NotFound` for each.

## Net

`Net` supplies scoped TCP listeners and connections. `withListener port action`
binds the wildcard address, runs `action`, and closes the listener on every exit.
`accept listener action` waits for one connection and scopes it; `withClient host
port action` connects and scopes the client side.

```fango
withListener : Int -> (Net.Listener ->{IO, Fail Net.Error | e} a) ->{IO, Fail Net.Error | e} a
accept : Net.Listener -> (Net.Connection ->{IO, Fail Net.Error | e} a) ->{IO, Fail Net.Error | e} a
withClient : String -> Int -> (Net.Connection ->{IO, Fail Net.Error | e} a) ->{IO, Fail Net.Error | e} a
source : Net.Connection -> Bytes.Source {IO, Fail Net.Error}
sink : Net.Connection -> Bytes.Sink {IO, Fail Net.Error}
```

`source` blocks until bytes arrive or the peer reaches end of stream, then
answers at most 8192 bytes per pull. `sink` writes the complete supplied block.
Neither adapter closes the connection; the surrounding scope owns cleanup.

Libraries that manage a connection's lifetime themselves, such as the
[HTTP client](library-http-client.md), use the unscoped operations:

```fango
dialTimeout : String -> Int -> Int ->{IO} Result Net.Error Net.Connection
dialTls : String -> Int -> Int -> String ->{IO} Result Net.Error Net.Connection
readConnectionBytes : Net.Connection -> Int ->{IO} Result Net.Error Bytes
writeConnectionBytes : Net.Connection -> Bytes ->{IO} Result Net.Error ()
closeConnection : Net.Connection ->{IO} Result Net.Error ()
```

`dialTimeout host port millis` gives up after `millis` milliseconds; 0 leaves
the limit to the system. `dialTls host port millis roots` also completes a TLS
handshake within that time, sending `host` for SNI and verifying the
certificate for it against the system roots plus the PEM file at `roots`
(`""` for none). The connection then reads and writes plaintext like any other. `readConnectionBytes` answers up to the requested
count, and an empty `Bytes` at end of stream.

`Net.Error` is `{ kind : Net.Kind, address : String, message : String }`.
The portable kinds are `ConnectionRefused`, `ConnectionReset`, `AddressInUse`,
and `TimedOut`; other failures use `Other` and retain the system message.
`address` is the endpoint associated with the failed operation when one is
available. Listener and connection values are abstract resource wrappers over
`Runtime.Native.Any`; no native handle table or public release operation exists.

The runnable [echo server](../../examples/echo.fango) shows the adapters used
together with a bounded `Reader` and a flushing `Writer.over` loop. It listens
on port 8000 by default, or on the port passed as its sole argument:

```sh
fango run examples/echo.fango -- 8000
telnet 127.0.0.1 8000
```

Connection reads serialize access to the shared input buffer. Writes have a
separate lock covering the whole byte sequence. Closing a connection can
interrupt a blocked read or write and makes later operations fail. These
runtime guarantees support shared handles in [Async tasks](library-async.md).
Task cancellation does not automatically close the connection.

`acceptAsync` scopes an accepted connection like `accept` and closes the
listener when its waiting task is cancelled. `sourceAsync` and `sinkAsync`
expose the same byte interfaces while closing the connection when a blocked
operation's task is cancelled. `stopListener` closes a listener idempotently;
`listenerStopped` reports whether it has closed. `setReadDeadline` and
`setWriteDeadline` set socket deadlines in milliseconds from now; a
nonpositive value clears the deadline. These operations support the
[HTTP server](library-http.md#server).
