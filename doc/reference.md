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
fango repl
fango clean main.fango
```

`build` writes a native executable (defaulting to the source basename without
`.fango`). `run` builds if needed and runs the cached executable, forwarding
every argument after the source path to the program; an optional `--` is
removed first. `check` runs
through parsing, inference, elaboration, and Core validation without generating
Go. `clean` removes the source file's persistent `.fango/build` artifacts.

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
Every ordinary module implicitly loads `Basics`, receiving the classes
`Num`, `Eq`, `Ord`, and `Show`, every operator `Basics` exposes, the `IO`
effect, and unqualified `show`, `print`, and `readLine`. Other
standard-library APIs still require explicit imports.

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
milestone. `Basics` and the ambient portion of `IO` are implicit; other modules
and APIs must be imported explicitly.

`List` exposes the following algebraic type:

```fango
module List exposing (List(..), range, each, foldl)

type List a = Nil | Cons a (List a) deriving (Eq, Ord, Show)
```

Its inferred public function types are
`range : (Num a, Ord a) => a -> a -> List a`,
`each : (a ->{e} ()) -> List a ->{e} ()`, and
`foldl : (a -> b ->{e} b) -> b -> List a ->{e} b`.

`range start end` produces ascending values by adding one, including `end`
when that value is reached, and returns `Nil` immediately when `start > end`.
It works at both `Int` and `Float`; callers must use finite bounds because the
ordinary recursive implementation is not guaranteed to terminate for `NaN`
or positive infinity. `each action values` applies `action` from left to right
and propagates its effects. `foldl combine initial values` visits values from
left to right, passing the current element first and the accumulator second to
`combine`; callback effects are propagated.

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
trailing newline. It is a native operation available only through an `IO`
import. The names `print : Show a => a ->{IO} ()` and `readLine` come from
ambient IO. `IO` also exposes:

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
line. Malformed UTF-8 input sequences are replaced with U+FFFD.

`Basics` also declares two explicitly importable integer functions (the
implicit prelude exposes only the operators, `print`, `readLine`, and `show`):

```fango
import Basics exposing (modBy, remainderBy)
```

`modBy : Int -> Int -> Int` is the floored modulus: `modBy modulus x` has the
modulus's sign, so `modBy 3 (-4)` is `2` and `modBy (-3) 4` is `-2`.
`remainderBy : Int -> Int -> Int` is the truncated remainder:
`remainderBy divisor x` has the dividend's sign, so `remainderBy 3 (-4)` is
`-1`. A zero modulus or divisor crashes the program in both backends.

`Maybe` exposes the optional-value type:

```fango
module Maybe exposing (Maybe(..), withDefault)

type Maybe a = Nothing | Just a deriving (Eq, Ord, Show)
```

`withDefault : a -> Maybe a -> a` returns the contained value or the
fallback.

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
uncons : String -> Maybe String.Uncons
fromChar : Char -> String
```

`length` counts Unicode scalars and `byteLength` counts UTF-8 bytes. `slice`
uses clamped half-open scalar indices and returns `""` when its end is not
greater than its start. `startsWith prefix text` tests an exact prefix;
`uncons` returns the first scalar and remaining string, or `Nothing` for the
empty string. `fromChar` makes the corresponding one-scalar string.
`String.words : String -> List String` splits on ASCII space, tab, LF, CR,
vertical tab, and form feed, and
`toInt : String -> Maybe Int`, which
parses an optional `+`/`-` sign followed by base-10 digits. An empty digit
sequence, any other character, and values outside the signed 64-bit range all
produce `Nothing`; `String.toInt "007"` is `Just 7` and
`String.toInt "-9223372036854775808"` parses the most negative Int.

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

Because current handlers are tail-resumptive, a handler cannot carry state
of its own across resumes; the bundled handlers instead advance a native
generator cell private to `Random`'s sidecar, and they swap and restore that
cell around the handled computation so nested `runSeeded`/`runSystem` uses
behave lexically. That cell is process state reachable only through these
handlers.

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
the Go function and a Unit result is represented by no Go result. Functions,
ADTs, polymorphic variables, class constraints, explicit effect rows on native
value types, Go type parameters, multiple
results, and `error` results are rejected. Sidecars may import only Go
standard-library packages. Every call-form declaration needs its matching
exported function, and every exported sidecar function needs a declaration.

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

The interpreter API accepts user sidecars now. The CLI REPL still has no module
loading command, so there is not yet a CLI path for bringing a user module into
an interactive session.

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
polymorphism uses ordinary class constraints, for example
`double : Num a => a -> a`. Variable names such as `number`, `equatable`, or
`printable` have no special meaning. Polymorphic recursion and non-regular
recursive ADTs are rejected. Local value bindings are monomorphic; local
functions and lambda bindings may generalize.

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

Their operator-named methods are ambient, as is `show`. The named ones —
`fromInt` and `negate` — require an explicit `Basics` import. `print` is an ordinary
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

A record literal must name its type and provide every declared field exactly
once; source field order does not affect its type. `value.field` projects a
field, and `{ value | field = expression, ... }` produces a new value of the
same nominal type. Update expressions are evaluated left to right and the
original value is evaluated once. A projection or update receiver must already
have a known nominal record type; field labels do not drive structural type
inference.

A nominal record pattern names its type and any fields to inspect:

```fango
case line of
    IO.Line { text = "", ending = ending } -> ending
    IO.Line { text = text } -> text
```

Fields are keyed and may be reordered. Omitted fields are implicit wildcards,
so `IO.Line {}` is irrefutable. Duplicate and unknown fields are rejected, and
matching requires the field schema exposed by `Type(..)`.

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
pinned values, and `_`. `^expected` compares with an existing local, top-level,
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
- it may not reach a Go sidecar or a bundled native that observes
  process-global state, which today means `Random` (`COMPILE-TIME NATIVE`);
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

`fango repl` evaluates expressions, installs
value/function/type/effect/class/instance/deriver declarations, and accepts
multiline layout-sensitive input. Definitions echo
their inferred types; expressions print a value and type. Errors do not end the
session. Redefinition is allowed at the prompt, while existing memoized values
and closures retain earlier bindings.
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

`:load`, `:reload`, cancellation, and interactive history are not implemented.
