# Roadmap: typed web APIs

A proposed official library, `Web`, for HTTP APIs and simple web apps. The
contract is a record of functions indexed by an effect row, whose fields carry
route attributes; typed endpoint values derived from it are the shared
foundation for the server dispatcher, the generated client, links, and API
documentation. Nothing here is implemented. Examples use proposed APIs, with
application-specific modules such as `Db` left to the application.

The library builds on the existing [HTTP server](../stdlib/Http/Server.fango),
[HTTP client](../stdlib/Http/Client.fango), and [JSON](../stdlib/Json.fango).
Stage IDs follow the [repository milestone rules](../AGENTS.md).

## Direction

An API is a record type `Api e` whose fields are the operations, each an arrow
`request ->{e} response` with one route attribute. A request is one named
record, or `()`, whose [field attributes](reference/metaprogramming.md#attributes)
bind inputs by name. Everything the library needs is readable today:
record-field attributes, `Meta.info`, `Meta.arrow` for each field's parameter,
result, and row, and the `Meta.match` and `Meta.construct` builders. Row-indexed
records of functions are already a library idiom
([`Text.Writer.Output`](../stdlib/Text/Writer.fango),
[`Http.Wire.ChunkCell`](../stdlib/Http/Wire.fango)).

Compile-time generators normalize the record and its mount into one checked
endpoint description:

- a **server** implementation is an ordinary value of the contract type, so a
  record literal that omits an operation is a type error. A generated
  dispatcher decodes a request, calls the matching field, and encodes its
  result;
- a **client** is a generated value of the same contract type. Each field is
  an ordinary function that performs `Http.Client.Http` and
  `Fail Web.Client.Error` in the caller's context, so `Fail.attempt` around
  one call recovers that call's transport or decoding failure and later calls
  proceed;
- **links and docs** use the same binding and response descriptions;
- a **test** hands the dispatcher a stub record, overrides selected operations
  of a real implementation or client with record update, or connects the
  client to the dispatcher through
  [`Http.Client.mock`](design/http.md#mocking-through-framing).

An effect declaration was the earlier candidate for the contract. A handler
gives the same coverage check as a record literal, but the client could not
share the type: a client implemented as a handler fails through a handler
outside the runner, because clauses run outside their own activation, which
is why the existing HTTP transport
[returns results to caller code](design/http.md#the-client-transport).
Effects would also need operation attributes, operation reflection, and
generated operation calls before anything worked, and stubbing one operation
would need partial handlers. A record needs none of that. An application that
wants ambient access to a client wraps the record in an effect of its own.
A union of route constructors would need a different way to express each
constructor's response type and is not needed.

`Endpoint request response` associates both types without type families or
GADTs. One shared description owns parameter binding, codecs, statuses,
headers, and schemas; generators must not keep independent result-type
tables. Codec/schema agreement and compatibility between separately deployed
versions still need verification.

The first vertical slice is a buffered JSON GET and POST API with typed domain
errors, typed links, and client/server round trips. Forms, redirects, and
extensible responses remain later work. The prototype can live beside an
example before official library distribution is settled.

## Example

The contract module is shared by the server and client:

```fango
module Shop.Users exposing (GetUser(..), User(..), UserError(..), Users(..))

import Json
import Web

type User = { id : Int, name : String }
    deriving (Json.Encode, Json.Decode, Web.Schema)

type UserError =
    #[Web.Status 404] NoSuchUser
    deriving (Json.Encode, Json.Decode, Web.Schema, Web.Failure)

type GetUser =
    { #[Web.Path "org"] org : String
    , #[Web.Path "id"] id : Int
    , #[Web.Query "verbose"] verbose : Maybe Bool
    }

-- Users of one organization. Operations perform `e`.
type Users e =
    {
      -- The user with this ID.
      #[Web.Get "/users/:id"]
      getUser : GetUser ->{e} Result UserError User
    }
```

`GetUser` needs no JSON instance: it describes path and query inputs. A POST
request record can instead contain a `#[Web.Body]` field whose type implements
JSON encoding and decoding. `Web.Schema` is explicit schema evidence for docs,
not a claim that reflection can identify the selected codec's implementation.

A named mount selects the public location. Endpoint names stay explicit source
bindings; expression splices introduce no hidden public declarations:

```fango
module Shop.Api exposing (api, getUserEndpoint, users)

import Shop.Users exposing (GetUser(..), User(..), UserError(..), Users(..))
import Web

users = Web.mount "users" "/orgs/:org" (typeOf (Users {}))
api = Web.api [users]

getUserEndpoint : Web.Endpoint GetUser (Result UserError User)
getUserEndpoint = $(Web.endpoint users "getUser")
```

The server implements the contract as a record and passes it to the generated
dispatcher, one implementation per mount in tree order:

```fango
import Db
import Shop.Api
import Shop.Users exposing (Users(..))
import Web.Server

serveUsers : Users {Db}
serveUsers = { getUser = { request -> Db.findUser request.org request.id } }

dispatch = $(Web.Server.dispatch Shop.Api.api)

app request = Db.run { dispatch serveUsers request }
```

An endpoint can also be attached to a function directly, without a contract
record:

```fango
route = Web.Server.route Shop.Api.getUserEndpoint {
    request -> Db.findUser request.org request.id
}
```

The client is a value of the contract type, generated from the mount:

```fango
import Fail exposing (Fail)
import Http.Client
import Shop.Api
import Shop.Users exposing (GetUser(..), User, UserError, Users(..))
import Web.Client

shop : Users {Http.Client.Http, Fail Web.Client.Error}
shop = $(Web.Client.client Shop.Api.users) "https://shop.example"

fetchUser : String -> Int ->{Http.Client.Http, Fail Web.Client.Error} Result UserError User
fetchUser org id = shop.getUser (GetUser { org = org, id = id, verbose = Nothing })
```

Inside `Http.Client.run`, `Fail.attempt { fetchUser "acme" 7 }` catches that
call's client failure. Its result distinguishes `Ok (Ok user)`,
`Ok (Err NoSuchUser)`, and `Err clientError`. A second call can proceed after
the first fails. `Web.Client.call endpoint base request` is the function each
generated field applies.

Links use the endpoint and its request value; documentation uses the API tree:

```fango
userUrl = Web.link Shop.Api.getUserEndpoint
    (GetUser { org = "acme", id = 7, verbose = Nothing })
-- userUrl == "/orgs/acme/users/7"

apiDocs = $(Web.Docs.openApi Shop.Api.api
    (Web.Docs.Info { title = "Shop", version = "1.0" }))
```

## Specification

### Modules

| Module | Contents |
| --- | --- |
| `Web` | Route/source attributes, named mounts and `Api`, `Endpoint`, `endpoint`, `Param`, `Failure`, `Schema`, and `link`. |
| `Web.Server` | `route`, `dispatch`, `respond`, `run`, and server response errors; `Config` and `defaultConfig` re-exported from `Http.Server`. |
| `Web.Client` | `call`, the generated `client`, and client errors. |
| `Web.Docs` | `openApi`, schema data, and documentation attributes. |
| `Web.Testing` | `strict`, a generated contract value whose operations fail the test. |

Forms and result wrappers belong to the later stages below. Official shipping
outside `stdlib/` is [W6](#w6-official-library-home); a local prototype does not
need a new library search mechanism.

### Contract records

A contract is a record type with exactly one parameter, used as the row of
every field. Every field is an arrow with exactly one route attribute, whose
parameter is a concrete visible record or `()` and whose result is a wire
value. Reject other parameters, a field without an arrow, missing or duplicate
route attributes, curried fields, and rows naming effects beyond the parameter.
Wire fields and results must have supported concrete codecs; function and
resource values are not wire values.

Field comments are the operations' descriptions once [W5](#w5-doc-comments-in-reflection)
lands. Endpoint identity is the selected mount and the field's name within
the contract's compiler identity. The contract module must expose the record
and request schemas (`Users(..)`) for the generators to read them.

### Route attributes

```fango
type Route = Get String | Post String | Put String | Patch String | Delete String
```

The path is relative to a mount. Query bindings live on request fields, not in
the path template. A `Get` also answers `HEAD`, omitting its response body.

```text
path      = "/" | ( "/" segment )+
segment   = literal | ":" name | "*" name
name      = lower ( letter | digit | "_" )*
```

A rest capture `*name` is allowed only as the final segment. Literals exclude
`:`, `*`, `?`, and `#` as template syntax; URL escaping and matching use one
normalization rule in links, clients, and servers. Captures are decoded once.
Encoded `/` and `\` in a segment are rejected with 400, following
[`Http.Server.Route`](../stdlib/Http/Server/Route.fango).

Names are unique across the full mount and operation path. Matching compares
complete paths, preferring literal segments over captures and captures over a
rest capture at the first differing position; a dead-end literal branch must
not hide a matching capture branch. Equal shapes after capture-name erasure
conflict for the same method. Effective methods include generated `HEAD` routes
and raw routes. Path matching and method selection must agree on 404 versus
405 and the `Allow` set.

### Parameter binding

Each request field carries exactly one source attribute:

| Source | Binding and allowed types |
| --- | --- |
| `Path "name"` | The named full-path capture; `a` with `Web.Param a`. |
| `Path "name"` for a rest capture | `String` joined by `/`, or `List String`. |
| `Query "name"` | `a` required, `Maybe a` optional, `List a` repeated; elements implement `Param`. |
| `Header "Name"` | `a` or `Maybe a`, with `Param`; names compare without ASCII case. |
| `Body` | One JSON payload field, with `Json.Decode` for servers and `Json.Encode` for clients. |
| `Cookie "name"` | Deferred to W13; `a` or `Maybe a`, with `Param`. |

All captures must bind exactly once; unknown names, duplicate sources, and
unbound fields are errors. Field order has no binding meaning. Mount captures
bind by the same names as operation captures, with equal types across the
mounted operations. `()` is valid only when there are no bound inputs.
Bodies are allowed on POST, PUT, PATCH, and DELETE, at most one per request.

```fango
class Param a
    parseParam : String -> Result String a
    printParam : a -> String
```

Instances cover `String`, `Int`, finite `Float`, `Bool` as `true`/`false`, and
`Char`. Derivation covers single-field wrappers and nullary unions. Required
values must be present; optional missing values become `Nothing`; missing
repeated queries become `[]`. Duplicate scalar query values are rejected;
repeated query values preserve order. A bare query key has an empty value and
is not an implicit Boolean flag in the first version. `parseParam (printParam
value)` round trips for supported values. Unrepresentable path values are a
client-side validation failure, never a different target.

### Request bodies

The initial `Body` encoding is JSON for every supported type, including
`String`. Content types are `application/json` and application subtypes ending
in `+json`, with media-type parameters parsed separately. A mismatch answers
415; malformed JSON or invalid field values answer 400. Bodies are bounded by
the server's `bodyLimit` and decoded before the operation runs.

Text, bytes, and forms need explicit encoding selection when added; a type must
not silently switch from JSON to text because its representation is `String`.
Streaming and uploads use [raw routes](#raw-routes). JSON field defaults
remain the [codec's contract](../stdlib/Json/Field.fango): a `Maybe` field
accepts `null` but is still required unless it has `#[Json.Default ...]`.

### Results

One response description specifies both encoding and decoding:

| Result | Server response | Client acceptance |
| --- | --- | --- |
| `()` | 204, no body | 204 with no body. |
| JSON `a` | 200, `application/json`, encoded `a` | 200, compatible JSON media type, valid `a`. |
| `Result err a` | `Ok` uses `a`'s description; `Err` uses `failureStatus err` and JSON `err` | The declared success shape or a declared error status with valid `err` whose `failureStatus` matches. |

```fango
class Failure a
    failureStatus : a -> Int
```

`deriving (Web.Failure)` reads a `#[Web.Status n]` on every constructor;
statuses must be 4xx or 5xx. Shared statuses are allowed because JSON identifies
the constructor. The endpoint description also records the declared status
set; a handwritten mapping needs explicit matching status metadata.

Unexpected statuses are client errors. A declared status with the wrong media
type, malformed body, or inconsistent error constructor is an invalid response.
A framework 400/404 response is not a domain error merely because its status
also appears in the contract. Server encoding failures produce 500 before any
response bytes are committed. `Html`, `Text`, `Redirect`, `Created`, and other
status-bearing wrappers are [W12](#w12-extensible-responses)/[W13](#w13-web-app-support)
work; each must supply the complete response description, including a matching
client decoder and documentation.

### API trees and endpoints

Proposed compile-time builders:

```fango
api : List Entry -> Api
mount : String -> String -> TypeRepr -> Entry
root : String -> TypeRepr -> Entry
nest : String -> List Entry -> List Entry
raw : String -> String -> Code -> Entry
endpoint : Entry -> String -> Code
```

The first string of `mount`, `root`, and `raw` is a stable, unique name in the
API tree. `mount` also takes a prefix; `root` uses `/`. The `TypeRepr` is the
contract applied to the pure row, `typeOf (Users {})`, which needs
[W15](#w15-row-indexed-types-in-typeof). `raw` takes a `"METHOD template"` and
a quoted handler. Prefixes cannot contain query or rest captures. `nest`
returns entries with the prefix applied, so an endpoint selects the resulting
mounted entry.

One contract may be mounted several times; each is selected explicitly by its
entry. `endpoint entry "name"` checks the name against the contract's fields
and generates an ordinary `Endpoint request response`; its public binding is
written by the user. Duplicate entry names and conflicting effective routes are
compile-time errors. Joining a prefix and an operation path of `/` drops the
extra slash. `/orgs/acme` and `/orgs/acme/` remain distinct.

`Api` and `Entry` are compile-time-only values holding reflection and code.
Direct server adapters, client calls, and links use runtime `Endpoint` values
without `Code`, `TypeRepr`, or runtime reflection. For the buffered first
version, endpoint functions convert between typed values and ordinary buffered
HTTP data and return validation/codec results; reader/writer effects and HTTP
framing live in the server and client drivers. The internal model's concrete
representation is an implementation decision; consistency across all consumers
is an invariant.

### Raw routes

A raw handler has the existing
`List Http.Server.Route.Param -> Http.Server.Request e ->{e} Http.Server.Response e`
shape. It participates in matching, conflict checking, and 405 answers. It has no
typed endpoint, client, or link; docs identify it as untyped. Its streaming and
resource lifetime remain those of the existing HTTP server.

### Server

```fango
dispatch : Api -> Code
route : Endpoint request response -> (request ->{e} response) -> Http.Server.Route.Route {Http.Protocol, e}
```

`dispatch` generates a function taking one implementation record per mount, in
tree order, then the request. Its row is the union of the implementations'
rows, protocol effects, and any effects raw routes need. Each generated
operation adapter is `Meta.match` over the contract binding the fields, with
the same endpoint decoding/encoding as `route`, which checks the
implementation's input and output before erasing those types into a
homogeneous HTTP route.

For each request: match path and method, return 404 or 405 with `Allow` as
appropriate, decode inputs, call the field, and encode its result. Built-in
validation errors are handled at this boundary.

Application response failures use a server-specific error:

```fango
type Error = { status : Int, message : String }
```

`Web.Server.respond` converts `Fail Web.Server.Error` to a JSON error response.
It must enclose the implementations' own handlers whose clauses can fail;
placing it only around the dispatcher cannot catch failures originating in
those enclosing clauses. `Web.Server.run` wraps each application callback with
`respond` and delegates to `Http.Server.serve`, preserving its explicit stop
channel and scoped request lifetime. Process-interrupt integration is a
separate optional convenience that needs an OS signal API. Request/response
middleware such as `Http.GZip.wrap` composes normally; operation-level
middleware is a function from a contract value to a contract value.

### Client

```fango
call : Endpoint request response -> String -> request ->{Http.Client.Http, Fail Error} response
client : Entry -> Code

type Error
    = Transport Http.Client.Error
    | UnexpectedStatus Int String
    | InvalidResponse String
    | InvalidRequest String
```

`call` builds the request through the endpoint, sends it through
`Http.Client`, bounds the buffered response using the client configuration, and
decodes it through the same response description used by the server. It maps
underlying client failures to `Transport`, input/encoding failures to
`InvalidRequest`, and declared-but-invalid responses to `InvalidResponse`.

`client entry` generates `String -> Contract {Http.Client.Http, Fail Error}`:
`Meta.construct` over the contract, each field a lambda applying `call` to that
field's endpoint and the base URL. Failures are raised in the caller's context
and domain errors are returned as `Result` values, so `Fail.attempt` around
one field call handles that call alone. The first version disables automatic
redirect following so status validation observes the actual endpoint response.

### Testing

The first tests need no language work:

```fango
alice = User { id = 7, name = "Alice" }

testUsers : Users {}
testUsers =
    { getUser = { query ->
        if query.org == "acme" && query.id == 7 then Ok alice else Err NoSuchUser }
    }

result = Http.Client.mock { request -> dispatch testUsers request } {
    Fail.attempt { fetchUser "acme" 7 }
}
-- result == Ok (Ok alice)
```

This tests the generated request, framing, binding, dispatch, response
encoding, and client decoding. A separate fixture must fail the first call and
successfully perform a second under the same transport handler.

Application unit tests invoke a real workflow function with a stub contract
value. Overriding one operation of a real implementation or client is record
update, `{ shop | getUser = { _ -> Ok alice } }`. `Web.Testing.strict entry`
generates a contract value whose every operation fails through
`Expect.failWith`, reporting the operation and `inspect` arguments, as the base
for such updates. Recording uses ordinary effects such as `State`.

### Links

```fango
link : Endpoint request response -> request -> String
```

Links use only the request's URL fields. Optional query values are omitted;
repeated values keep their order. Header and body fields neither participate in
URL construction nor make the endpoint unlinkable. A URL-only projection of the
request, so a POST link needs no body value, is part of the first slice; its
shape is an [open decision](#open-decisions).

URL generation and validation share the client encoder. A total `String` result
can encode a target even when a field value is not accepted by the endpoint;
validate before sending. A checked link API or validated path types remain an
open choice before promising that every generated link matches.

### API documentation

```fango
openApi : Api -> Info -> Code
type Info = { title : String, version : String }
```

`Web.Docs.openApi` generates a pure expression producing an OpenAPI 3.1 string.
Route metadata is resolved at compile time; schema methods are ordinary typed
calls in generated code.

`Web.Schema` supplies explicit JSON schema evidence, with separate input and
output views when codec defaults or omissions make them differ. Its methods
mention the class parameter through a type witness; the schema data is
ordinary data with named references for recursive/shared types. W11 settles the
precise API. A deriver uses the same field-option interpretation as JSON:
`Json.Key` renames and `Json.Skip` omits; input fields without `Json.Default`
are required, including `Maybe` fields; `Maybe` makes a value nullable;
output requiredness follows what the encoder emits; nullary constructors are
strings and other constructors single-key objects; lists are arrays and
`Json.Value` is unconstrained.

The library does not infer whether a selected codec is handwritten. Custom
codecs require matching explicit schema evidence; deriving a schema is an
explicit promise that the codecs follow those rules, backed by codec/schema
tests. Operation descriptions use W5 when available; `#[Web.Docs.Tag "name"]`
on a field groups operations. Raw routes are identified as untyped.

### Diagnostics

Generators use `Meta.failAt` where source provenance is available:

| Problem | Site |
| --- | --- |
| Missing/duplicate route, invalid field shape or wire type | contract field or attribute |
| Missing/duplicate source, bad source type, unknown capture name | request field or attribute |
| Missing capture binding or conflicting capture types | field and mount context |
| Malformed template, duplicate mount name, conflicting effective route | available mount/field site with both route names |
| Missing/invalid `Status` | error constructor |
| Unknown operation name in `endpoint` or `client` | splice |
| Missing codec or schema evidence | generated use/splice, with endpoint context |

Ordinary typechecking verifies generated instance requirements; generators do
not promise to discover all missing instances in advance. A plain compile-time
string does not preserve the source site of its construction, so precise
mount-call blame needs explicit provenance or a site-bearing API.

## Language prerequisites

None is required to start: a prototype reflects the contract through
`Meta.declaredType` on a quoted annotated value until W15 lands. W1, W2, W3,
and W7 were retired with the effect-based contract; W14 moved to the
[effects roadmap](roadmap-effects.md#w14-partial-handlers-and-super). Do not
reuse those IDs.

### W15 Row-indexed types in `typeOf`

`typeOf (Users {})` is rejected as not closed: the closedness check in
`internal/infer/stage.go` has no case for a row argument. Accept a row
argument without a tail whose label arguments are closed, reflect it through
`Meta.args` as the row-kinded parameter's value, and keep open rows rejected.
Add a reflection fixture and a golden for the open-row diagnostic.

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

Deferred until official distribution. The library should live outside `stdlib/`;
a local example can carry its modules during the first prototype. A proposed
`libs/` directory would contain one directory per official library, enabled by
a build option or entry-file pragma. A separate repository with a search path
is the alternative. Preserve reserved-module and caching rules without
pre-empting a future package manager. This decision gates shipping, not W8's
local implementation.

## Library stages

### W8 Contract, templates, and binding

First implement the shared endpoint model, named mounts, record-field sources,
template validation, `Param`, `Failure`, runtime `Endpoint` values, buffered
JSON request/response codecs, and compile-time endpoint generation. Add
source-context diagnostic goldens. Validate a GET and POST contract before
expanding the surface.

`Form`, text/bytes body selection, additional result wrappers, and flag/checkbox
conveniences remain later increments coordinated with W12/W13. They must use
the same binding and response descriptions, not separate generator tables.

### W9 Server dispatcher

`Web.Server.route`, `dispatch`, `respond`, and `run`, raw routes, 404, 405,
`HEAD`, content-type checks, body limits, and server encoding failures. Cover
these with `Http.Server.serveRequest` and an example beside or replacing the
hand-routed part of `examples/http_server.fango`. Keep stop-channel ownership
and request/stream lifetimes aligned with the existing server.

### W10 Client runner and links

`Web.Client.call`, the generated `client`, client error types, `Web.link`, and
`Web.Testing.strict`. Verify named mount selection, per-call failure recovery,
response limits, record-update stubs over a generated client, and round trips
through `Http.Client.mock`.

### W11 OpenAPI

Implement explicit `Web.Schema` evidence and its derivation with shared JSON
field rules, then `Web.Docs.openApi`. Cover custom/opaque codecs, nullable versus
omittable fields, input/output differences, recursive references, error status
sets, and repeated mounts. Generated documents must validate against the
OpenAPI 3.1 schema in an offline test. Schema/codec conformance fixtures are
required; structural reflection alone is not proof of codec behavior.

### W12 Extensible responses

Deferred beyond the buffered JSON slice. Extend the shared response description
with status-bearing wrappers, text, HTML, files, CSV, and custom encodings only
with corresponding decoding and schema contracts. `Created` and other statuses
need concrete consumers. Buffered customization can precede streaming.

A `Respond` class is a candidate, not a settled interface. Before choosing it,
type-check a file response, a response carrying effects in its value, and a
request-backed streaming response without escaping their scopes. W4 alone is
insufficient; the response's required effects must be related to the method's
row. Raw routes remain the supported proposal for effectful streams until that
design is resolved. Header composition must preserve repeated fields such as
`Set-Cookie`.

### W13 Web app support

Deferred: form decoding/encoding, explicit text/bytes body modes, cookies,
`SetCookie`, `Html`, `Text`, `Redirect`, POST-redirect-GET helpers, and static
files through raw routes. Form derivation uses field names and `Param`, with
`Maybe`, repeated `List`, and checkbox rules specified explicitly.
`Redirect` must encode 303 plus `Location` and decode that same value without
following it. The `Html` wrapper remains a raw string; escaping builders and
templates are separate decisions. Content negotiation waits for a consumer.

### Dependencies

| Stage | Depends on |
| --- | --- |
| W4 Open effect rows in class methods | —; deferred |
| W5 Doc comments in reflection | —; optional descriptions |
| W6 Official library home | —; gates official distribution only |
| W15 Row-indexed types in `typeOf` | —; a prototype can use `Meta.declaredType` meanwhile |
| W8 Contract, templates, and binding | W15 for the final mount API |
| W9 Server dispatcher | W8 |
| W10 Client runner and links | W8; W9 for round-trip acceptance |
| W11 OpenAPI | W8; W5 for descriptions |
| W12 Extensible responses | W8, W9, W10; W4 only if the chosen class design requires it, plus a resolved effect-row relationship |
| W13 Web app support | W9, W10, relevant W12 response descriptions |

Deliver the minimal W8/W9/W10 slice together, with W15 alongside. First prove
local failure recovery and round-trip agreement. W11 follows with explicit
schema evidence. W4, W5, and W6 do not block that experiment.

## Verification

Language stages keep the Core linter, interpreter/compiler differential suite,
functional tests, and `go vet`, and keep `fango doc --stdlib --strict` clean
for new `Meta` declarations. Ordinary development checks benchmark builds with
`go vet ./benchmarks`; elapsed-time benchmarks remain governed by the
repository's measurement policy.

Library coverage includes:

- generator diagnostics, schema visibility, and explicit public endpoint names;
- named bindings under field reordering, repeated mounts, and conflicting routes;
- GET/POST round trips, typed domain errors, and one failed call followed by a
  successful call under the same transport handler;
- a record-update stub over the generated client and over `Web.Testing.strict`;
- invalid input, missing/duplicate parameters, media-type checks, 404, 405,
  `HEAD`, body limits, and server encoding failures;
- timeouts/transport errors, unexpected status, malformed declared response,
  error-constructor/status mismatch, and disabled automatic redirects;
- JSON nullable/omittable fields, explicit defaults, custom schema evidence,
  input/output schema differences, and offline OpenAPI validation;
- one example under both backends, retaining real framing through
  `Http.Client.mock` and the existing network acceptance gates.

## Open decisions

- **Mount captures in every request.** A mount capture such as `org` must be a
  field of every mounted request record. A mount-level record merged into each
  request, or a second parameter on the contract's fields, would remove the
  repetition; decide after the first multi-operation example.
- **URL-only request projection.** Choose between a generated `Link` record
  per endpoint, a request builder that leaves body/header fields unset, or
  accepting the whole request; a checked link helper or validated path types
  remain open before promising every generated URL is accepted.
- **Ambient clients.** The library hands out a record. Whether to generate a
  one-operation effect wrapper, or leave it to applications, waits for a
  second consumer.
- **Parameter conventions.** Resolve duplicate header/cookie handling,
  empty/rest captures, flags, and form checkboxes before enabling those forms.
- **Schema evidence.** W11 must settle witness methods, input/output views,
  recursive references, and a way to supply custom error status metadata.
  Do not infer codec provenance or require runtime type reflection.
- **Typed error envelope.** Domain errors initially use their bare JSON;
  problem-details envelopes are a later policy choice.
- **Content negotiation.** A response initially has one encoding. `Accept`-driven
  JSON/HTML selection waits for an application.
- **Library home and process integration.** W6 settles distribution. Interrupt
  handling needs a separate signal capability and must preserve server shutdown.
