# JSON encoding

Json.Encode deriving and the parseString decoder helper.

[Reference index](../reference.md). Sources: [Json](../../stdlib/Json.fango).

## Json

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
