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
make ci         # formatting, vet, correctness, and performance gates
```

The CLI accepts one `.fango` source file:

```text
fango build main.fango [-o out] [--emit-go]
fango run main.fango
fango check main.fango
fango repl
fango clean main.fango
```

`build` writes a native executable (defaulting to the source basename without
`.fango`). `--emit-go` prints generated Go instead. `run` builds if needed and
runs the cached executable. `check` runs through parsing, inference,
elaboration, and Core validation without generating Go. `clean` removes the
source file's persistent `.fango/build` artifacts.

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

An exposing list is either `(..)` by itself or a non-empty comma-separated
list. A lowercase item exports/imports a value or one effect operation. `Type`
or `Effect` exposes the abstract type/effect label; `Type(..)` also exposes all
constructors and `Effect(..)` all operations. Constructors cannot be selected
individually, member lists cannot be partial, and imported declarations cannot
be re-exported. Qualified names are accepted for values, operations,
constructors, patterns, types, effect rows, and handler clauses.

The entry file's directory is the source root. `Foo.Bar` resolves exactly to
`Foo/Bar.fango` beneath it. Imported files require a header whose module name
and casing match that path. A named entry must match its top-level filename,
so `Main.fango` declares `Main`. Headerless entry files remain compatible,
receive a private synthetic identity, and cannot themselves be imported.

`build` and `run` use only the entry module's `main`; a dependency's `main` is
an ordinary declaration. `check` does not require `main`. Imports expose only
the direct module's declared public interface, never its dependencies. Import
cycles are rejected with the complete cycle chain. The generated build
directory includes `sources.json`, containing each transitive source's logical
name, root-relative path, and SHA-256 hash for build invalidation.

Top-level declarations begin in column 1 and are visible only to declarations
below them within their module. Tabs are rejected; indent with spaces. `--`
starts a line comment, and `{- ... -}` comments may nest.

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

## Values and operators

Built-in value types are `Int`, `Float`, `String`, `Bool`, and Unit `()`.
Integers are signed 64-bit decimal literals. Floats include `1.25`, `1e3`, and
`1.0e-2`; `.5` and `1.` are not float literals. Strings are single-line,
double-quoted values with `\\`, `\"`, `\n`, `\t`, and `\r` escapes. Booleans are
the constructors `True` and `False`.

Operators, from tighter to looser precedence, are:

| Operators | Meaning | Associativity |
| --- | --- | --- |
| unary `-` | numeric negation | prefix |
| `*`, `/` | multiplication, floating division | left |
| `+`, `-` | addition, subtraction | left |
| `++` | string concatenation | right |
| `==`, `/=`, `<`, `>`, `<=`, `>=` | comparison | non-associative |

`+`, `-`, and `*` accept `Int` or `Float`; `/` accepts only `Float` and there
is no implicit integer-to-float conversion. Ordering is available for numeric
values and strings, not booleans. Equality is available for supported ground
values and structurally for supported ADTs; functions and types transitively
containing functions cannot be compared. Chained comparisons require
parentheses.

`if condition then a else b` is an expression. Its condition is `Bool` and both
branches have the same type.

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
`\x y -> expression`; parenthesize a lambda when passing it as an argument.
`_` discards a function parameter. Functions may have indented block bodies,
and local function bindings are supported.

A Unit function is canonically called as `f()`; `f ()` remains equivalent.
Definitions accept the matching `f() = body` and `f () = body` spellings,
which introduce one discarded Unit parameter. The empty parameter list must be
the definition's only syntactic parameter group. The compatible `f _ = body`
form remains available.

Top-level functions can recurse and Hindley-Milner inference generalizes their
types. Polymorphic values and parameterized ADTs are supported. Numeric
polymorphism prints as `number` and ranges over `Int` and `Float`. Polymorphic
recursion and non-regular recursive ADTs are rejected. Local value bindings are
monomorphic; local functions and lambda bindings may generalize.

## Algebraic data types and matching

A `type` declaration defines one or more constructors; its right-hand side is
never a type alias:

```fango
type Maybe a = Nothing | Just a

withDefault : a -> Maybe a -> a
withDefault fallback value =
    case value of
        Nothing -> fallback
        Just x -> x
```

Constructor arguments are type atoms. Parenthesize applied or function types,
as in `Cons a (List a)` or `Fn (a -> b)`. Constructors are ordinary curried
values and can be partially applied.

`case` branches align with the first pattern after `of`. Patterns support
constructors, nested constructor patterns, integer/float/string literals,
variables, and `_`. Branch bodies may be inline expressions or blocks. Matches
must be exhaustive and non-redundant, patterns must have the constructor's
exact arity, and a pattern cannot bind the same variable twice.

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
    line = readLine()
    print line
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

A handler handles one effect, must contain exactly one clause for every
operation of that effect, and may include one `return value -> expression`
clause. All clauses align like `case` branches. Operation parameters may use
names, `_`, or `()` where the declared parameter is Unit. `resume value`
continues from the handled operation.

Current handlers are deliberately restricted: every reachable operation-clause
path must end in exactly one tail call to `resume`. Aborting clauses, non-tail
or escaping continuations, operation-local/result polymorphism, and handlers
for builtin `IO` are rejected. Effects other than the handled label remain in
the surrounding row.

Builtin `print : a ->{IO} ()` displays supported ground values and ADTs.
`readLine : () ->{IO} String` reads one line and returns the text without its
line ending. Call it as `readLine()` (or equivalently `readLine ()`).

## Entry points

A file passed to `build` or `run` must define `main`. A pure value is valid:

```fango
main = 42
```

A value-style `main` may perform builtin IO:

```fango
main : ()
main = print 42
```

The recommended effectful form is a nullary function:

```fango
main : () ->{IO} ()
main() = print "hello"
```

A value-style `main` may perform builtin IO but no unhandled custom effect. A
function-style effectful `main` must have exactly the shown IO/Unit shape and
one discarded Unit parameter; `main _ = ...` remains compatible. Non-Unit pure
`main` values are primarily observable in the REPL and test harness; an
ordinary built executable exits without printing them.

## REPL

`fango repl` evaluates expressions, installs value/function/type/effect
declarations, and accepts multiline layout-sensitive input. Definitions echo
their inferred types; expressions print a value and type. Errors do not end the
session. Redefinition is allowed at the prompt, while existing memoized values
and closures retain earlier bindings.

Effectful expressions run directly. Ordinary effectful declarations such as
`x = print 1` are rejected. Effectful function definitions are accepted and
execute only when explicitly applied.

Supported commands are:

```text
:type <expr>   show a type without evaluating
:help          show command help
:quit, :q      leave the REPL (Ctrl-D also exits)
```

`:load`, `:reload`, cancellation, and interactive history are not implemented.
