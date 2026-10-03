# Regular expressions

`Regex` wraps Go's [`regexp`](https://pkg.go.dev/regexp) engine. It uses RE2
syntax, with Unicode matching and leftmost-first search. Lookaround and
backreferences are unsupported. Matching takes linear time in input size for a
fixed pattern. Inputs and outputs are valid UTF-8 `String` values.

[Reference index](../reference.md). [Literal syntax](syntax.md#regular-expression-literals).

## Compilation

`Regex` is opaque and immutable. Its constructor is private, and it has no
`Eq` or `Show` instance. Literals need no import; named operations require
`import Regex`.

```fango
import Regex
import Result exposing (Result(..))

main =
    print (Regex.matches /(?i)hello/ "Hello")
    case Regex.compile "[a-z]+" of
        Ok regex -> print (Regex.matches regex "abc")
        Err error -> print (Regex.message error)
```

| Operation | Type |
| --- | --- |
| compile | `String -> Result Error Regex` |
| pattern | `Regex -> String` |
| message | `Error -> String` |
| escape | `String -> String` |

`compile` is pure and compiles on each call. Invalid patterns return
`Err (InvalidPattern reason)`; `Error(..)` is public and derives `Eq` and
`Show`. `message` returns that reason, whose exact wording comes from the Go
engine. `pattern` returns the compiled pattern without literal delimiters or
Fango delimiter escaping. `escape` quotes regex metacharacters so arbitrary
text can be included as a literal portion of a dynamically built pattern.

Literal patterns are checked by the compiler. Compiled programs initialize
them once at startup; the interpreter initializes them on first evaluation
and shares them within its worker. Matching never recompiles a literal.
This does not serialize a compiled regex machine into the executable.

## Matching and captures

| Operation | Type |
| --- | --- |
| matches | `Regex -> String -> Bool` |
| find | `Regex -> String -> Maybe Match` |
| findAll | `Regex -> String -> List Match` |

These operations search anywhere in the input; use anchors for a whole-input
match. `find` returns `Nothing` when no match exists, including when the input
is empty and the pattern cannot match empty text. `findAll` returns all
successive non-overlapping matches in input order. Empty matches adjacent to a
preceding match are omitted, following Go's `FindAllStringSubmatchIndex` rules.

The module exposes three nominal records, including their fields, construction,
and matching. They derive `Eq` and `Show`:

```fango
type Span = { text : String, start : Int, end : Int }
type Capture = { name : Maybe String, value : Maybe Span }
type Match = { text : String, start : Int, end : Int, captures : List Capture }
```

All positions are zero-based, half-open Unicode scalar offsets into the original
input, matching `String.slice`. They are neither byte nor grapheme offsets.
`Match` describes the whole match. Its captures list excludes the whole match
and follows numbered-group order, starting with group 1. A named group carries
`Just name`, while an unnamed group carries `Nothing`. An unmatched optional
group has `value = Nothing`; a group matching empty text has `Just` a span with
empty text and equal endpoints.

## Replacement and splitting

| Operation | Type |
| --- | --- |
| replaceAll | `Regex -> String -> String -> String` |
| replaceAllLiteral | `Regex -> String -> String -> String` |
| split | `Regex -> String -> List String` |

Replacement arguments are regex, replacement, then input. Both replacement
operations replace all matches. `replaceAll` expands Go templates: `$1` and
`${name}` refer to captures, `$$` produces a dollar sign, and missing groups
expand to empty text. Names consume their longest spelling, so `${1}suffix`
is needed to append text to group 1. `replaceAllLiteral` inserts replacement
text verbatim, without interpreting dollar signs.

`split` returns all pieces separated by regex matches, following Go's
`Regexp.Split` with an unlimited result count. Callback replacement, byte-input
operations, and mutable regexp configuration are not provided.
