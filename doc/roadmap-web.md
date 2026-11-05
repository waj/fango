# Roadmap: typed web APIs

`Web` is an official library for HTTP APIs and simple web apps, kept in
[`libs/web`](../libs/web/Web.fango) until it moves to a repository of its own.
A contract is a record of functions indexed by an effect row, whose fields
carry route attributes. The typed endpoint values derived from it are the
shared foundation for the server dispatcher, the generated client, links, and
API documentation.

The buffered JSON slice is implemented: contracts, binding, endpoints, the
dispatcher, the client, links, and strict test values. Its API reference and
rules are the library's own source comments, which `fango doc --lib web`
extracts. This page keeps the remaining stages. The library builds on the
[HTTP server](../stdlib/Http/Server.fango), the
[HTTP client](../stdlib/Http/Client.fango), and [JSON](../stdlib/Json.fango).
Stage IDs follow the [repository milestone rules](../AGENTS.md).

## Direction

One shared description owns parameter binding, codecs, statuses, headers, and
schemas; generators must not keep independent result-type tables. Codec/schema
agreement and compatibility between separately deployed versions still need
verification. Forms, redirects, and extensible responses remain later work.

The server runs each operation inside a runner the application supplies, which
handles the implementations' own effects once per call. Only typed values cross
the runner, never the request or response. A request's reader row is the
server's, and Fango has no effect subsumption, so an implementation that
performed effects handled around the whole dispatcher could not share the
request's row. That is also why there is no `Web.Server.respond` or `run`
wrapping a handler that may fail: `Web.Server.recover` is the runner that turns
an operation's `Fail Web.Server.Error` into an error response.

## Language prerequisites

