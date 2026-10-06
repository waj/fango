# Syntax, literals, and operators

Lexical forms, layout, strict sequencing, operators, and fixity.

[Reference index](../reference.md).

Top-level declarations begin in column 1. Functions with parameters have
module-wide visibility; ordinary values and local bindings are source-ordered
(see [functions](functions.md#visibility-and-recursion)). Tabs are rejected; indent with spaces. `--`
starts a line comment, and `{- ... -}` comments may nest. `{-#` opens a
pragma rather than a comment, so a block comment whose first character is `#`
must be written `{- #`.

`#[` opens a [typed attribute group](metaprogramming.md#attributes), closed by
`]`. Its contents use ordinary expression tokens; `#` elsewhere remains an
operator character.

Lowercase names identify values, parameters, type variables, operations, and
effect-row tails. Uppercase names identify types, effects, and constructors.
Values cannot be shadowed, including by parameters, patterns, or local
bindings. Type names and constructor names are separate namespaces.

## Blocks and sequencing

Indented declaration bodies are blocks. The first item sets the block's
indentation ceiling. Later items may start further left, but must remain deeper
than the enclosing layout column; a line further right than the first item
continues the preceding item. A block ends in exactly one result expression:

```fango
hypotenuse =
    x = 3.0
    y = 4.0
    x * x + y * y
```

The same outdent rule applies to indented effect signatures, class
signatures and defaults, and instance and deriver methods. The formatter aligns all items in each group.
If a block ends after a binding, parsing and formatting still succeed; checking
reports `BLOCK RESULT` because the block has no expression to return.

Bindings are eager and sequential. Unit-valued expression statements may be
placed before the final result, which is how effectful work is sequenced.
There is no `let ... in` expression.

A closing parenthesis may align with the indentation of the line containing
its opening parenthesis, even when an indented body ends immediately before it.
Several closing parentheses may share that line.
An expression inside grouping parentheses has a fresh layout boundary until
the matching `)`.
Braces give a lambda body a fresh layout boundary. Its body may begin at any
column on the next line, including left of the surrounding block. The closing
brace ends that body. Later items inside the body may outdent to the
surrounding block's column and still belong to the lambda; the first body item
remains their indentation ceiling. A statement after `}` at the surrounding
block's column belongs to that block.

A call answering anything other than Unit is not a statement. A block says
it wants the effects and not the answer by binding the result to a wildcard:
`_ = reader.skip 4` (see [destructuring bindings](types.md#destructuring-bindings)).
Outside a block, `ignore : a -> ()`, declared in `Basics` and exposed by
`Prelude`, does the same: `ignore (reader.skip 4)`. Its argument is evaluated
before the call, like every argument.

The same block may be written inline with `;` between its items. This works in
every statement-bearing body: after `=`, `->`, `then`, `else`, and `handle`,
including case branches and handler clauses:

```fango
incrementAfterPrinting = { a b -> print a; b + 1 }
withLocal x = y = x + 1; print y; y * 2
```

Inline blocks admit the same bindings, local functions, annotations,
destructuring, and Unit statements as indented blocks. The last item is always
the result, so a leading, doubled, or trailing semicolon is an error; `body;`
does not imply `()`. A sequence may wrap onto later lines while it remains
inside the ordinary layout boundary.

A nested body owns the separators it reaches. Thus
`if condition then a else b; c` means
`if condition then a else (b; c)`. Parenthesize the completed nested expression
to continue the surrounding body instead: `(if condition then a else b); c`.
A semicolon after a local binding RHS ends that binding item, as in the
`withLocal` example.

### `use` items

A block item `use head` applies `head` to the rest of the block, as a Unit
callback, and makes that application the block's result. `use patterns <- head`
passes a callback taking those parameters instead:

```fango
fetchStatus () =
    use Fail.attempt
    use Client.run
    use reply <- Client.send (Client.request "GET" "https://example.com/")
    reply.status
```

is `Fail.attempt { Client.run { Client.send (Client.request "GET" "https://example.com/") { reply -> reply.status } } }`.
Consecutive items nest, so the first `use` is outermost. Binder patterns are
lambda parameters: a constructor pattern with arguments needs parentheses, as
in `use (Just value) <- lookup`. Because the expansion is an ordinary named
call, a [scoped runner](functions.md#scoped-callbacks) may head a `use` when
it is applied to all of its other parameters.

`use` preserves the runner's result rather than discarding it. A Unit-returning
runner such as [`Async.run`](../../stdlib/Async.fango) can therefore head `main()`:

```fango
import Async

main() =
    use Async.run
    task = Async.spawn { 42 }
    print (Async.await task)
```

The callback reaches the end of the enclosing block, so the block, not the
function, delimits a `use`. A binding's indented right-hand side, a branch, or
a case arm ends it early:

```fango
main() =
    status =
        use Client.run
        Client.getText "https://example.com/status"
    print status
```

Items before a `use` stay in the outer block. A `use` needs at least one item
after it. In an inline block it is followed by `;`: `{ use x <- pair 1; x }`.
`use` is a reserved word. [Handler state](effects.md#stateful-handlers) is
written with `with`, which stays an ordinary name elsewhere.

## Conditionals

`if condition then a else b` requires a Bool condition and equal branch types.
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

## Values and operators

Backticks delimit [expression quotations](metaprogramming.md#quotes-and-splices);
their contents are Fango code rather than string text.

Built-in value types are `Int`, `Float`, `String`, `Char`, `Bool`, and Unit `()`.
Integers are signed 64-bit decimal literals. Floats include `1.25`, `1e3`, and
`1.0e-2`; `.5` and `1.` are not float literals. Strings are single-line,
double-quoted values with `\\`, `\"`, `\n`, `\t`, `\r`, and `\#{` escapes. A Char
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

### String interpolation

`"Hello, #{name}"` inserts an expression's output text into a string using
[`Display`](classes.md#standard-classes). Each hole is an ordinary expression
in the surrounding scope. Nested strings and interpolations, records, lambdas,
and conditionals work normally; an explicit block can be called as
`"#{{ x = 2; x * 3 }()}"`. The result is always `String`.

Holes evaluate exactly once, left to right, and each value is rendered before
the next hole is evaluated. Their effects propagate to the enclosing expression;
an abort prevents later holes from running. Polymorphic holes require
`Display a`, and values without an instance report `MISSING INSTANCE` at the
hole. Rendering uses `Basics.displayTo`, regardless of local names such as
`display`, `displayTo`, or `Text`.

The string and every hole must stay on one physical line. `\#{` inserts the
literal characters `#{`; a preceding escaped backslash, as in `\\#{value}`,
still leaves an active hole. An empty hole reports `EMPTY INTERPOLATION`, a
missing closing brace reports `UNCLOSED INTERPOLATION`, and a newline in a
hole reports `MULTILINE INTERPOLATION`. String patterns and native templates
must be plain strings; interpolation there reports `INTERPOLATED PATTERN` or
`INTERPOLATED NATIVE TEMPLATE`. Escape literal interpolation markers in those
contexts too. Char and regex literals do not interpolate.

Assembly uses [`Text.Builder`](../../stdlib/Text/Builder.fango). Scalar
renderers append directly; the blanket `Show` fallback and nested interpolation
results can still produce intermediate strings. The final builder text is
copied once.

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
cannot be declared. Other plausible characters are deliberately excluded: `.`
is field access, module qualification, and `..`; `$` belongs to the splice
opener `$(`; braces delimit lambdas and records; and `,` `;` `(` `)` are
punctuation. A backslash outside a string or character literal is rejected.
So `(.)`, `($)`, and `(<$>)` are unavailable, while `(<+>)`, `(|>)`, `(>>=)`,
and `(:::)` are all ordinary names.

`@` immediately followed by an uppercase type name or `(` begins a
[type witness](#type-witnesses) instead of an operator. Other `@` runs retain
ordinary operator parsing, so `x @ y` is an operator application.

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

### Type witnesses

A type witness is a value of the singleton
[`Basics.Type a`](../../stdlib/Basics.fango) that selects a type a call could
not otherwise determine, as in `Json.parse @Person text`. The type after `@`
must be closed and fully applied, as in `@Person` and `@(List Person)`.

## Regular expression literals

`/pattern/` constructs an opaque [`Regex`](../../stdlib/Regex.fango) value. A completed
literal with an invalid Go regex reports `INVALID REGEX` during checking.

An opening slash is recognized at the beginning of input, after ASCII
whitespace, or immediately after `(`, `[`, `{`, `,`, `;`, or a quotation
backtick. It must touch a non-whitespace pattern character, and an unescaped
closing slash must exist on the same line. `//` is the empty pattern. If no
closing slash exists, the lexer falls back to ordinary operators.

```fango
Regex.matches /abc/ text  -- a literal argument
x/y/z                    -- division
x / y / z                -- division
x /y                     -- division: no closing slash
```

A slash immediately attached to the preceding value remains an operator.
Parenthesized slash operator names without a second slash, such as `(/)` and
`(/=)`, and slash-led operator runs without a second slash followed by whitespace
remain operators.
Otherwise a slash sequence satisfying the literal rule takes precedence over
custom operators: `x /y/ z` contains a regex literal. Use spaces around division
operators to make their meaning explicit.

Regex backslashes are not string escapes. `\d`, `\n`, and `\\` reach the regex
engine unchanged; `\/` represents a literal slash, including inside character
classes. A slash after an even number of consecutive backslashes closes the
literal, while one after an odd number is escaped. Patterns beginning with
whitespace can spell it with `[ ]` or `\x20`. Flags use Go's inline syntax,
such as `/(?i)abc/`, rather than a suffix after the closing slash. Literals are
single-line expressions without interpolation and are not case patterns.
