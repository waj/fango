# IO, files, and sockets

Console and process IO, structured IO.Error values, and scoped File APIs.

[Reference index](../reference.md). Sources: [IO](../../stdlib/IO.fango), [File](../../stdlib/File.fango).

## IO

`IO` exposes console IO, process arguments, files, and explicit process exit:

```fango
import IO

main() =
    IO.write "same line"
    print " then newline"
```

`IO.write : String ->{IO} ()` writes the string exactly as provided without a
trailing newline. It is a native operation; the prelude imports `IO`, so
`IO.write` is reachable without an import of your own, while reaching it
unqualified takes one. `print : Show a => a ->{IO} ()` and `readLine` are
unqualified already, from the prelude. `IO` also exposes these legacy
operations, kept for existing programs; new code reads and writes files
through the `File` module below, which reports failures as values:

```fango
args : () ->{IO} List String
readFile : String ->{IO} Maybe String
writeFile : String -> String ->{IO} ()
exit : Int ->{IO} ()
```

`args()` returns the program arguments after the source path (and optional
`--`) when launched with `fango run`, using the ordinary `List` type. Import
`List exposing (List(..))` to pattern-match its constructors unqualified.
Relative file paths are resolved from
the running program's current working directory. `readFile` returns `Nothing`
when the path does not exist and `Just contents` otherwise; malformed UTF-8
bytes in a file are replaced with U+FFFD. Other read errors fail the program.
`writeFile path contents` creates or replaces the file, and `exit status`
terminates with that status.

`IO` also exposes the nominal record
`type Line = { text : String, ending : String }`;
`readLine : () ->{IO} Maybe IO.Line`
returns `Nothing` at clean end of input and otherwise preserves the line
terminator separately as `"\n"`, `"\r\n"`, or `""` for an unterminated final
line. Malformed UTF-8 input sequences are replaced with U+FFFD. The pure
helpers `lineText : String -> String` and `lineEnding : String -> String`
split a raw line the same way, so other line sources can produce a `Line`.

Structured failures are values of `IO.Error`:

```fango
type Kind = NotFound | PermissionDenied | AlreadyExists | IsDirectory | NotDirectory | Other
    deriving (Eq, Show)

type Error = { kind : Kind, path : String, message : String } deriving (Eq, Show)

describeError : Error -> String
```

`kind` classifies what went wrong and `path` is the path the program supplied.
For `NotFound`, `PermissionDenied`, `AlreadyExists`, `IsDirectory`, and
`NotDirectory`, `message` is respectively `no such file or directory`,
`permission denied`, `file exists`, `is a directory`, or `not a directory`.
Those messages, and `describeError`'s output for them, are the same on every
platform. An `Other` failure instead preserves the underlying system message
for diagnostics; programs should use `kind`, rather than matching that text,
for portable behavior. The legacy `readFile` and `writeFile` above do not
produce these values; the `File` module does.

## File

`File` reads, writes, and lists files with structured failures, and treats an
open file as a scoped resource:

```fango
import Fail exposing (Fail, attempt)
import File
import IO exposing (Error)

countLines : File.Handle -> Int ->{IO, Fail Error} Int
countLines file count =
    case File.readLine file of
        Nothing -> count
        Just _ -> countLines file (count + 1)

main() =
    case attempt (\_ -> File.withFile "input.txt" (\file -> countLines file 0)) of
        Ok count -> print count
        Err error -> print (IO.describeError error)
```

Its public types are

```fango
withFile : String -> (File.Handle ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
withOutput : String -> (File.Handle ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
withAppend : String -> (File.Handle ->{IO, Fail IO.Error | e} a) ->{IO, Fail IO.Error | e} a
readLine : File.Handle ->{IO, Fail IO.Error} Maybe IO.Line
readBytes : File.Handle -> Int ->{IO, Fail IO.Error} Maybe Bytes
source : File.Handle -> Bytes.Source {IO, Fail IO.Error}
write : File.Handle -> String ->{IO, Fail IO.Error} ()
writeBytes : File.Handle -> Bytes ->{IO, Fail IO.Error} ()
sink : File.Handle -> Bytes.Sink {IO, Fail IO.Error}
read : String ->{IO} Result IO.Error String
writeAll : String -> String ->{IO} Result IO.Error ()
listDirectory : String ->{IO} Result IO.Error (List String)
isDirectory : String ->{IO} Result IO.Error Bool
```

`withFile path use` opens `path` for reading and runs `use` on the handle;
`withOutput` creates or truncates the file first, and `withAppend` opens it
for appending, creating it if needed. Each is a [cleanup scope](resources.md): the
file is closed exactly once when `use` finishes, whether it returned, failed,
or exited to an outer handler. A failed open raises `Fail IO.Error` before
anything is acquired; a failed close after a successful body is the scope's
failure, and after a failed body it is recorded alongside the body's failure.
`readLine` has the console `readLine`'s contract — `Nothing` at end of file,
otherwise the text and its exact terminator — and `write` writes a string as
given. `readBytes file count` answers `Nothing` at end of file and otherwise up
to `count` bytes, which may be fewer; a non-positive count answers an empty
`Bytes`, which is not end of file. It shares the handle's buffered reader with
`readLine`, so counted reads and line reads interleave on one handle, and it is
the only read that can carry a byte no `String` holds. `writeBytes` writes a
`Bytes` as given. All four raise `Fail IO.Error` on a system failure, so a body
that only reads and writes needs no `case` of its own; `attempt` around the
scope collects the failure.

`source` and `sink` adapt an open file to the leaves a
[buffered reader and writer](library-readers.md) are built over, so a file and
a memory buffer drive the same parsing code. Both capture the handle and are
therefore bound to its scope; neither closes anything, because closing belongs
to the scope. A pull answers whatever the file had, which may be short. `read` and `writeAll` handle a whole file without a
handle and answer a `Result` instead. `listDirectory` names a directory's
entries in sorted order, and `isDirectory` answers whether a path names one;
a missing path is an `Err` with kind `NotFound` for both.

`File.Handle` is abstract, with no accessible constructor, `Show`, or `Eq`.
It is available only inside a `with*` scope and obeys the ordinary
[resource escape and wrapper rules](resources.md#resource-escape-checks).
Named callbacks may perform fewer effects than the wrapper permits.

## Net

`Net` supplies scoped TCP listeners and connections. `withListener port use`
binds the wildcard address, runs `use`, and closes the listener on every exit.
`accept listener use` waits for one connection and scopes it; `withClient host
port use` connects and scopes the client side.

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

`Net.Error` is `{ kind : Net.Kind, address : String, message : String }`.
The portable kinds are `ConnectionRefused`, `ConnectionReset`, `AddressInUse`,
and `TimedOut`; other failures use `Other` and retain the system message.
`address` is the endpoint Go associates with the failed operation when one is
available. Listener and connection values are abstract resource wrappers over
`Runtime.Native.Any`; no native handle table or public release operation exists.

The runnable [echo server](../../examples/echo.fango) shows the adapters used
together with a bounded `Reader` and a flushing `Writer.over` loop. It listens
on port 8000 by default, or on the port passed as its sole argument:

```sh
fango run examples/echo.fango -- 8000
telnet 127.0.0.1 8000
```
