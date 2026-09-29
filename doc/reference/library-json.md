# JSON

`Json` parses and emits JSON in Fango. It uses the bundled `Bytes`, `Reader`,
and `Writer` primitives but has no JSON native. Parsing from a `Reader` consumes
tokens as needed; a typed decoder does not build an intermediate `Json.Value`.

[Reference index](../reference.md). Source: [Json](../../stdlib/Json.fango).

## Typed values

`Json.Decode` and `Json.Encode` have bundled instances for `String`, `Int`,
`Float`, `Char`, `Bool`, `()`, `List a`, and `Maybe a`. `Json.Value` also has both
instances. `Char` accepts exactly one decoded Unicode character. An `Int`
decoder accepts only a JSON integer within Fango's integer range; a `Float`
decoder accepts JSON numbers that fit in a finite Float. A nonfinite Float
cannot be encoded.

```fango
import Json exposing (Decode(..), Encode(..))

type Person = { name : String, age : Int } deriving (Encode, Decode)

person = Json.parse @Person "{\"name\":\"Ada\",\"age\":4}"
json = Json.stringify (Person { name = "Ada", age = 4 })
```

`parse : Decode a => Type a -> String -> Result Json.Error a` takes a type
witness because its result alone does not determine the decoder. `parseBytes`
accepts `Bytes`; `read` accepts a `Reader e` and has the reader's effect row.
`write : Encode a => Writer e -> a ->{e} Result Json.Error ()` emits to a
writer. `stringify` collects output in a `String`. The writer may already
contain a prefix when encoding fails.

Records encode as objects in declaration order. Decoding accepts keys in any
order and skips unknown keys. A duplicate declared key or a missing required
field is an error. Nullary union constructors encode as strings, such as
`"Red"`; a constructor with fields encodes as a single-key object whose value
is an array, such as `{"RGB":[1,2,3]}`. Unit and `Nothing` use `null`; `Just`
uses its payload's representation. Nested `Maybe` cannot distinguish outer
`Nothing` from an inner `Nothing` in this wire format.

Record fields can carry JSON pragmas before the field name:

```fango
type Settings =
    { {-# json key "full_name" #-} name : String
    , {-# json default 7 #-} count : Int
    , {-# json skip #-} {-# json default "local" #-} secret : String
    } deriving (Encode, Decode)
```

`key` changes the wire key, `default` supplies a value when the key is absent,
and `skip` omits the field while encoding and ignores it while decoding.
`skip` requires `default`. A default expression is resolved in the declaring
module. Keys must be unique among emitted fields.

## Pull parser and value tree

`withPull reader { ... }` installs the `Json.Pull` effect. `next()` consumes a
token, `peek()` reads ahead without consuming it, and `at()` returns the
current source position. `beginArray`, `nextElement`, `beginObject`, `nextKey`,
and `skipValue` help decoders consume a container. `withPath segment { ... }`
adds a segment to errors raised while a custom decoder handles a nested value.
`skipValue` validates and
discards a value without building a tree. `readValue()` builds the generic
`Json.Value` tree from the current token. `parseValue`, `parseBytesValue`, and
`stringifyValue` are the whole-input tree helpers. `Json.Numeric` holds a
`Json.Number` with the original number lexeme, so a value tree can round-trip
numbers without Float conversion. `Json.Number` is opaque to callers;
`Json.number` validates a number lexeme and `Json.numberText` retrieves it.

`Json.Error` contains `message`, zero-based byte `offset`, one-based `line`
and `column`, and a slash-separated `path` for failures inside decoded
records, lists, or generic value containers. Emission errors have no input
position. Parsing rejects invalid UTF-8, malformed escapes, trailing input,
and nesting deeper than 256 arrays or objects.

## Type witnesses

`Basics.Type a` is a singleton used to select an otherwise ambiguous type.
`@Person` and `@(List Person)` are witness expressions; the type after `@`
must be closed and fully applied. `@` remains an ordinary operator when it
is not immediately followed by an uppercase letter or `(`.
