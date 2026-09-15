# fango language reference

This document describes functionality available in the current compiler. The
implementation architecture belongs in [the design](design.md), and unfinished
or speculative features belong in [the roadmap](roadmap.md).

## Setup and commands

The repository uses Go 1.26 and provides a Nix development environment:

```sh
nix develop
go build -o fango ./cmd/fango
```

Repository verification is split between correctness and performance:

```sh
make test       # correctness and interpreter/compiler differential tests
make test-perf  # compile-latency and runtime-ratio gates
make ci         # formatting, vet, and correctness gates
make clean      # repository build artifacts and Go test cache
```

`make test-perf` measures elapsed time, so a busy machine can fail it without
anything having regressed. It is deliberately excluded from `make ci`; run it
on an otherwise idle machine when you want the numbers. GitHub Actions runs
`make ci` on pushes to `master` and on pull requests
(`.github/workflows/ci.yml`).

`make clean` removes the repository-local `fango` executable, every `.fango/`
build directory beneath the checkout (including those created under examples
and test fixtures), and the Go test cache. It does not remove exported `.out`
projects or other ignored application data.

The CLI accepts one `.fango` source file:

```text
fango build [-o out] [--emit-go] main.fango
fango run main.fango [--] [args...]
fango check main.fango
fango fmt [-w] [-l] [file...]
fango repl [dir]
fango clean main.fango
```

