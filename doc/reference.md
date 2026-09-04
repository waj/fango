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

## Source layout and names

A program is currently one source file. Top-level declarations begin in column
1 and are visible only to declarations below them. Tabs are rejected; indent
with spaces. `--` starts a line comment, and `{- ... -}` comments may nest.

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

Functions are curried and application uses whitespace: `f x y`. Partial
application and functions as values are supported. Lambdas use
`\x y -> expression`; parenthesize a lambda when passing it as an argument.
`_` discards a function parameter. Functions may have indented block bodies,
and local function bindings are supported.

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

## Computation types

`{IO} String` is a delayed computation that may perform `IO` and return a
`String`. `{}` is a closed empty row; `{Console, Fail String | e}` has two
known labels and an open tail. The compact forms `{e}` and `{Console, e}` also
denote open rows. `A ->{IO} B` and `A -> {IO} B` are equivalent spellings for
an effectful function.

A bare mention of a computation runs it when the surrounding context expects
its result:

```fango
say : {IO} ()
say = print "hello"       -- defines the computation; does not print yet

main : {IO} ()
main =
    say                   -- runs it
    line = readLine       -- runs and binds the resulting String
    print line
```

An unannotated local `=` runs a computation-valued RHS. An explicit
computation annotation stores it without running:

```fango
saved : {IO} ()
saved = say
```

Computation values may be used as definition, parameter, and return types. They
cannot instantiate an unconstrained type variable or be stored in an ADT.
Partial operation application remains a pure function; an operation performs
only when saturated.

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
called with `()`; the mention-runs rule also permits bare computation-typed
builtins such as `readLine`.

A handler handles one effect, must contain exactly one clause for every
operation of that effect, and may include one `return value -> expression`
clause. All clauses align like `case` branches. Operation parameters may use
names, `_`, or `()` where the declared parameter is Unit. `resume value`
continues the handled computation.

Current handlers are deliberately restricted: every reachable operation-clause
path must end in exactly one tail call to `resume`. Aborting clauses, non-tail
or escaping continuations, operation-local/result polymorphism, and handlers
for builtin `IO` are rejected. Effects other than the handled label remain in
the surrounding row.

Builtin `print : a ->{IO} ()` displays supported ground values and ADTs.
`readLine : () ->{IO} String` reads one line and returns the text without its
line ending; bare `readLine` also runs through computation forcing.

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

The preferred effectful form is a computation:

```fango
main : {IO} ()
main = print "hello"
```

The Unit-function spelling is also accepted:

```fango
main : () ->{IO} ()
main _ = print "hello"
```

A value-style `main` may perform builtin IO but no unhandled custom effect. A
computation-style or function-style effectful `main` must have exactly the
shown IO/Unit shape. Function-style `main` accepts only one discarded Unit
parameter. Non-Unit pure `main` values are primarily observable in the REPL and
test harness; an ordinary built executable exits without printing them.

## REPL

`fango repl` evaluates expressions, installs value/function/type/effect
declarations, and accepts multiline layout-sensitive input. Definitions echo
their inferred types; expressions print a value and type. Errors do not end the
session. Redefinition is allowed at the prompt, while existing memoized values
and closures retain earlier bindings.

Effectful expressions run directly. Ordinary effectful declarations such as
`x = print 1` are rejected, but annotated computation definitions are accepted
and execute on each mention.

Supported commands are:

```text
:type <expr>   show a type without evaluating
:help          show command help
:quit, :q      leave the REPL (Ctrl-D also exits)
```

`:load`, `:reload`, cancellation, and interactive history are not implemented.
