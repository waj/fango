# Text and numeric helpers

Unicode text operations and integer division/modulus helpers.

[Reference index](../reference.md). Sources: [String](../../stdlib/String.fango), [Basics](../../stdlib/Basics.fango).

## String

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
span : (Char ->{e} Bool) -> String ->{e} (String, String)
split : String -> String -> List String
trim : String -> String
padLeft : Int -> Char -> String -> String
padRight : Int -> Char -> String -> String
words : String -> List String
toInt : String -> Maybe Int
toFloat : String -> Maybe Float
```

`length` counts Unicode scalars and `byteLength` counts UTF-8 bytes. `slice`
uses clamped half-open scalar indices and returns `""` when its end is not
greater than its start. `startsWith prefix text` tests an exact prefix and
`contains needle text` tests for an occurrence anywhere, with the empty needle
found in every string; `uncons` returns the first scalar and remaining string, or `Nothing` for the
empty string. `fromChar` makes the corresponding one-scalar string.
`String.span keep text` returns the longest prefix whose scalars satisfy `keep`
and the remaining suffix. It calls `keep` once per scalar from left to right,
including the first rejected scalar, then stops; callback effects propagate.
For example, `String.span (\c -> c /= ' ') "hello world"` returns
`("hello", " world")`. Empty input returns `("", "")` without calling `keep`.
`words` splits on ASCII space, tab, LF, CR, vertical tab, and form feed.
`toInt` parses an optional `+`/`-` sign followed by base-10 digits. An empty digit
sequence, any other character, and values outside the signed 64-bit range all
produce `Nothing`; `String.toInt "007"` is `Just 7` and
`String.toInt "-9223372036854775808"` parses the most negative Int.
`toFloat` accepts an optional sign, one or more decimal digits, an optional
fraction with digits on both sides of `.`, and an optional decimal exponent.
It consumes the complete string: whitespace, `NaN`, infinities, hexadecimal
floats, `.5`, and `1.` produce `Nothing`. Finite values representable as a
64-bit Float, including signed zero and subnormal values, produce `Just`;
overflow and nonzero values that underflow to zero produce `Nothing`.

`split separator text` cuts at every occurrence, so n occurrences give n + 1
pieces and adjacent separators give empty ones: `String.split "," "a,,b"` is
`["a", "", "b"]` and `String.split "," ""` is `[""]`. An empty separator
yields the text unchanged as a single piece. `trim` removes leading and
trailing bytes from the same ASCII whitespace set `words` splits on, so an
all-whitespace string trims to `""`. `padLeft` and `padRight` measure width
in Unicode scalars, like `length`, and return a string that is already that
wide unchanged; a zero or negative width never truncates.

## Basics: integer arithmetic

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
`quotientBy d x * d + remainderBy d x` recovers `x` for nonzero `d`.
There is no floored division to pair with `modBy` yet. A zero modulus or
divisor crashes the program in both backends.
