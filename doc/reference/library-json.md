# JSON

`Json` parses and emits JSON in Fango. It uses the bundled `Bytes`, `Reader`,
and `Writer` primitives through [text adapters](library-text-io.md), but has no JSON native. Parsing from a `Reader` consumes
tokens as needed; a typed decoder does not build an intermediate `Json.Value`.
Custom decoders and token-level parsing use [`Json.Pull`](library-json-pull.md).

[Reference index](../reference.md). Source: [Json](../../stdlib/Json.fango).

## Typed values

`Json.Decode` and `Json.Encode` have bundled instances for `String`, `Int`,
`Float`, `Char`, `Bool`, `()`, `List a`, and `Maybe a`. `Json.Value` also has both
instances. `Char` accepts exactly one decoded Unicode character. An `Int`
decoder accepts only a JSON integer within Fango's integer range; a `Float`
decoder accepts JSON numbers that fit in a finite Float. A nonfinite Float
cannot be encoded.

```fango
import Json exposing (Decode, Encode(..))

type Person = { name : String, age : Int } deriving (Encode, Decode)

person = Json.parse @Person "{\"name\":\"Ada\",\"age\":4}"
json = Json.stringify (Person { name = "Ada", age = 4 })
```

`Json` exposes `Decode` without its method, which is enough to derive it and
to write `Decode a =>` constraints; a handwritten instance imports
`Decode(..)` from [`Json.Pull`](library-json-pull.md#custom-decoders).

`parse : Decode a => Type a -> String -> Result Json.Error a` takes a
[type witness](#type-witnesses) because its result alone does not determine the decoder. `parseBytes`
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

Record fields can carry [typed attributes](metaprogramming.md#attributes):

```fango
type Settings =
    { name : String    #[Json.Key "full_name"]
    , count : Int      #[Json.Default `7`]
    , secret : String  #[Json.Skip, Json.Default `"local"`]
    }
    deriving (Encode, Decode)
```

`Json.FieldOption = Key String | Skip | Default Meta.Code` is an ordinary
library type. `Key` changes the wire key, `Default` supplies code for a value
when the key is absent, and `Skip` omits the field while encoding and ignores it
while decoding. `Skip` requires `Default` and cannot combine with `Key`. Each
option may occur once per field, and keys must be unique among emitted fields.
Defaults preserve declaration-site resolution and are checked against the
field type when the decoder is generated. Their expressions run only when the
decoder needs the fallback.

Both JSON derivers validate these rules in Fango. `FieldOption` attributes on
types, constructors, or positional union fields are rejected when consumed.
Failures point to the offending attribute with `COMPILE-TIME FAILURE`; no JSON
validation runs for a declaration that derives neither JSON class. The old
`{-# json ... #-}` pragmas are no longer supported.

## Text adapters and custom encoders

Byte-reader/writer entry points select UTF-8. `readText` and `writeText` accept
`Text.Reader.Reader e` and `Text.Writer.Writer e` respectively, with the same
type-witness, result, and source/sink effect contracts as `read` and `write`.
Their caller selects the text adapter's encoding;
Latin-1 text adapters, for example, can decode Latin-1 JSON input or report
unrepresentable output characters. Encoding errors become `Json.Error` values;
unrelated source/sink effects propagate.
`write` and `withWriter` leave flushing to the byte writer's owner.

`Encode` has one method, `encodeValue : a ->{Json.Emit, Fail Json.Error} ()`.
A handwritten instance writes raw JSON text with `Json.emit` or delegates to
`encodeValue` for its parts. `Json.withWriter writer { ... }` and
`Json.withTextWriter` install the `Emit` handler over a byte or text writer.

## Value tree

`Json.Value` is the generic document tree: `Null`, `Boolean`, `Numeric`,
`Text`, `Array`, and `Object`, with object members as an ordered key/value
list. It is opted into by type, like any decoded value:
`Json.parse @Json.Value text`, `Json.parseBytes @Json.Value bytes`,
`Json.read @Json.Value reader`, and `Json.stringify value`.

`Json.Numeric` holds a `Json.Number` with the original number lexeme, so a
value tree can round-trip numbers without Float conversion. `Json.Number` is
opaque; `Json.number` validates a number lexeme and `Json.numberText`
retrieves it.

## Errors

`Json.Error` contains `message`, zero-based byte `offset`, one-based `line`
and byte-based `column`, and a slash-separated `path` for failures inside decoded
records, lists, or generic value containers. Emission errors have no input
position. Parsing rejects invalid UTF-8, malformed escapes, trailing input,
and nesting deeper than 256 arrays or objects. Malformed and incomplete encoded
input is reported at the first offending sequence, without consuming that
sequence. Paths follow the active decoder context; errors found while scanning
a lookahead token can precede entry into a field or element path. Typed array
decoding enters the element's index before scanning its value, so lexical errors
in that value include the index.

## Type witnesses

`Basics.Type a` is a singleton used to select an otherwise ambiguous type.
`@Person` and `@(List Person)` are witness expressions; the type after `@`
must be closed and fully applied. `@` remains an ordinary operator when it
is not immediately followed by an uppercase letter or `(`.