`build` writes a native executable (defaulting to the source basename without
`.fango`). `run` builds if needed and runs the cached executable, forwarding
every argument after the source path to the program; an optional `--` is
removed first. `check` runs
through parsing, inference, elaboration, and Core validation without generating
Go. `clean` removes the source file's persistent `.fango/build` artifacts.
`repl` starts an interactive session whose source root is `dir`, or the
working directory; see [REPL](#repl).

`fmt` formats source. With no paths, or with `-`, it reads standard input and
writes to standard output; with paths it writes each formatted file to standard
output, `-w` rewrites the files in place, and `-l` lists the files that would
change and exits 1, which is how the repository gates its own sources. It
normalizes spacing, indentation, the `(op)` spelling of an operator name, and
runs of blank lines. A declaration is preserved exactly as written when it
holds a comment the formatter cannot anchor — one between an operator and its
operand, say, rather than above a statement or a branch — or when some part of
it has a line structure the printer cannot reproduce.

Redundant parentheses are dropped, because the syntax tree does not record
them. Grouping is not: an operator run is printed flat in the order it was
written, never regrouped, and a run that was parenthesized keeps its
parentheses. Literal spelling is preserved exactly — `1.50` and `1e3` are not
rewritten — as is the difference between `f()` and `f ()`.

The formatter keeps the author's line breaks rather than reflowing to a width,
so a construct written across several lines stays that way and one written
inline stays inline. That extends to where a keyword sits: a body moved below
its `=` or `->` stays below it, a `case` written on its declaration's own line
keeps its branches one level in from there, and a `then` or `else` given a line
of its own is anchored at the column of its `if`, so a chain of arms lines up
instead of staircasing rightward. An `exposing` list the author moved below
its keyword is printed in the leading-comma block form.

A bracket list or tuple written across lines uses that same leading-separator
style. Its separators and closing delimiter align with its opening delimiter;
elements remain grouped on the source lines the author chose. Nested lists and
tuples align independently, in expressions and patterns:

```fango
values =
    [ first, second
    , third
    ]

pair =
    ( first
    , second
    )
```

Import lines are sorted by module name and exposed names are sorted by kind —
types, effects and constructors first, then values, then operators — and
alphabetically within each kind. A comment written directly above an import
moves with it; one set off by a blank line stays at the top of the block. A
broken `exposing` list starts a line per kind and wraps to stay readable, which
is the one place the formatter consults a width: sorting has already discarded
the author's line structure there, so there is no break left to preserve. A
list written inline is left inline however long it is.
A comment above a statement, a branch, a handler clause or a declaration body
keeps its place, and one written at the end of a line stays at the end of that
line.

A file that does not lex or parse is left untouched and its diagnostics are
reported, because a failed declaration is dropped during recovery and formatting
would lose it.

The VS Code extension in `editors/vscode/` registers `fmt` as the formatter for
`.fango` files and enables format-on-save for them, both as ordinary settings
the user can override. It runs the executable named by `fango.path`, or one
built at the workspace root, or `fango` from `PATH`.

`build --emit-go` writes a complete Go project instead of an executable. For
`Main.fango`, its default destination is the `Main.out` directory in the
current working directory; `-o DIR` selects another directory. The project has
one `go.mod`, a root `main.go`, the shared `fangort` package, and one package per
imported Fango module beneath `modules/`. It can be compiled by running
`go build .` inside the directory without network access. Emission is quiet on
success. A missing, empty, or previously Fango-generated destination is
accepted; a non-empty unmanaged directory is rejected. Exported `.out`
directories are output artifacts and are not removed by `fango clean`.

## Modules, imports, and source layout

A program may consist of local modules. A named file begins with a module
header, followed by all imports, followed by declarations:

```fango
module Geometry.Shape exposing (Shape(..), area)

import Geometry.Point
import Geometry.Point as Point
import Geometry.Point exposing (Point, origin)
import Geometry.Point as P exposing (Point, origin)
```

Every import permits qualified access through the full module name and, when
present, its single-capitalized-name alias. An import `exposing` list also
introduces selected names unqualified; it does not remove qualified access.
Imports cannot be interspersed with declarations, and duplicate module imports
or qualifier aliases are rejected.

An exposing list is `(..)`, empty `()`, or a comma-separated list. A lowercase
item exports/imports a value or one effect operation. `Type`
or `Effect` exposes the abstract type/effect label; `Type(..)` also exposes all
constructors and `Effect(..)` all operations. `Class` exposes a class name;
`Class(..)` also exposes all its methods. Individual methods can be exposed as
lowercase values. Declaring an instance requires access to all class methods
(qualified access counts). Constructors cannot be selected
individually, member lists cannot be partial, and imported declarations cannot
be re-exported. Qualified names are accepted for values, operations,
constructors, patterns, types, effect rows, and handler clauses.

The entry file's directory is the source root. A non-bundled `Foo.Bar` resolves
exactly to `Foo/Bar.fango` beneath it. Imported files require a header whose
module name and casing match that path. A named entry must match its top-level
filename, so `Main.fango` declares `Main`. Headerless entry files remain
compatible, receive a private synthetic identity, and cannot themselves be
imported.

The compiler also contains standard-library modules.
Their names are reserved: a named entry or local module that has the same name
is rejected with `RESERVED MODULE`, rather than replacing the bundled module.
One of them, `Prelude`, declares the default scope. It holds nothing but
imports, and every other module resolves as though they stood at the top of
its own file:

```fango
import Basics exposing
    ( Num, Eq, Ord, Show, show
    , (+), (-), (*), (/), (==), (/=), (<), (>), (<=), (>=), (++)
    )
import IO exposing (IO, print, readLine)
import List exposing (List)
import Maybe exposing (Maybe(..))
```

These are ordinary imports, so besides the unqualified names they also grant
qualified access: `IO.write` and `Basics.modBy` need no import line of their
own. Everything the exposing lists leave out does — the named methods
`fromInt` and `negate` among them, and every other standard-library module.
Importing a module the prelude already names is not a duplicate import; it
simply adds the names its own exposing list selects. Aliasing another module
to a qualifier the prelude holds is a `DUPLICATE IMPORT ALIAS`, so `import
Helper as IO` is rejected.

Because `Maybe`, `Nothing`, `Just` and `List` are in scope everywhere, a
module cannot declare its own: doing so is an `UNQUALIFIED COLLISION`, since
fango rejects shadowing rather than resolving it. Pick another name, or opt
out with the pragma below.

A module opts out with the `{-# no-prelude #-}` pragma above its header,
after which the only names in scope are its own declarations and whatever its
own imports bring in:

```fango
{-# no-prelude #-}
module Bare exposing (double)

import Basics exposing ((+))

double x = x + x
```

A pragma is `{-#`, a directive name, and `#-}`. It describes the whole file,
so it belongs above the `module` header; one appearing later is a `MISPLACED
PRAGMA`, and an unrecognized directive is an `UNKNOWN PRAGMA`. `no-prelude` is
currently the only one. The bundled standard library sits below the prelude
and carries the pragma, which is why its modules import `Basics` explicitly.

Bracket list syntax, tuple syntax, `deriving`, and the staging forms are
implicit in a different way. A module that uses one automatically depends on
the bundled module the syntax desugars into — `List`, `Tuple`, `Derive`, or
`Meta` — but the dependency exposes nothing. `Nil`, `Cons`, `Pair`, `Triple`
and `Code` still follow the ordinary import rules; `List` and `Maybe` are in
scope because the prelude imports them, not because the syntax does.

`build` and `run` use only the entry module's `main`; a dependency's `main` is
an ordinary declaration. `check` does not require `main`. Imports expose only
the direct module's declared public interface, never its dependencies. Import
cycles are rejected with the complete cycle chain. The generated build
directory compiles each Fango module as a separate Go package within one
private Go module, allowing unchanged packages to use Go's build cache. It
includes `sources.json`, containing each transitive Fango source and native
sidecar's logical name, path, and SHA-256 hash for build invalidation. Local
paths are relative to the source root; bundled paths begin with `<stdlib>/`.

Top-level declarations begin in column 1 and are visible only to declarations
below them within their module. Tabs are rejected; indent with spaces. `--`
starts a line comment, and `{- ... -}` comments may nest. `{-#` opens a
pragma rather than a comment, so a block comment whose first character is `#`
must be written `{- #`.

Lowercase names identify values, parameters, type variables, operations, and
effect-row tails. Uppercase names identify types, effects, and constructors.
Values cannot be shadowed, including by parameters, patterns, or local
bindings. Type names and constructor names are separate namespaces.

Indented declaration bodies are blocks. Statements align with the first item,
and a block ends in exactly one result expression:

```fango
hypotenuse =
    x = 3.0
    y = 4.0
    x * x + y * y
```

Bindings are eager and sequential. Unit-valued expression statements may be
placed before the final result, which is how effectful work is sequenced.
There is no `let ... in` expression.

An `if` anchors its `then` and `else` at the column of its own `if`. Both may
align with it, or lead their own line further right, and an `else if` on the
`else`'s line continues the same chain, so every arm aligns with the first
`if`:

```fango
classify byte =
    if byte == 32 then
        "space"
    else if byte == 9 then
        "tab"
    else
        "other"
```

Either branch may be an indented block, including under a keyword-led line:

```fango
describe verbose byte =
    if verbose
      then
        label = classify byte
        print label
        label
      else
        classify byte
```

Because neither keyword can begin a statement, a branch block ends at the
following `then` or `else` even when the block sits at that keyword's column.

## Bundled standard library

The standard library ships with the compiler, has no separately selected
version, and is experimental: its API may evolve before a future stability
milestone. `Prelude` declares what is in scope without an import — the
"Modules, imports, and source layout" section above lists it, and it covers
the `Maybe` type with its constructors and the `List` type name; every other
module and API must be imported explicitly.

`Prelude` is the one bundled module with no API. It exposes nothing, may
contain only imports, and exists so that the default scope is written in fango
rather than fixed in the compiler. Importing it does nothing; editing it
changes what every module sees.

`List` exposes the following algebraic type:

```fango
module List exposing (List(..), range, each, foldl, foldr, map, filter, length, reverse)

type List a = Nil | Cons a (List a) deriving (Eq, Ord)
```

Its public function types are
`range : (Num a, Ord a) => a -> a -> List a`,
`each : (a ->{e} ()) -> List a ->{e} ()`,
`foldl : (a -> b ->{e} b) -> b -> List a ->{e} b`,
`foldr : (a -> b ->{e} b) -> b -> List a ->{e} b`,
`map : (a ->{e} b) -> List a ->{e} List b`,
`filter : (a ->{e} Bool) -> List a ->{e} List a`,
`length : List a -> Int`, and
`reverse : List a -> List a`.

Its handwritten `Show a => Show (List a)` instance displays lists with bracket
syntax, using each element's `Show` instance: `[]`, `[1]`, and `[1, 2, 3]`.

The prelude imports the `List` type name, so an annotation may say `List a`
with no import. `Nil`, `Cons`, and the functions above still need one.

`range start end` produces ascending values by adding one, including `end`
when that value is reached, and returns `Nil` immediately when `start > end`.
It works at both `Int` and `Float`; callers must use finite bounds because the
ordinary recursive implementation is not guaranteed to terminate for `NaN`
or positive infinity. `each action values` applies `action` from left to right
and propagates its effects. `foldl combine initial values` visits values from
left to right, passing the current element first and the accumulator second to
`combine`; callback effects are propagated. `foldr` passes the same arguments
in the same order but visits values from right to left, so its combining
function receives the result of folding the rest of the list. `map fn values`
applies `fn` to every element, preserving order and length. `filter keep
values` retains the elements for which `keep` answers `True`, preserving their
order. Both call their callback once per element, from left to right.
`length` counts elements and `reverse` returns the same elements in the
opposite order.

A `List` is a linked list: `Cons`, the head, and the tail are each constant
time, and a tail is shared rather than copied. It is stored as a spine of
fixed-size arrays rather than one cell per element, so building a list
allocates far less than its length would suggest and traversing one is
contiguous. That representation is not observable — values are immutable, so
sharing has no effect a program can detect — but it is what the complexity
above rests on. `length` is a traversal, not a stored count.

`map`, `filter`, and `foldr` build an intermediate list and reverse it, so they
run in constant stack rather than one frame per element, at the cost of
allocating each result twice. Callback order is unaffected: `map` and `filter`
call theirs from left to right, `foldr` from right to left.

`Generator` and `Iterator` provide scoped, pull-driven traversal:

```fango
import Generator
import Iterator

main() =
    Generator.withIterator (\_ ->
        Generator.yield 10
        Generator.yield 20
        Generator.yield 30) (\iterator ->
        Iterator.forEach print iterator)
```

`Generator.yield : a ->{Generator a} ()` suspends the producer and offers one
value to its owning iterator scope. `Generator.withIterator` takes the
producer and a consumer:

```fango
withIterator
    : (() ->{Generator a | e} ())
    -> (Iterator a ->{e} result)
    ->{e} result
```

`Iterator a` is an opaque resource. Consumers may be named functions; aliases,
helper calls, and temporary ADTs or closures may use the cursor within its
owner. Returning the cursor, returning a closure or ADT that retains it, or
storing it in an outer handler reports `RESOURCE ESCAPES`. Unrelated closures
may be returned. A producer cannot reenter an advancement of the same cursor;
overlapping or possibly overlapping access reports `ITERATOR ADVANCEMENT CONFLICT`.
There is no public `next` yet.

Sequential terminal calls are supported. Once a cursor is exhausted, `forEach`
does nothing and `fold` returns its initial accumulator. Distinct cursors in
nested scopes remain independent. These rules follow inferred contracts across
module boundaries, including callbacks stored in records or dictionaries.
An ordinary handler cannot intercept `Generator.yield`: attempting to handle
its compiler-owned effect reports `COMPILER-OWNED EFFECT`.

`Iterator.forEach : (a ->{e} ()) -> Iterator a ->{e} ()` invokes its callback
once per yield in production order. `Iterator.fold` has type
`(a -> b ->{e} b) -> b -> Iterator a ->{e} b`; it threads an accumulator in
the same order, passing the element first and accumulator second like
`List.foldl`. Normal producer return ends traversal. If the consumer returns
early or an effect exits the scope, unfinished production is abandoned and its
pending cleanup scopes run before control continues. The iterator and generator
modules must be imported explicitly.

A producer starts only on the first pull; a consumer that ignores its cursor
starts no production. A pure traversal can run in a splice, including repeated
traversals of the same producer. Compile-time native restrictions and the
evaluation-step budget apply throughout production and consumption, including
producer loops that never yield. Failed expansions retain the usual REPL
rollback behavior.

`Range.each : (Num a, Ord a) => (a ->{e} ()) -> a -> a ->{e} ()` traverses an
inclusive ascending numeric range without constructing a `List`. For example,
`Range.each drawPoint 0 78` calls `drawPoint` with every value from `0` through
`78`. It does nothing when the start is greater than the end, works with both
`Int` and `Float`, and has the same finite-bound requirement as `List.range`.
The callback runs in ascending order and its effects are propagated.

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
write : File.Handle -> String ->{IO, Fail IO.Error} ()
read : String ->{IO} Result IO.Error String
writeAll : String -> String ->{IO} Result IO.Error ()
listDirectory : String ->{IO} Result IO.Error (List String)
isDirectory : String ->{IO} Result IO.Error Bool
```

`withFile path use` opens `path` for reading and runs `use` on the handle;
`withOutput` creates or truncates the file first, and `withAppend` opens it
for appending, creating it if needed. Each is a cleanup scope (see below): the
file is closed exactly once when `use` finishes, whether it returned, failed,
or exited to an outer handler. A failed open raises `Fail IO.Error` before
anything is acquired; a failed close after a successful body is the scope's
failure, and after a failed body it is recorded alongside the body's failure.
`readLine` has the console `readLine`'s contract — `Nothing` at end of file,
otherwise the text and its exact terminator — and `write` writes a string as
given. Both raise `Fail IO.Error` on a system failure, so a body that only
reads and writes needs no `case` of its own; `attempt` around the scope
collects the failure. `read` and `writeAll` handle a whole file without a
handle and answer a `Result` instead. `listDirectory` names a directory's
entries in sorted order, and `isDirectory` answers whether a path names one;
a missing path is an `Err` with kind `NotFound` for both.

`File.Handle` is abstract: it has no constructor, no `Show`, and no `Eq`, and
it can be obtained only inside a `with*` scope. The compiler treats it as a
capability, so a body may not return the handle, a closure over it, or data
containing it (`RESOURCE ESCAPES`). An outer handler may not keep it in its
state, even when the surrounding computation returns Unit. Generic wrappers
over `withFile` infer and export the same lifetime obligations. Functions and
function-bearing data may be returned when their contracts prove independence
from the file. Named callbacks use ordinary effect inclusion, so they may
perform fewer effects than the wrapper permits.

`Basics` also declares three integer functions the prelude leaves out, so
reaching them unqualified takes an import of your own:

```fango
import Basics exposing (modBy, quotientBy, remainderBy)
```

`modBy : Int -> Int -> Int` is the floored modulus: `modBy modulus x` has the
modulus's sign, so `modBy 3 (-4)` is `2` and `modBy (-3) 4` is `-2`.
`remainderBy : Int -> Int -> Int` is the truncated remainder:
`remainderBy divisor x` has the dividend's sign, so `remainderBy 3 (-4)` is
`-1`. `quotientBy : Int -> Int -> Int` is the matching truncated division:
the quotient rounds toward zero, so `quotientBy 3 (-7)` is `-2`, and
`quotientBy d x * d + remainderBy d x` recovers `x` for every `d` and `x`.
There is no floored division to pair with `modBy` yet. A zero modulus or
divisor crashes the program in both backends.

`Maybe` exposes the optional-value type:

```fango
module Maybe exposing (Maybe(..), withDefault)

type Maybe a = Nothing | Just a deriving (Eq, Ord, Show)
```

`withDefault : a -> Maybe a -> a` returns the contained value or the
fallback. The prelude imports `Maybe(..)`, so the type and both constructors
need no import; `withDefault` does.

`Tuple` exposes the types behind tuple syntax:

```fango
module Tuple exposing (Pair(..), Triple(..), first, second, swap)

type Pair a b = Pair a b deriving (Eq, Ord)

type Triple a b c = Triple a b c deriving (Eq, Ord)
```

`(a, b)` and `(a, b, c)` are surface syntax for these, so a file that uses
tuples needs no import; naming `Tuple`, `Pair`, or `Triple` still does. Both
types have handwritten `Show` instances that display a tuple the way it is
written, `(1, one)`, rather than the derived structural form `Pair 1 one`,
and the type printer spells them `(Int, String)` for the same reason.
Derived `Eq` and `Ord` compare elements left to right, so `Ord` on a tuple
needs `Ord` on every element.

Its public function types are `first : Pair a b -> a`,
`second : Pair a b -> b`, and `swap : Pair a b -> Pair b a`. There is no
`Triple` accessor set and no `mapFirst`/`mapSecond`; pattern matching covers
both, and the roadmap adds library functions when an example needs them.

`Dict` exposes an ordered dictionary keyed by any `Ord` type:

```fango
module Dict exposing
    (Dict, empty, foldl, foldr, fromList, get, insert, isEmpty, keys, map,
     member, remove, singleton, size, toList, update, values)
```

`Dict` is exposed without its constructors, so the type is abstract: the
balance invariant belongs to the module. Its public types are
`empty : Dict k v`, `singleton : k -> v -> Dict k v`,
`size : Dict k v -> Int`, `isEmpty : Dict k v -> Bool`,
`get : Ord k => k -> Dict k v -> Maybe v`,
`member : Ord k => k -> Dict k v -> Bool`,
`insert : Ord k => k -> v -> Dict k v -> Dict k v`,
`remove : Ord k => k -> Dict k v -> Dict k v`,
`update : Ord k => k -> (Maybe v ->{e} Maybe v) -> Dict k v ->{e} Dict k v`,
`keys : Dict k v -> List k`, `values : Dict k v -> List v`,
`toList : Dict k v -> List (k, v)`,
`fromList : Ord k => List (k, v) -> Dict k v`,
`map : ((k, a) ->{e} b) -> Dict k a ->{e} Dict k b`, and
`foldl` and `foldr : ((k, v) -> b ->{e} b) -> b -> Dict k v ->{e} b`.

Only the operations that navigate by key carry `Ord k`. `empty`, `singleton`,
`size`, `isEmpty`, `map`, and the traversals do not, because none of them
compares a key.

`keys`, `values`, `toList`, `foldl`, and `map` visit in ascending key order
and `foldr` in descending order. Callback effects are performed in that order,
so the order is part of the contract rather than an accident. Callbacks take
one `(k, v)` pair rather than a curried key and value, which also matches
`List.foldl`'s arity. `update`'s callback runs exactly once per call, at the
end of the search path; returning `Nothing` removes a present key and leaves
an absent one absent.

When a key is already present the stored key is kept and only the value
changes. That one rule covers `insert`, `update`, and `fromList` alike, and
it is observable only through an `Ord` instance that calls distinguishable
keys equivalent. `fromList`'s later entries win the value.

`show` displays `Dict [a = 1, b = 2]`, and the empty dictionary as `Dict []`.
Equality compares entries in key order rather than tree shape, so two
dictionaries built by inserting the same entries in different orders are
equal even though their trees differ.

The representation is a weight-balanced search tree caching each subtree's
size. `size` and `isEmpty` are constant time; `get`, `member`, `insert`,
`remove`, and `update` are logarithmic; `keys`, `values`, `toList`, the folds,
and `map` are linear; `fromList` is `n log n`. Nodes are shared rather than
copied, so an update rewrites only its search path.

`Json` exposes a derivable encoding class:

```fango
module Json exposing (Encode(..), StringToken(..), parseString)

class Encode a
    encode : a -> String
```

`Encode` has bundled instances for `Int`, `Float`, `String`, `Char`, `Bool`,
`()`, `List a`, and `Maybe a`. `deriving (Encode)` supports records and union
types. Records become JSON objects whose keys follow field declaration order.
Ordinary unions use the uniform representation
`{"$tag":"Constructor","$fields":[...]}`. Lists are arrays; `Nothing` and
Unit are `null`; `Just value` uses the value's representation. Output is
compact and deterministic. Encoding a non-finite `Float` fails because JSON
has no representation for it.

There is intentionally no `Decode` class yet. `parseString : String -> Maybe
Json.StringToken` is a small aid for hand-written decoders: it consumes one
leading JSON string, applies JSON escape rules, and returns its decoded value
and the unconsumed suffix.

`Meta` exposes the compile-time stage's representations: the abstract `Code`
type, the reflected `TypeRepr` and its schema records, the `Lift` class, and
the traversal a deriver walks. `Code` has no constructors: the only way to
build one is a `quote` or `Meta`'s own builders. See
[Compile-time metaprogramming](#compile-time-metaprogramming). A file that
uses `quote`, `$(…)`, or `typeOf` gets `Meta` in its module graph
automatically, but naming `Code` in an annotation requires the ordinary
`import Meta exposing (Code)`.

`Derive` supplies the derivers for `Eq`, `Ord`, and `Show`. It exposes nothing
a program calls: a file that writes `deriving` gets it in the module graph
automatically, and `deriving` is the only way to reach it.

Strings are always valid UTF-8 sequences and are not normalized. `Char` is one
Unicode scalar value. `String` exposes the nominal record
`type Uncons = { first : Char, rest : String }` and these operations:

```fango
length : String -> Int
byteLength : String -> Int
slice : Int -> Int -> String -> String
startsWith : String -> String -> Bool
contains : String -> String -> Bool
uncons : String -> Maybe String.Uncons
fromChar : Char -> String
split : String -> String -> List String
trim : String -> String
padLeft : Int -> Char -> String -> String
padRight : Int -> Char -> String -> String
```

`length` counts Unicode scalars and `byteLength` counts UTF-8 bytes. `slice`
uses clamped half-open scalar indices and returns `""` when its end is not
greater than its start. `startsWith prefix text` tests an exact prefix and
`contains needle text` tests for an occurrence anywhere, with the empty needle
found in every string; `uncons` returns the first scalar and remaining string, or `Nothing` for the
empty string. `fromChar` makes the corresponding one-scalar string.
`String.words : String -> List String` splits on ASCII space, tab, LF, CR,
vertical tab, and form feed, and
`toInt : String -> Maybe Int`, which
parses an optional `+`/`-` sign followed by base-10 digits. An empty digit
sequence, any other character, and values outside the signed 64-bit range all
produce `Nothing`; `String.toInt "007"` is `Just 7` and
`String.toInt "-9223372036854775808"` parses the most negative Int.

`split separator text` cuts at every occurrence, so n occurrences give n + 1
pieces and adjacent separators give empty ones: `String.split "," "a,,b"` is
`["a", "", "b"]` and `String.split "," ""` is `[""]`. An empty separator
yields the text unchanged as a single piece. `trim` removes leading and
trailing bytes from the same ASCII whitespace set `words` splits on, so an
all-whitespace string trims to `""`. `padLeft` and `padRight` measure width
in Unicode scalars, like `length`, and return a string that is already that
wide unchanged; a zero or negative width never truncates.

`Result` exposes a conventional success-or-error value and basic transforms:

```fango
module Result exposing (Result(..), map, mapError, andThen, withDefault)

type Result error value = Err error | Ok value deriving (Eq, Ord, Show)
```

`map` transforms an `Ok`, `mapError` transforms an `Err`, `andThen` chains a
successful computation, and `withDefault` extracts a success or returns its
fallback.

`Fail` is the conventional abort-only failure effect, so programs no longer
declare their own:

```fango
module Fail exposing (Fail, attempt, fail, fromResult)

effect Fail error
    abort fail : error -> value

attempt : (() ->{Fail error | e} value) ->{e} Result error value
fromResult : Result error value ->{Fail error} value
```

`fail error` never returns: it unwinds to the nearest enclosing `attempt`,
which answers `Err error`; a normal completion answers `Ok value`. `fromResult`
unwraps an `Ok` and raises an `Err`, so a `Result`-returning call can join a
failing computation with `fromResult (File.read path)`. Nested attempts handle
only the failures raised inside them, and an abort raised by an outer `attempt`'s
clause propagates outward as usual. The effect is not in the prelude: import
`Fail exposing (Fail, attempt, fail)` to use it, and a module that declares its
own `Fail` effect is unaffected.

`State` provides a parameterized state effect and its standard runner:

```fango
module State exposing (State(..), StateResult(..), run)

effect State s
    get : () -> s
    put : s -> ()

type StateResult s a = { value : a, state : s }

run : s -> (() ->{State s | e} a) ->{e} StateResult s a
```

`State.run initial action` evaluates `initial` once, runs `action` with a
private cell, and returns both the action value and final state. `get()` reads
the current state and `put next` replaces it. Cells belong to handler
activations, so nested runs—including two runs of the same `State s`—are
independent.

`Writer` packages the same mechanism as an immutable accumulator:

```fango
effect Writer w
    tell : w -> ()

type WriterResult w a = { value : a, output : w }

run : (w -> w -> w) -> w
    -> (() ->{Writer w | e} a) ->{e} WriterResult w a
```

`Writer.run combine empty action` updates the accumulator with
`combine output item` for each `tell item`, preserving source order.

`Scope` provides cleanup that survives every exit from a region, which a
handler for one named failure cannot:

```fango
module Scope exposing (bracket, finally)

bracket : (() ->{e} resource) -> (resource ->{e} ())
       -> (resource ->{e} result) ->{e} result

finally : (() ->{e} result) -> (() ->{e} ()) ->{e} result
```

`bracket acquire release use` acquires once, runs `use` on the resource, and
releases. `finally action cleanup` is the same shape without a resource. Both
are described under [cleanup scopes](#cleanup-scopes).

`Random` declares a randomness effect and two ready-made handlers:

```fango
module Random exposing (Random(..), int, runSeeded, runSystem)

effect Random
    int : Int -> Int -> Int
```

`int lo hi` requests a draw in `[lo, hi]` (reversed bounds are swapped). It
has no default handler; a program chooses an interpretation by wrapping the
effectful computation in one of

```fango
runSeeded : Int -> (() ->{Random | e} a) ->{e} a
runSystem : (() ->{Random | e} a) ->{e} a
```

`runSeeded seed action` answers every draw from a deterministic 31-bit
linear congruential generator (glibc constants; modulo-biased and not
cryptographic) started at `seed`: the same seed always yields the same
draws, in both the interpreter and compiled programs. `runSystem action`
seeds the same generator from system entropy, so every run draws a fresh
sequence. `examples/guess.fango` performs `Random.int` opaquely and picks
the interpretation with one line in `main`.

The seed is handler-local state. Nested seeded or system runs do not disturb
an outer sequence, and independent runs share no PRNG cell. Deterministic
`runSeeded` computations are allowed during compile-time staging; `runSystem`
is rejected with `COMPILE-TIME NATIVE` because entropy is observable.

## Native Go sidecars

A module may implement an annotated value or effect operation in adjacent Go:

```fango
module Hash exposing (crc32)

crc32 : String -> Int
crc32 = native
```

`Hash.native.go` must declare `package native` and export the corresponding
capitalized function. Imported dotted modules follow their source layout
(`Foo/Bar.fango` and `Foo/Bar.native.go`); a headerless entry uses its source
basename.

```go
package native

import "hash/crc32"

func Crc32(text string) int64 {
    return int64(crc32.ChecksumIEEE([]byte(text)))
}
```

The supported boundary types are `Int`/`int64`, `Float`/`float64`,
`String`/`string`, `Char`/`rune`, `Bool`/`bool`, and Unit. String and Char
results are validated, and an invalid UTF-8 string or non-scalar rune panics at
the native boundary. Unit parameters are omitted from
the Go function and a Unit result is represented by no Go result.

One kind of declared type also crosses: a type the same module declares with
exactly one constructor holding exactly one boundary scalar, such as
`type Token = Token Int`, may appear as a parameter or result. The Go function
sees the scalar (`int64` here); the compiler projects the field on the way in
and rebuilds the constructor on the way out, in both backends. Keep the
constructor out of the module's exposing list and derive no `Show` or `Eq`,
and callers hold an opaque handle they can neither forge nor inspect — the
bundled `File.Handle` is exactly this, with `{-# resource #-}` adding its scoped
capability contract. Anything else — functions, other ADTs,
records, polymorphic variables, class constraints, Go type parameters, and
multiple results — is a `NATIVE ABI` error. A Go `error` result is likewise
rejected in user sidecars (`FALLIBLE NATIVE NOT ALLOWED`); only the bundled
`File` module's natives return one, which the compiler turns into
`Result IO.Error a`. Effect rows on native value types
are preserved for checking and may contain `IO` or user-declared effects; the
sidecar call itself uses the same scalar ABI and does not receive a hidden
evidence argument. Sidecars may import only Go standard-library packages.
Every call-form declaration needs its matching exported function, and every
exported sidecar function needs a declaration.

An operation in an `effect` declaration may also use call form:

```fango
effect Clock
    tick : () -> Int = native
```

Its Go function follows the same scalar and Unit-erased ABI. A Fango handler
takes precedence; the native function supplies an otherwise unhandled native
operation.

Every materialized sidecar package receives the reserved process-global
`FangoHost`. Its `HasInput`, `ReadInputLine`, `WriteOutput`, `Arguments`,
`WorkingDirectory`, and `Exit` methods expose the surrounding Fango process.
Compiled programs install the system host; the interpreter worker installs a
proxy to the active interpreter session. Native function signatures never gain
a hidden context argument. Sidecars may use `FangoHost` only during a native
call and must not replace it or retain it for asynchronous work.

Native sidecars participate in `check`, build manifests, incremental rebuilds,
`build`, `run`, and `--emit-go`. Bundled standard-library modules use the same
sidecar form, host, and ABI; their sidecars are materialized into generated
projects just like user sidecars. During ordinary interpreter and REPL
evaluation, call-form sidecars run in a cached persistent worker process.
Package globals persist for the session. Panics are reported across the worker
boundary and reproduced as native panics; `FangoHost.Exit` becomes a
program-exit error instead of terminating the REPL. Native sidecars are trusted
code and are not sandboxed.

A module imported at the REPL prompt brings its sidecar along: the session
rebuilds its worker over the bundled sidecars and every user sidecar imported
so far, and the worker builds and starts the first time one of its functions
is called. Package globals persist across calls but not across such a rebuild.

The word `native` is reserved. Inline `native "Go expression"` templates are
compiler-bundled syntax and are rejected in user modules. The standard library
keeps templates for inlined scalar primitives and compiler-only metaprogramming
representations. `IO` uses its ordinary sidecar.

## Values and operators

Built-in value types are `Int`, `Float`, `String`, `Char`, `Bool`, and Unit `()`.
Integers are signed 64-bit decimal literals. Floats include `1.25`, `1e3`, and
`1.0e-2`; `.5` and `1.` are not float literals. Strings are single-line,
double-quoted values with `\\`, `\"`, `\n`, `\t`, and `\r` escapes. A Char
literal contains exactly one Unicode scalar between single quotes and accepts
`\\`, `\'`, `\n`, `\t`, and `\r` escapes; examples are `'x'`, `'二'`, and
`'\n'`. Booleans are the constructors `True` and `False`.

An operator is an ordinary value whose name is punctuation, so the operators
below are declarations in `Basics` rather than built-in syntax. These are the
fixities it declares, from tighter to looser:

| Operators | Meaning | Fixity |
| --- | --- | --- |
| unary `-` | numeric negation | prefix |
| `*`, `/` | multiplication, floating division | `infixl 7` |
| `+`, `-` | addition, subtraction | `infixl 6` |
| `++` | string concatenation | `infixr 5` |
| `==`, `/=`, `<`, `>`, `<=`, `>=` | comparison | `infix 4` |
| `&&` | logical and, short-circuiting | `infixr 3` |
| `\|\|` | logical or, short-circuiting | `infixr 2` |
| `\|>` | pass the left value to the right function | `infixl 0` |
| `<\|` | apply the left function to the right value | `infixr 0` |

`+`, `-`, `*`, and unary negation require `Num`; equality requires `Eq`, and
ordering requires `Ord`. These classes have standard scalar instances and can
be implemented for custom types. `/` accepts only `Float`, `++` only `String`,
and there is no implicit conversion of an existing `Int` value to `Float`.
Integer literals use `fromInt` and can therefore inhabit any type with a `Num`
instance; decimal literals always have type `Float`. Chained comparisons
require parentheses.

Unary minus is prefix syntax rather than an operator name: it binds tighter
than every operator and looser than application, always, and desugars to the
`Num` method `negate`. There is no `(-)` prefix section.

`&&` and `||` take `Bool` operands and produce a `Bool`. They short-circuit:
the right operand is not evaluated when the left one already decides the
result, so its effects do not happen either. They are the one exception to
"an operator is a value": they are fixed syntax with fixed fixity, and there
is no `(&&)` function to pass, bind, or redeclare, because a called value
would have to evaluate both operands.

### Declaring operators

An operator is named by its spelling in parentheses, and that spelling names
a value wherever an identifier can — a top-level definition and its
annotation, a class signature, an instance or deriver method, an `exposing`
list, and an expression:

```fango
(<+>) : Int -> Int -> Int
(<+>) a b = a * 10 + b

sum = List.foldl (<+>) 0 items
```

An operator name is a run of one or more of these characters:

```
! # % & * + - / : < = > ? @ ^ | ~
```

The runs `=`, `->`, `=>`, `:`, `|`, and `^` are reserved by the grammar and
cannot be declared. Four plausible characters are deliberately excluded: `.`
is field access, module qualification, and `..`; `$` belongs to the splice
opener `$(`; `\` is the lambda; and `,` `(` `)` `{` `}` are punctuation. So
`(.)`, `($)`, and `(<$>)` are unavailable, while `(<+>)`, `(|>)`, `(>>=)`,
and `(:::)` are all ordinary names.

Operator characters group greedily: the longest run is one operator. So
`a<-b` is the operator `<-` rather than `a < -b`, and `x =-1` is the operator
`=-` rather than an assignment — put spaces around operators. A run may not
begin with `--`, which starts a line comment, so `-->` is a comment while
`<--` is an operator.

A fixity declaration gives an operator its precedence, 0 through 9, and its
associativity. It may sit above or below the operator's own declaration, but
must live in the module that declares it:

```fango
infixl 6 (<+>)
infixr 5 (++)
infix  4 (==)
```

An operator with no fixity declaration is `infixl 9`: the tightest level,
still looser than application. Fixity belongs to the spelling rather than to
any one definition, because a class declares an operator and its instances
implement it; it is therefore shared across a whole program, and two modules
declaring the same operator's fixity differently is an error. Operators that
share a precedence must share an associativity to be mixed without
parentheses.

Operators are declared at the top level, in a class, or in an instance —
never in a function body, where a fixity would have no home.

`if condition then a else b` is an expression. Its condition is `Bool` and both
branches have the same type. `then` and `else` may align with their own `if`
or lead their own line, and each branch may be an indented block; see
[the layout rules](#modules-imports-and-source-layout).

## Declarations, annotations, and functions

Definitions and optional annotations are separate, adjacent declarations:

```fango
square : Float -> Float
square x = x * x

main = square 3.0
```

Arrows associate to the right. Type application uses spaces, such as
`Maybe Int`. User type constructors must always be fully applied.

Functions are curried and application uses whitespace: `f x y`. An attached
empty `()` is a postfix Unit call and binds tighter than whitespace
application: `print foo()` means `print (foo ())`. This applies repeatedly and
after parentheses, as in `foo()()` and `(factory x)()`. Spaced `foo ()` remains
ordinary whitespace application, so `print foo ()` means `(print foo) ()`.
Only empty attached parentheses have this precedence; `print foo(1)` retains
the ordinary whitespace-application grouping `(print foo) 1`. Whitespace or a
comment before `()` makes it an ordinary application.

Partial application and functions as values are supported. Lambdas use
`\x y -> expression`. A final lambda argument may omit parentheses:

```fango
Scope.bracket acquire release \resource ->
    use resource
```

The lambda body extends rightward, including operators, until its enclosing
layout boundary or delimiter. A list comma ends the current element, so
`[test "one" \_ -> checkOne(), test "two" \_ -> checkTwo()]` contains two
calls. An indented lambda body may contain bindings and Unit statements.
Parenthesized lambdas remain valid; a lambda always needs at least one pattern.

`value |> function` and `function <| value` are ordinary strict calls to
operators declared in `Basics` and exposed by `Prelude`. They perform the
callback's effects on the final application. For example,
`values |> List.map double |> List.foldl (+) 0` chains leftward;
`print <| 1 + 2` applies `print` to the sum. Mixing `|>` and `<|` without
parentheses is an associativity conflict. In `apply \x -> x |> finish`, the
pipe belongs to the lambda body.

Function and lambda arguments are patterns. Constructor applications must be
parenthesized in an argument position (`map f (Cons x xs)`), while record and
list patterns delimit themselves. `_` discards an argument. A lambda has one
pattern row, so that row must be exhaustive.

Adjacent definitions with the same name and arity form source-ordered
equations for one function:

```fango
withDefault fallback Nothing = fallback
withDefault _ (Just value) = value
```

Blank lines and comments do not split a group; another declaration does. One
annotation immediately above the first equation applies to the group, so a row
carrying its own annotation starts a new definition instead of joining the one
above it. Rows in a group must agree on how many arguments they take, and the
group must be exhaustive and non-redundant. Top-level, local, operator,
instance-method, and deriver-method equations use the same rule. Only
definitions with arguments group: a repeated zero-argument value remains a
duplicate definition. Functions may have indented block bodies, and local
function bindings are supported.

A Unit function is canonically called as `f()`; `f ()` remains equivalent.
An attached definition `f() = body` retains the sole-argument Unit-function
spelling. Spaced Unit is an ordinary exhaustive pattern, so `f () x = body`
has two arguments. The compatible `f _ = body` form remains available.

Top-level functions can recurse and Hindley-Milner inference generalizes their
types. Polymorphic values and parameterized ADTs are supported. Numeric
polymorphism uses ordinary class constraints, for example
`double : Num a => a -> a`. Variable names such as `number`, `equatable`, or
`printable` have no special meaning. Polymorphic recursion and non-regular
recursive ADTs are rejected. Local value bindings are monomorphic; local
functions and lambda bindings may generalize.

An ADT parameter is inferred as row-kinded when it is used as an open effect-row
tail. This supports effect-indexed declarations without explicit kind syntax:

```fango
type Foo eff = Foo (() ->{IO | eff} ())
```

The row parameter may be used in effect rows, but not as an ordinary value type
or as an ordinary value type. When an ADT parameter is known to be row-kinded,
an effect name is accepted as a singleton row argument, so `Foo IO` means
`Foo {IO}`; parameterized effects use the corresponding application, such as
`Foo (State Int)`. Row-kinded parameters are source-level metadata and are
erased from Core and generated Go representations; the declaration remains
available to inference and reflection.

### Tail-call guarantee

Recursion is the language's loop, and self tail calls are guaranteed to run
in constant stack in both backends. A recursive call is optimized when all of
the following hold; programs may rely on it, and arbitrarily deep tail
recursion of this shape never overflows:

- the call invokes the *same* top-level function it appears in (a local
  function that generalizes counts: it is hoisted to the top level), directly
  and with all its arguments;
- the call is in tail position: the returned expression of the body, of an
  `if` branch, of a `case` branch, or the final expression of a block,
  including through any nesting of those — but not inside a lambda body, not
  under a `handle` expression, and not as an operand or argument of anything
  else;
- the function calls itself at its own type (polymorphic recursion at a
  different instantiation is not optimized);
- no lambda or handler clause anywhere in the body captures a parameter that
  the recursion changes; parameters passed through unchanged (such as a
  callback threaded through a driver loop) are always safe to capture.

Mutually recursive functions (`f` calls `g` calls `f`) and monomorphic local
recursive bindings are *not* optimized and consume stack proportional to
depth. A tail call that never terminates, such as `f x = f x`, spins instead
of eventually overflowing.

## Type classes and instances

A class has exactly one type parameter and an indented block of method
signatures. An instance supplies every method exactly once:

```fango
class Label a
    label : a -> String

type Item = Item String

instance Label Item
    label item = case item of
        Item text -> text

describe : Label a => a -> String
describe value = label value
```

Each method must be a function mentioning the class parameter. Additional
method type variables, open effect rows, superclasses, higher kinds, and
default methods are unsupported. Closed effect rows are allowed: a method
`read : a ->{Ask} Int` performs `Ask` when applied. Constructing a method
value must be pure, including implementations written as `method = expression`;
IO during construction is rejected with `UNHANDLED EFFECT`.

Qualified annotations put constraints before `=>`; multiple constraints use
parentheses, as in `(Num a, Ord a) => a -> a`. Inference retains required
constraints on generalized functions and values. An annotation omitting one
reports `MISSING CONSTRAINT`. Explicit constraints provide evidence to the
body, including structural constraints such as `Show (Box a)`; method calls
use that evidence. A constraint variable absent from the annotated type is
ambiguous. Local syntactic functions can generalize constraints; ordinary
local values remain monomorphic.

Instance heads are either a bare type variable (a blanket instance) or a fully
applied named type. Direct function heads are unsupported, but a blanket can
match a function type. Applied heads are parenthesized. Their arguments can be
variables, repeated variables, concrete types, or nested applications.
Conditional instances may require structural constraints; every context
variable must occur in the head, and open effect rows are rejected:

```fango
type Box a = Box a

instance Show a => Show (Box a)
    show box = case box of
        Box value -> "Box " ++ show value

instance Show (Box Int)
    show box = "integer box"
```

Blanket instances provide implementations for types satisfying their context.
For example, an application can use ordinary display as its logging default
and specialize particular domain types:

```fango
class LogValue a
    logValue : a -> String

instance Show a => LogValue a
    logValue x = show x

type Credentials = Credentials String String deriving (Show)

instance LogValue Credentials
    logValue credentials = case credentials of
        Credentials username _ -> username ++ " [password omitted]"

log : LogValue a => a ->{IO} ()
log x = print (logValue x)

main() =
    log 42
    log (Credentials "alice" "secret")
```

This prints `42` and `alice [password omitted]`. An unannotated
`forward x = logValue x` infers `LogValue a => a -> String`, so its caller
supplies the specialized dictionary. Annotating it with only `Show a` instead
reports `MISSING CONSTRAINT`: the blanket does not imply `LogValue a` inside
a polymorphic body. The same rule applies to structured types:
`render x = show (Box x)` infers `Show (Box a) => a -> String`, preserving the
caller's `Show (Box Int)` specialization.

Matching first selects the most specific visible head. Equivalent heads may
have different contexts. Duplicate head/context pairs and incomparable
overlapping heads report `OVERLAPPING INSTANCE`; renaming variables, reordering
constraints, or repeating a constraint does not make a distinct instance.

Within the selected head group, only candidates whose contexts are satisfied
are applicable. A strict superset of constraints takes precedence over its
subset. This compares predicate sets, not logical implications through other
instances. Among the remaining candidates, the latest declaration in each
module wins. If candidates from multiple modules remain, the use reports
`AMBIGUOUS INSTANCE` with their declaration locations and contexts.

For example, a conditional blanket overrides an unconditional fallback when
its context is available, regardless of their declaration order:

```fango
class Inspect a
    inspect : a -> String

instance Inspect a
    inspect _ = "<inspect not implemented>"

instance Show a => Inspect a
    inspect x = show x

instance Inspect String
    inspect x = "\"" ++ x ++ "\""

type Foo = Foo

main() =
    print (inspect 42)
    print (inspect "Hola")
    print (inspect Foo)
```

This prints `42`, `"Hola"`, and `<inspect not implemented>`. With incomparable
contexts such as `Show a` and `Eq a`, the later declaration wins when both
apply in the same module. A `(Show a, Eq a)` context beats either one when
applicable.

An unresolved
type is never guessed from the set of instances. Predicates containing any
unresolved or quantified variable retain their evidence requirement, even
when only one instance currently matches. Concrete predicates resolve through
instances; explicitly passed evidence takes precedence. If no context in the
most-specific head group is satisfied, resolution does not fall back to a
less-specific head. Cycles, nesting-limit failures, and ambiguity encountered
while checking a context are errors, not reasons to try a fallback.

Structured contexts need not be smaller than their heads. For example,
`Show (Box a) => Show (Wrapper a)` can delegate to a wrapper's `Box a` field.
A circular requirement encountered at a concrete use reports
`INSTANCE RESOLUTION` with its cycle; a growing chain reports the nesting
limit instead. Declarations with such structural cycles are allowed, and a
concrete specialization can break a cycle. Blanket contexts must constrain
only their head variable, and cycles between their class requirements are
rejected at declaration time with `INSTANCE CONTEXT`, even when a concrete
specialization could break the cycle for some types.

Inside an instance method, its own head is available as self evidence using
the instance's declared context. This permits direct recursive implementations
without requiring callers to supply a circular self constraint.

Classes, instances, and ordinary definitions are checked in source order.
Later instances do not change earlier concrete calls; a polymorphic function
still uses the evidence supplied by its caller.
Instances may live outside both the class's and the type's defining module
(orphan instances). Resolution sees the defining module and its transitive
imports; exposing lists do not hide instances. Overlap checking covers the
entire loaded module graph. A missing concrete implementation reports
`MISSING INSTANCE`.

The standard classes are independent (in particular, `Ord` does not imply
`Eq`):

| Class | Methods | Standard instances |
| --- | --- | --- |
| `Num a` | `fromInt : Int -> a`, `(+)`, `(-)`, `(*) : a -> a -> a`, `negate : a -> a` | `Int`, `Float` |
| `Eq a` | `(==) : a -> a -> Bool` | `Int`, `Float`, `String`, `Char`, `Bool`, `()` |
| `Ord a` | `(<)`, `(>)`, `(<=)`, `(>=) : a -> a -> Bool` | `Int`, `Float`, `String`, `Char` |
| `Show a` | `show : a -> String` | `Int`, `Float`, `String`, `Char`, `Bool`, `()` |

Their operator-named methods are in the prelude, as is `show`. The named ones —
`fromInt` and `negate` — are not, so using them unqualified takes a `Basics`
import. `print` is an ordinary
Show-constrained function that writes `show value` followed by a newline.
Strings display raw, not quoted.

When evaluation requires a concrete type, an unresolved variable defaults to
`Int` only if its defaulting requirements include standard `Num` and no classes
outside standard `Num`, `Eq`, `Ord`, and `Show`. Eligibility may expand general
instance contexts: `Num a, LogValue a` qualifies through `Num a, Show a` in the
example above. This does not choose evidence; the original constraints are
resolved after defaulting, so concrete specializations still win. A custom
bare constraint without an eligible blanket prevents defaulting.
Other unresolved constraints, including undetermined phantom types, report
`AMBIGUOUS CONSTRAINT`. Decimal literals do not default. An unconstrained
runtime type variable defaults to Unit.

## Algebraic data types and matching

A nominal record declares a named type and a fixed ordered field schema:

```fango
type Counts = { lines : Int, words : Int, bytes : Int } deriving (Eq, Show)

zero = Counts { words = 0, lines = 0, bytes = 0 }
bump : Counts -> Counts
bump counts = { counts | lines = counts.lines + 1 }
```

A record literal provides every declared field exactly once; source field order
does not affect its type. `value.field` projects a field, and
`{ value | field = expression, ... }` produces a new value of the same nominal
type. Update expressions are evaluated left to right and the original value is
evaluated once. A projection or update receiver must already have a known
nominal record type.

A literal may omit its type name when the expected type already says which
record it is:

```fango
type Point = { x : Int, y : Int }
type Circle = { center : Point, radius : Int }

near = Circle { center = { x = 10, y = 20 }, radius = 8 }

far : Circle
far = { center = { x = 90, y = 90 }, radius = 1 }
```

The type comes from context and from nothing else: an annotation, a parameter
type at the call site, the declared type of an enclosing field, or a branch
unified with a known type. Field labels never choose a record, so adding a
second record type with the same labels anywhere in scope cannot change how an
existing program infers. A literal no context reaches reports `AMBIGUOUS
RECORD`, even when exactly one record in scope has those labels. Once the type
is known the schema is checked as usual, and a label whose schema this module
cannot see still reports `PRIVATE RECORD FIELD`.

A capitalized name immediately before `{` always names the record being built,
so a constructor that takes a record parenthesizes an inferred literal
(`Wrap ({ x = 1 })`); writing `Wrap { x = 1 }` reports `UNKNOWN RECORD` and
says so.

A nominal record pattern names its type and any fields to inspect, and may omit
the type name on the same terms:

```fango
case line of
    IO.Line { text = "", ending = ending } -> ending
    IO.Line { text = text } -> text

startsAt : Point -> Int
startsAt { x = x } = x
```

Fields are keyed and may be reordered. Omitted fields are implicit wildcards,
so `IO.Line {}` is irrefutable. Duplicate and unknown fields are rejected, and
matching requires the field schema exposed by `Type(..)`. A record pattern
delimits itself, so it needs no parentheses in an argument position.

Records participate in module abstraction. An exposing item `Counts` makes
only the type name available, while `Counts(..)` additionally exposes its field
schema for construction, projection, and update. Derived equality
compares fields in declaration order. Derived display has the form
`Counts { lines = 1, words = 2, bytes = 3 }`.

A `type` declaration defines a nominal type. Its right-hand side is either a
record schema or one or more constructor alternatives; it is never a type
alias:

```fango
type Status a = Pending | Done a

orPending : a -> Status a -> a
orPending fallback status =
    case status of
        Pending -> fallback
        Done x -> x
```

Constructor arguments are type atoms. Parenthesize applied or function types,
as in `Cons a (List a)` or `Fn (a -> b)`. Constructors are ordinary curried
values and can be partially applied.

Lists have construction syntax backed by the bundled `List` type:

```fango
empty = []
numbers = [1, 2, 3]
extended = [0, 1 | numbers]
```

`[a, b]` is `Cons a (Cons b Nil)`, and `[a, b | tail]` is
`Cons a (Cons b tail)`. Elements are evaluated from left to right, followed
by the tail, and the existing tail is shared rather than copied, in constant
time however many lists already share it. All elements have one type and
the tail must be a list of that type. A trailing comma is not accepted, and
`|` requires at least one element on its left and one tail expression on its
right. Bracket syntax selects the bundled constructors directly and needs no
import; naming `List`, `Nil`, or `Cons` still does.

Tuples use parentheses and commas in type, expression, and pattern position
alike:

```fango
labelled : (Int, String)
labelled = (1, "one")

keyOf : (k, v) -> k
keyOf pair =
    case pair of
        (key, _) -> key
```

`(a, b)` is `Tuple.Pair a b` and `(a, b, c)` is `Tuple.Triple a b c`, in
every position. Elements are evaluated left to right. A tuple holds two or
three elements; four or more is a `TUPLE TOO BIG` error pointing at nominal
records, whose fields have names. `(e)` with no comma stays an ordinary
grouped expression, type, or pattern, and `()` remains Unit. Like bracket
syntax, tuple syntax selects the bundled types directly and needs no import.

A class context is told apart from a tuple type by its `=>`, so
`(Eq a, Show a) => (a, a) -> String` reads the way it looks.

Equality, ordering, and display are opt-in, either handwritten instances or an
explicit deriving clause:

```fango
type Tree a = Leaf a | Branch (Tree a) (Tree a) deriving (Eq, Ord, Show)
```

`Eq`, `Ord`, and `Show` are derivable out of the box, and any class becomes
derivable through a `deriver` declaration (see
[Compile-time metaprogramming](#compile-time-metaprogramming)). Deriving a
class with no deriver is `CANNOT DERIVE`.

A generated instance's context is exactly what its generated methods need: a
field the generator never reads, and a phantom parameter that appears in no
field, add no constraint. Direct recursive fields reuse the instance being
derived. Other cyclic context chains follow the ordinary resolution error
rules. Concrete function fields require suitable blanket evidence; polymorphic
function fields retain their class constraint. Mutually recursive deriving
groups are not supported. A type error inside generated code points at the
line of the generator that produced it, with a note naming the `deriving`
clause that ran it.

Derived equality compares constructors and corresponding fields. Derived
display concatenates the constructor name and field displays with spaces,
without added parentheses or string quotes; a record displays as
`Name { field = value, … }`. Derived ordering compares constructors by
declaration position, then fields left to right; all four of `<`, `>`, `<=`,
and `>=` are generated together.

`case` branches align with the first pattern after `of`. Patterns support
constructors, nominal records, integer/float/string/Char literals, variables,
pinned values, `()`, and `_`. Unit has exactly one inhabitant, so `()` is an
exhaustive Unit pattern. `^expected` compares with an existing local, top-level,
imported, or qualified value and requires `Eq`; it does not bind a name. Pins
cannot refer to a binder introduced by the same pattern. Branch bodies may be
inline expressions or blocks. Matches
must be exhaustive and non-redundant, patterns must have the constructor's
exact arity, and a pattern cannot bind the same variable twice.
Integer patterns require both `Num` and `Eq`: the scrutinee is compared with
the pattern's `fromInt` value. Branch order is preserved even when different
integer literals compare equal under a custom instance. Redundancy checking
accounts for collective constructor coverage and identical literal tests;
it does not attempt to prove laws of user-defined equality.
Pins are conservatively refutable, so pinned branches need structural or
catch-all coverage after them. Identical pins can make a later branch
redundant, but different pins are never assumed to cover a type collectively.

List patterns use the same bracket forms:

```fango
case values of
    [] -> "empty"
    [only] -> "singleton"
    [first, second | rest] -> "two or more"
```

A pattern without `|` matches exactly its written length. A tail pattern
matches the remaining list, so `[first | rest]` is the bracket spelling of
`Cons first rest`. List patterns participate in the same exhaustiveness,
redundancy, nesting, pinning, and duplicate-binder checks as constructor
patterns.

The same patterns may appear on the left of strict local or top-level value
bindings:

```fango
Pair first second = pair
```

Such a binding must bind at least one name and its single row must be
exhaustive. It has no direct annotation syntax; an annotation directly above one
is a `DESTRUCTURING ANNOTATION` error, so annotate a named subject and
destructure that subject on the following declaration. The RHS is checked and
evaluated once, then every bound name becomes visible simultaneously. A
top-level destructuring group is monomorphic and all its names participate in
ordinary collision and export checks. `main` must be a direct declaration and
cannot be introduced inside such a pattern.

## Effectful function types

Effects appear only on function arrows. `A ->{IO} B` applies an `A` argument,
performs `IO`, and returns a `B`. `{Console, Fail String | e}` is a row with two
known labels and an open tail; `{e}` is the compact open-tail spelling. Pure
arrows omit a row, so `A ->{} B` is rejected as redundant. The spaced
`A -> {IO} B` spelling is equivalent to `A ->{IO} B`.

Every curried arrow owns its execution effects. `A ->{IO} (B -> C)` performs
when applied to the `A`; `A -> (B ->{IO} C)` performs only when the returned
function is applied to the `B`.

There is no implicit execution. Expected types, annotations, bare mentions,
bindings, conditionals, and higher-order arguments never apply a function.
Unit functions must be called explicitly:

```fango
say : () ->{IO} ()
say() = print "hello"

main() =
    action = say
    action()
    input = readLine()
    print input
```

Function values are first-class and may be stored in ADTs. Partial operation
application remains a pure function; an operation performs only when
saturated.

Rows may be shared across higher-order arrows. For example:

```fango
map : (a ->{e} b) -> List a ->{e} List b
```

A pure callback instantiates `e` to empty; an effectful callback propagates its
row to the traversal call.

A body may call arrows that carry the bare tail alongside arrows that add
labels to it, in either order:

```fango
using : (() ->{e} a) -> (a ->{Fail String | e} ()) ->{Fail String | e} ()
using acquire use =
    resource = acquire()
    use resource
```

Calling `acquire`, whose row is the bare tail `{e}`, does not stop the
surrounding row from gaining `Fail String` from the later call, and swapping
two such statements never changes whether a definition is accepted. The
annotated row may also carry effects a callee does not perform, so a
`{IO, Fail String | e}` body may call a `{Fail String | e}` argument and
`print` besides.

A shared row variable describes the permitted combined effects. Each callback
may perform fewer effects, whether it is named or written inline:

```fango
pair : (() ->{e} Int) -> (() ->{e} Int) ->{e} Int

pair emit boom      -- e includes IO and Fail String
pair boom emit      -- the same combined row
pair (\_ -> emit()) boom
```

Argument order does not determine the permitted row. Partial applications and
ordinary wrappers use the same rule as saturated calls, including `Scope`.
Passing a callback never executes it, and widening one use does not change its
binding or other uses.

The rule also applies to stored callbacks and covariant effect-indexed values.
For example, `type Test e = Test (() ->{e} ())` permits pure and IO tests in
one list, in either order. The compiler derives variance from fields, including
recursive types and imported abstract types. Function inputs reverse the
direction: a function accepting only pure callbacks cannot stand in for one
that must accept IO callbacks. Parameters used in both directions, effect-label
arguments, and class constraints remain invariant. Widening never removes an
effect or relaxes capture and resource restrictions.

Definition annotations remain exact about the known effects on their arrows.
A pure body annotated `() ->{IO} Int` is rejected, at top level or in a local
definition. This differs deliberately from argument compatibility: a named pure
function can be passed to a parameter permitting IO without claiming that its
own definition performs IO. Annotation type variables and residual row tails
remain rigid.


An annotation's tail stays rigid, so a body may not perform an effect the
annotation does not list. That reports `EFFECT MISMATCH`, naming the effects
the expression performs and the effects available where it appears.

## Effects and handlers

Effects declare operations using an indented signature block:

```fango
effect Ask
    ask : () -> String

query _ = if ask () == "yes" then 40 else 0

main =
    print
        (handle query () of
            ask () -> resume "yes"
            return n -> n + 2)
```

The compiler adds the declaring effect to each operation's type. Functions may
annotate closed or open effect rows. An operation with a Unit argument is
called explicitly with `()`.

An abort-only effect marks every operation with `abort`:

```fango
effect Fail error
    abort fail : error -> value
```

All operations in one effect must use the same discipline; mixing marked and
unmarked operations is rejected. An abort operation may introduce exactly one
operation-local type variable as its whole result, as above. That variable may
not occur in a payload parameter. This is the only supported form of
operation-local polymorphism. Abort operations cannot be `native`. A saturated
abort never returns normally, while partial application remains a pure function
value.

A handler handles one effect and must contain a clause group for every
operation of that effect. Adjacent repetitions of an operation form one
source-ordered, exhaustive, non-redundant pattern group; a noncontiguous repeat
is a duplicate-clause error. An optional adjacent `return` group matches the
handled computation's normal result under the same rules. All clauses align
like `case` branches and accept full argument patterns. `resume value`
continues from a resumptive operation. An abort clause instead returns the
handler answer directly and has no resume binding:

```fango
attempt action =
    handle action() of
        fail error -> Err error
        return value -> Ok value
```

Using `resume` there is a `RESUME IN ABORT CLAUSE` error. An abort evaluates
all payload arguments left to right, unwinds to the exact handler activation,
and only then runs its clause with the surrounding outer evidence. Recursive
and nested handlers of the same effect remain distinct. The abort answer
bypasses the handler's `return` clause; normal completion runs `return` once.
An abort raised by an abort clause or return clause propagates outward rather
than re-entering that activation.

A parameterized handler inserts `with snapshot = initial` between its subject
and `of`:

```fango
handle action() with current = initial of
    get () -> resume current with current
    put next -> resume () with next
    return value -> StateResult { value = value, state = current }
```

`with` is contextual and remains an ordinary lowercase name elsewhere. The
initial state is evaluated once before entering the handled body. `current` is
an immutable snapshot visible in operation and `return` clauses, but not in the
handled body. Every operation path must use `resume value with nextState`;
ordinary handlers continue to use `resume value`. The value and next-state
expressions evaluate left to right exactly once, and the state is committed
only after both finish successfully. The `return` clause sees the final state.

Resumptive handlers are deliberately restricted: every normally completing
operation-clause path must end in exactly one tail call to `resume`. A
saturated abort-only call is an exceptional terminal, so a path such as
`if valid then resume answer else fail error` is legal. Non-tail or escaping
continuations, general operation-local polymorphism, mixed-discipline effects,
and handlers for builtin `IO` are rejected. Effects other than the handled
label remain in the surrounding row. A resume in an operand or before another
expression is a `NON-TAIL RESUME`; a normal clause path without a resume is a
`MISSING RESUME`; and a bare, partially applied, stored, or lambda-captured
resume is a `RESUME ESCAPES` error. Diagnostics point to the offending
expression and name the owning operation clause's location. Nested operation
clauses bind their own resume, while nested handled bodies and return groups
retain the surrounding resume binding.

The compiler preserves a control-aware calling convention through
higher-order functions and abstract effect evidence. Direct calls keep their
plain generated-Go result, while definitions whose callback/evidence contract
can later carry a non-local exit have stable Direct and Exit ABI families in
their defining module. The Exit family uses an internal tagged `Outcome` and
propagates it before evaluating the next source expression. Function values
stored in ADTs or class dictionaries use matching representation families.
This calling convention is an implementation guarantee visible in
`--emit-go`. Abort-only operations select the Exit family; ordinary
tail-resumptive handlers retain the Direct fast path where their context allows
it.

Ordinary stateless user-declared effects have durable evidence: returning a pure closure
that captures an immutable Reader-style handler remains legal. There is no
scope annotation in source syntax. Parameterized handlers are scoped: a result
that can retain their local capability is rejected with `STATE RESULT ESCAPES`
or `RESOURCE ESCAPES`. Inferred contracts distinguish functions that capture
local evidence from functions that are independent of it. A partial operation
that receives fresh evidence on its next application does not by itself retain
the preceding handler. This does not shorten the lifetime of existing stateless
handler values.

A reusable handler wrapper may annotate that residual flow with an open row
tail. The handled label disappears from the callback's row while every other
effect the callback performs passes through the wrapper's own row:

```fango
run : (() ->{Ask | e} a) ->{e} a
run action =
    handle action() of
        ask () -> resume "yes"
```

Ambient `print : Show a => a ->{IO} ()` displays values through their instance.
`readLine : () ->{IO} Maybe IO.Line` distinguishes clean EOF from a line and
preserves the exact line ending as described under the bundled standard
library. Call it as `readLine()` (or equivalently `readLine ()`).

## Cleanup scopes

A handler releases a resource only for the failure it handles. Cleanup that
must also run when an arbitrary residual effect leaves the region needs a
scope, which the bundled `Scope` module provides through ordinary function
application — there is no `try`, `catch`, `finally`, `using`, or `defer`
syntax:

```fango
withResource label action =
    Scope.bracket (\_ -> open label) close action

main() =
    text = withResource "input" (\resource -> readAll resource)
    print text
```

`bracket acquire release use` evaluates `acquire()` once. If that fails,
nothing is released. Otherwise `use` runs on the acquired resource and
`release` runs exactly once when the scope exits:

| Event | Behavior |
| --- | --- |
| Acquisition fails | Propagate the failure; nothing is released |
| Body returns normally | Release, then produce the body value |
| Failure caught within the body | Continue the body; release at the real scope exit |
| Exit targets an outer handler | Release before the outer clause runs |
| Failure in a handler's `return` clause | Release before that failure propagates |
| Nested scopes exit | Release in reverse acquisition order |

So a scope inside a handler releases before the handler's abort clause sees
the failure:

```text
open -> body -> fail requested -> close -> outer fail clause
```

while a scope around a handler releases after that clause has produced the
handler's answer. The two nestings are different programs; neither ordering is
applied to the other.

A release runs with the evidence where it was written, not with whatever
handlers happened to be installed where the body exited, and may itself use
nested handlers.

Acquisition and release must complete synchronously. A callback that suspends
reports `SUSPENDING RESOURCE CALLBACK`, including through named wrappers,
stored callbacks, and resumptive effect handlers. These obligations follow the
actual callback rather than its widened effect row. A callback may synchronously
consume a producer using its own iterator scope. An abort clause outside the
callback runs after unwinding and is outside this restriction.

When a release fails, the failure the body was already carrying stays primary:

| Body | Release | Result |
| --- | --- | --- |
| Succeeds | Succeeds | The body value |
| Succeeds | Fails | The release failure |
| Fails | Succeeds | The original failure |
| Fails | Fails | The original failure, with the release failure recorded alongside it |

A recorded release failure is kept in the exit rather than discarded, in
deterministic inner-to-outer order. No API observes it yet, so a program sees
the primary failure plus whatever the release did before failing.

`finally action cleanup` is `bracket` without a resource. Because its resource
is `()`, it places no restriction on the result it returns.

A scope rejects a result that retains its resource with `RESOURCE ESCAPES`.
This includes a resource hidden in an ADT or captured by a returned function.
An unrelated function may be returned when its inferred contract proves that
it does not capture the resource. Scalars and transitively capture-free data
remain valid results.

Libraries mark opaque resource types with a declaration pragma:

```fango
module Connection exposing (Handle, withConnection)

{-# resource #-}
type Handle = Handle Int

withConnection address use =
    Scope.bracket (\_ -> openConnection address) closeConnection use
```

Here `openConnection` and `closeConnection` are private library functions.
`{-# resource #-}` must precede exactly one type declaration, allowing comments
and whitespace between them. It supports unions, records, and parameterized
types. It takes no arguments, cannot be repeated for one declaration, and is
not a file-header directive. `resource` remains an ordinary identifier outside
the pragma. The declaration works at the REPL as well.

A resource type carries a capability even when represented by an `Int`.
Export it as `Handle`; exporting its representation with `Handle(..)` or
`exposing (..)` reports `RESOURCE REPRESENTATION EXPOSED`. Importers cannot
inspect its constructors, record fields, or reflected schema. Native code and
the defining module remain responsible for resource representation and native
correctness. The marker alone does not acquire or release resources.

Wrappers and helpers infer and export capture and retention contracts without
compiler registration or written lifetime annotations. A helper may borrow a
resource synchronously. Returning it, retaining it through an indirect callback,
or storing it in an outer handler reports `RESOURCE ESCAPES`, even when the
enclosing result is `()`. A proven non-retaining outer resumptive handler may
use it synchronously. Passing it as an abort payload across its cleanup boundary
is rejected because the abort clause runs after release. Release callbacks obey
the same retention checks. Diagnostics identify the owning scope and the value
or destination that would outlive it.

Contracts are conservative at recursive joins where distinct dynamic owners
cannot be proved identical. Cursor advancement additionally carries exclusive
access obligations. Written capture contracts are not implemented.

`Scope.bracket` remains a compiler intrinsic for cleanup and lifetime handling.
Its callbacks use the ordinary argument-inclusion rule: acquisition and release
may use IO while the body also fails. Partial applications and ordinary wrappers
have the same effect compatibility, subject to the existing resource restrictions.

A scope does whatever its callbacks do, so a program whose parts are all
stage-safe may run one at compile time. Resources and system entropy remain
forbidden there because they are effects, not because a scope is special.

## Compile-time metaprogramming

fango has one compile-time stage. `quote` goes up a stage and `$(…)` comes
back down, and together they are the whole staging surface. `quote` is a
reserved word; `$` is a token only as part of `$(`.

`typeOf T` produces an opaque `Meta.TypeRepr` for a closed, fully applied
type. Nominal types compare by compiler identity, while applications and
function arrows compare structurally, including the effects on each arrow.
`Meta.sameType`, `Meta.head`, `Meta.args`, `Meta.isVar`, and `Meta.typeName`
inspect this representation; display text never determines identity.

`Meta.Lift` provides `lift : a -> Code` for `Int`, `Float`, `String`, `Char`,
`Bool`, and `()`. The generated literal retains its scalar type. `Meta.fail`
stops expansion and reports `COMPILE-TIME FAILURE` at the splice site.

`Meta.info : TypeRepr -> Reflected` reads a type's schema:

```fango
type Reflected = Opaque | Visible TypeInfo
```

It answers `Visible` only when the type's schema is readable where `typeOf`
was written. That is not a new rule: `exposing (T)` reflects as `Opaque` and
`exposing (T(..))` reflects in full, exactly as those two forms already govern
constructor patterns and record fields. A type variable and a scalar are
`Opaque` too — neither has a schema to read.

```fango
type TypeInfo = { name : String, moduleName : String, ty : TypeRepr, params : Items TypeRepr, shape : Shape }
type Shape = Union (Items Ctor) | Record Ctor
type Ctor = { name : String, symbol : String, index : Int, owner : TypeRepr, fields : Items Field }
type Field = { name : String, index : Int, ty : TypeRepr }
```

A union constructor's fields are positional, so their `name` is empty; a
record's sole constructor carries the type's own name and its fields' names.
Field types come back instantiated at the reflected type's arguments, so a
generator sees `Int` rather than the declaration's parameter.
`Meta.ctorsIn` flattens the two shapes into one constructor list.

`Items` is `Meta`'s own list — `NoItems | Item a (Items a)`, with
`Meta.foldItems`, `Meta.mapItems`, and `Meta.lengthItems`. `Meta` cannot import
`List`, because `List` derives its own instances and so depends on the module
that depends on `Meta`.

### Derivers

A `deriver` declaration opens `deriving` to a class:

```fango
class Tag a
    tag : a -> String

deriver Tag
    tag subject valueCode =
        Meta.match subject valueCode (\bound -> Meta.lift bound.ctor.name)

type Colour = Red | Green Int deriving (Tag)
```

A deriver method's type is dictated by the class: for a class method with *n*
arrows, its deriver method takes a `TypeInfo` plus *n* `Code` arguments and
returns `Code`. A deriver is otherwise an ordinary fango function, checked by
ordinary inference. It must supply exactly the class's methods
(`MISSING METHOD`, `UNKNOWN METHOD`), a class may have only one deriver
(`DUPLICATE DERIVER`), and — like an instance — it is visible by dependency.

The compiler owns the traversal, so a deriver never invents a binder:

- `Meta.match : TypeInfo -> Code -> (Bound -> Code) -> Code` builds the
  exhaustive case over the type's constructors and binds every field, handing
  each branch a `Bound { ctor : Ctor, fields : Items BoundField }` whose
  fields carry `{ name, index, ty, value : Code }`. Nesting two calls produces
  the nested case a two-argument method needs.
- `Meta.construct : Ctor -> Items Code -> Code` goes the other way, for a
  method that produces an `a`. A record constructor produces a record literal.

A `deriver` must precede, in source order, any `deriving` clause that uses it
— including on a type declared earlier in the same file. The bundled `Derive`
module supplies the derivers for `Eq`, `Ord`, and `Show`; a file that writes
`deriving` depends on it automatically, the way a file that writes `quote`
depends on `Meta`.

`quote atom` builds a value of the abstract type `Meta.Code`. It does not
evaluate the quoted expression — it describes it. The quoted text is ordinary
fango and takes exactly one atom, so anything larger is parenthesized:

```fango
import Meta exposing (Code)

answer : Code
answer = quote (6 * 7)
```

`$(expression)` inside a quote is a **hole**: the expression is evaluated
along with the quote, in source order like any other argument, and must
produce `Code`, which is pasted into the quoted text:

```fango
twice : Code -> Code
twice c = quote ($(c) + $(c))
```

`$(expression)` in ordinary program text is a **splice**: the compiler
evaluates the expression while compiling, and the code it produces takes the
splice's place and is checked there:

```fango
main() = print $(twice answer)     -- prints 84
```

Nesting is limited to one level in each direction. A quote inside a quote and
a splice inside a splice are both `STAGE ERROR`.

Quoted code is resolved in the module that wrote it and checked at the site
that splices it. Because names are resolved before inference, generated code
can neither capture nor be captured by names at the splice site.

A splice operand may name any top-level definition earlier in graph and source
order — the ordinary scoping rule, which is also the stage discipline: a
splice can only name what is already checked. **Top-level definitions are
available at both stages.** Local binders are not: a lambda parameter, block
binding, or case binder belongs to the stage it was introduced at, and using
one at the other stage is a `STAGE ERROR`. Staged code that needs a runtime
value takes it as a function argument instead.

Compile-time code runs inside the compiler and is restricted accordingly:

- it must type with an empty effect row (`COMPILE-TIME EFFECT`);
- it may not reach a Go sidecar or a bundled native that observes external
  state; deterministic `Random.runSeeded` is safe, while system entropy from
  `Random.runSystem` reports `COMPILE-TIME NATIVE`;
- it is bounded by an evaluation-step budget (`COMPILE-TIME LIMIT`).

Together these make generated Go reproducible.

`Code` is a **compile-time-only type**: a definition whose type mentions it is
not emitted, and no expression in an ordinary definition may have such a type.
Both halves report `STAGE ERROR`. In practice a code-producing helper is an
ordinary definition that simply never reaches the executable:

```fango
repeat : Int -> Code -> Code
repeat n c = if n <= 1 then c else twice (repeat (n - 1) c)
```

Quotes and splices work at the REPL with no extra mechanism, since the session
keeps one checker and one evaluator.

Quotes, splices, derivers, and `deriving` all work at the prompt. A failed
input leaves nothing behind: a `deriving` clause whose deriver fails does not
install its type, and a declaration whose splice fails does not install its
name.

Only expressions can currently be quoted and spliced. Generating a declaration
group, including a type declaration, remains a future milestone described in
the roadmap.

## Entry points

A file passed to `build` or `run` must define `main`. A pure value is valid:

```fango
main = 42
```

A value-style `main` may perform ambient IO:

```fango
main : ()
main = print 42
```

The recommended effectful form is a nullary function:

```fango
main : () ->{IO} ()
main() = print "hello"
```

A value-style `main` may perform ambient IO but no unhandled custom effect. A
function-style effectful `main` must have exactly the shown IO/Unit shape and
one discarded Unit parameter; `main _ = ...` remains compatible. Non-Unit pure
`main` values are primarily observable in the REPL and test harness; an
ordinary built executable exits without printing them.

## REPL

`fango repl [dir]` evaluates expressions, installs
value/function/type/effect/class/instance/deriver declarations, imports
modules, and accepts multiline layout-sensitive input. The prompt starts in
the same scope `Prelude` gives a module, and, since a later prompt may use
them, it also resolves the modules surface syntax desugars into — so `[1, 2]`,
`(1, 2)`, and `deriving` work without putting `List`, `Tuple`, or `Derive` in
scope. Names resolve exactly as they do in a module: an unqualified name must
be exposed by the prelude, an import, or a prompt declaration, and a qualifier
must be an imported module's name or alias, so `Dict.empty` is an
`UNKNOWN QUALIFIER` until `import Dict`, while `IO.write` works because the
prelude imports `IO`. Definitions echo
their inferred types; expressions print a value and type. Errors do not end the
session. Redefinition is allowed at the prompt, while existing memoized values
and closures retain earlier bindings.

An `import` line at the prompt takes every form a module's import does:

```text
> import Geometry.Point as P exposing (origin)
loaded Geometry.Point
```

The source root is the directory given to `fango repl`, or the working
directory, and a local `Foo.Bar` resolves to `Foo/Bar.fango` beneath it under
the same rules and diagnostics as a build (`MISSING MODULE`, `RESERVED
MODULE`, `MODULE/PATH MISMATCH`, `IMPORT CYCLE`, and so on). Bundled modules
outside the prelude, such as `Dict` or `String`, import the same way. The
session echoes `loaded M` for each module the import brought in for the first
time, dependencies included, in dependency order; a module already loaded
echoes nothing. The imported module's instances and derivers become usable at
the prompt, and its sidecar, if any, runs in the session's native worker.
Importing `File` and `Fail` gives the prompt scoped file access under the same
checks as a program: a body that fails releases its handle before the failure
reaches `attempt`, and a body that tries to return the handle is a
`RESOURCE ESCAPES` error at the prompt.

Prompt imports are cumulative. Importing a module again adds the names its
new exposing list selects, and repeating an alias for the same module is
accepted; binding the alias to a different module is a `DUPLICATE IMPORT
ALIAS`, as it would be in a file. An import is all-or-nothing: if any of its
modules fails to load, check, or expose a requested name (`UNKNOWN IMPORT`),
the session keeps neither the modules nor the names, and the same import can
be retried after the file is fixed.

A prompt declaration may redefine a name the prompt itself declared, but not
one an import or the prelude exposes: that is the `UNQUALIFIED COLLISION` it
would be in a module. Qualified access to the exposed name stays available.
Imports see only a module's public interface; a private name is a `PRIVATE
OR UNKNOWN NAME` under any qualifier.
The prompt accepts a single exhaustive patterned function equation and
top-level destructuring bindings. It does not collect multiple function
equations into a grouped input; use a source file for those.
Record type declarations echo `Name : record`; their synthetic internal
constructor is not part of the surface namespace.
Classes cannot be redefined. Type redefinition creates a fresh identity and
can install fresh instances for that identity. Failed instance, deriver, and
deriving declarations do not modify the persistent declaration environment,
and neither does a declaration whose splice fails part way through. Types are
printed with their class contexts. Expression display uses available `Show`
evidence; otherwise it prints `<value : T>` or `<function>` without adding a
Show constraint to the expression.

Effectful expressions run directly. Ordinary effectful declarations such as
`x = print 1` are rejected. Effectful function definitions are accepted and
execute only when explicitly applied.

Supported commands are:

```text
:type <expr>   show a type without evaluating
:help          show command help
:quit, :q      leave the REPL (Ctrl-D also exits)
```

`:reload`, cancellation, and interactive history are not implemented; a
module edited on disk after it was imported is not re-read in the same
session.
