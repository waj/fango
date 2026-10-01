# JSON

`Json` parses and emits JSON in Fango. It uses the bundled `Bytes`, `Reader`,
and `Writer` primitives through [text adapters](library-text-io.md), but has no JSON native. Parsing from a `Reader` consumes
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

`Decode` has two methods:

```fango
class Decode a
    decodeValue : () ->{Json.Pull, Fail Json.Error} a
    scanValue : Json.Scan -> Maybe (a, Json.Scan)
```

`decodeValue` is the streaming decoder. `scanValue` is an optional pure buffered
decoder used by enclosing generated records and lists. It may return `Nothing`
for any input, including valid input; this consumes nothing and selects the
streaming decoder. `Just (value, rest)` must represent the same complete JSON
value as `decodeValue`, with the position immediately after it and the same
remaining nesting allowance. It cannot refill or execute consumer effects.
`Json.Scan` is opaque; custom implementations can compose existing scan methods
and transform their results, or decline. Manual instances must provide both
methods. To retain an existing decoder, add `scanValue _ = Nothing`.

Generated record scans optimize required fields in declaration order using
the encoder's key spelling. Other orders, extra keys, alternative escapes,
buffer boundaries, and type or syntax failures use the streaming decoder with
the same errors and consumption. Defaults and skipped fields use streaming
decoding so their expressions execute only when required.

## Pull parser and value tree

Byte-reader/writer entry points select UTF-8. `readText` and `writeText` accept
`Text.Reader.Reader e` and `Text.Writer.Writer e` respectively, with the same
type-witness, result, and source/sink effect contracts as `read` and `write`.
`withTextPull` and `withTextWriter` are the corresponding handlers for custom
decoders and encoders. Their caller selects the text adapter's encoding;
Latin-1 text adapters, for example, can decode Latin-1 JSON input or report
unrepresentable output characters. Encoding errors become `Json.Error` values;
unrelated source/sink effects propagate.
`write` and `withWriter` leave flushing to the byte writer's owner.

`withPull reader { ... }` installs the `Json.Pull` effect. `next()` consumes a
token, `peek()` reads ahead without consuming it, and `at()` returns the
current source position and publishes buffered consumption. `beginArray`, `nextElement`, `beginObject`, `nextKey`,
and `skipValue` help decoders consume a container. `nextElement first` consumes
an array separator or closing bracket, returning whether another value follows;
it leaves that value unscanned for the decoder. `nextKey first` consumes an
object separator, key, and colon, returning the key or `Nothing` at the end.
Both honor an existing token from `peek()`. `withPath segment { ... }`
adds a key segment, and `withIndex index { ... }` an element index, to errors
raised while a custom decoder handles a nested value.
`skipValue` validates and
discards a value without building a tree. When `withPull` or `withTextPull`
returns, normally or with `Err`, the reader is positioned after the last token
the parser scanned, including a lookahead token obtained by `peek`. The same
reconciliation occurs when an unrelated effect exits the scope. Within the
scope, the underlying text and byte reader positions can lag behind the JSON
cursor until a refill or `at()`; advance them only after leaving the pull scope.

`Json.arrayElement` and `Json.objectKey` implement the corresponding container
helpers. `Json.decodedString()`, `Json.decodedInt()`, and `Json.decodedFloat()`
consume a scalar and return `Result String a`: a type or conversion mismatch
returns its diagnostic message after consuming the token. Lexical and encoding
failures are handled by `withPull` or `withTextPull` as usual. The primitive
`Decode` instances turn these mismatch messages into `Json.Error` at the
current position and path.

`Json.bufferedScan()` obtains a speculative view of the current Pull cursor,
or `Nothing` when a lookahead token is pending. It consumes nothing.
`Json.acceptScan rest` publishes a successful scan result; use it only with a
complete value derived from that view, while the Pull cursor is otherwise
unchanged. Abandoning a view consumes nothing. Publication advances the JSON
cursor; the reader is reconciled at the usual refill, `at()`, or scope boundary.

`readValue()` builds the generic
`Json.Value` tree from the current token. `parseValue`, `parseBytesValue`, and
`stringifyValue` are the whole-input tree helpers. `Json.Numeric` holds a
`Json.Number` with the original number lexeme, so a value tree can round-trip
numbers without Float conversion. `Json.Number` is opaque to callers;
`Json.number` validates a number lexeme and `Json.numberText` retrieves it.

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