None remains for the next stages. W1, W2, W3, and W7 were retired with the
effect-based contract, and W14 moved to the
[effects roadmap](roadmap-effects.md#w14-partial-handlers-and-super). Do not
reuse those IDs.

### W15 Row-indexed types in `typeOf`

DONE: [type reflection](reference/metaprogramming.md#type-reflection) accepts
a type applied to a closed row.

### W4 Open effect rows in class methods

Deferred general class-system work: method-local row variables, per-method
quantification, dictionary representation, and deriver support. The endpoint
foundation needs no such feature; its buffered codecs operate on plain data.
This alone does not solve W12: a method `respond : a -> Http.Server.Response e`
quantified over arbitrary `e` cannot require `FileSystem` merely because a
particular response streams a file. Settle that relationship against concrete
examples before treating this milestone as sufficient.

### W5 Doc comments in reflection

`Meta.docOf : Code -> Maybe String`, and doc text on `TypeInfo`, `Ctor`, and
`Field`, using the comments [`fango doc`](reference/commands.md#api-documentation)
already extracts. Keep existing visibility rules. OpenAPI works without
descriptions until this lands.

### W6 Official library home

DONE: [optional libraries](reference/modules.md#optional-libraries) live in
the library root's `libs/` and are enabled with `--lib`. Extracting one to a
repository of its own, with a search path, waits for a package manager.

## Library stages

### W8 Contract, templates, and binding

DONE: [`Web`](../libs/web/Web.fango).

### W9 Server dispatcher

DONE: [`Web.Server`](../libs/web/Web/Server.fango), served by the
[shop example](../examples/shop.fango).

### W10 Client runner and links

DONE: [`Web.Client`](../libs/web/Web/Client.fango),
[`Web.link`](../libs/web/Web.fango), and
[`Web.Testing`](../libs/web/Web/Testing.fango).

### W11 OpenAPI

`Web.Docs.openApi : Api -> Info -> Code`, with `type Info = { title : String,
version : String }`, generates a pure expression producing an OpenAPI 3.1
string. Route metadata is resolved at compile time; schema methods are ordinary
typed calls in generated code. Operation descriptions use W5 when available, and
`#[Web.Docs.Tag "name"]` on a contract field groups operations. Raw routes are
identified as untyped.

`Web.Schema` supplies explicit JSON schema evidence, with separate input and
output views when codec defaults or omissions make them differ. Its methods
mention the class parameter through a type witness; the schema data is ordinary
data with named references for recursive/shared types. A deriver uses the same
field-option interpretation as JSON: `Json.Key` renames and `Json.Skip` omits;
input fields without `Json.Default` are required, including `Maybe` fields;
`Maybe` makes a value nullable; output requiredness follows what the encoder
emits; nullary constructors are strings and other constructors single-key
objects; lists are arrays and `Json.Value` is unconstrained. The library does
not infer whether a selected codec is handwritten. Custom codecs require
matching explicit schema evidence; deriving a schema is an explicit promise
that the codecs follow those rules.

Cover custom/opaque codecs, nullable versus omittable fields, input/output
differences, recursive references, error status sets, and repeated mounts.
Generated documents must validate against the OpenAPI 3.1 schema in an offline
test. Schema/codec conformance fixtures are required; structural reflection
alone is not proof of codec behavior.

### W12 Extensible responses

Deferred beyond the buffered JSON slice. Extend the shared response description
with status-bearing wrappers (`Created` and others with concrete consumers),
text, HTML, files, CSV, and custom encodings, each with a matching client
decoder and documentation. Buffered customization can precede streaming.

A `Respond` class is a candidate, not a settled interface. Before choosing it,
type-check a file response, a response carrying effects in its value, and a
request-backed streaming response without escaping their scopes. W4 alone is
insufficient; the response's required effects must be related to the method's
row. Raw routes remain the supported way to stream until that design is
resolved. Header composition must preserve repeated fields such as
`Set-Cookie`.

### W13 Web app support

Deferred: form decoding/encoding, explicit text/bytes body modes, cookies
(a `Cookie "name"` source, `a` or `Maybe a` through `Param`), `SetCookie`,
`Html`, `Text`, `Redirect`, POST-redirect-GET helpers, and static files through
raw routes. Form derivation uses field names and `Param`, with `Maybe`,
repeated `List`, and checkbox rules specified explicitly. `Redirect` must encode
303 plus `Location` and decode that same value without following it. The `Html`
wrapper remains a raw string; escaping builders and templates are separate
decisions. Content negotiation waits for a consumer.

### Dependencies

| Stage | Depends on |
| --- | --- |
| W4 Open effect rows in class methods | —; deferred |
| W5 Doc comments in reflection | —; optional descriptions |
| W11 OpenAPI | W5 for descriptions |
| W12 Extensible responses | W4 only if the chosen class design requires it, plus a resolved effect-row relationship |
| W13 Web app support | relevant W12 response descriptions |

## Open decisions

- **Mount captures in every request.** A mount capture such as `org` must be a
  field of every mounted request record. A mount-level record merged into each
  request, or a second parameter on the contract's fields, would remove the
  repetition; decide after the first multi-operation application.
- **URL-only request projection.** `Web.link` takes the whole request, so a
  link to a `POST` needs a body value. Choose between a generated `Link` record
  per endpoint, a request builder that leaves body/header fields unset, or
  keeping the whole request; a checked link helper or validated path types
  remain open before promising every generated URL is accepted.
- **Framework errors under declared statuses.** A dispatcher's own 400 or 404
  carries `{"message": …}`, so a client whose endpoint declares that status for
  a domain error reads it as an invalid response rather than as unexpected.
  Marking framework answers would let the client tell them apart.
- **Ambient clients.** The library hands out a record. Whether to generate a
  one-operation effect wrapper, or leave it to applications, waits for a
  second consumer.
- **Parameter conventions.** Resolve duplicate header/cookie handling,
  empty/rest captures, flags, and form checkboxes before enabling those forms.
- **Typed error envelope.** Domain errors use their bare JSON; problem-details
  envelopes are a later policy choice.
- **Content negotiation.** A response has one encoding. `Accept`-driven
  JSON/HTML selection waits for an application.
- **Process integration.** Interrupt handling needs a separate signal
  capability and must preserve server shutdown.
