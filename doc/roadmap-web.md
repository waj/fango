# Roadmap: typed web APIs

A proposed official library, `Web`, for HTTP APIs and simple web apps. Effects
with route attributes are the authoring surface; typed endpoint values are the
shared foundation for server adapters, ordinary client calls, links, and API
documentation. Nothing here is implemented. Examples use proposed APIs, with
application-specific modules such as `Db` left to the application.

The library builds on the existing [HTTP server](../stdlib/Http/Server.fango),
[HTTP client](../stdlib/Http/Client.fango), and [JSON](../stdlib/Json.fango).
Stage IDs follow the [repository milestone rules](../AGENTS.md).

## Direction

An effect groups named operations with independently typed inputs and results.
An API operation takes one named request record, or `()` when it has no inputs.
Record-field attributes bind inputs by name. Compile-time generators normalize
the declaration and its mount into one checked endpoint description:

- a **server** decodes a request, performs the operation, and encodes its result;
  an application implements operations with ordinary handlers;
- a **client** calls an ordinary function with an `Endpoint request response`
  value, a base URL, and a request value;
- **links and docs** use the same binding and response descriptions;
- a **test** handles API operations directly or connects client calls to the
  dispatcher through [`Http.Client.mock`](design/http.md#mocking-through-framing).

Client calls perform HTTP work in the caller's context. A `Fail.attempt`
around one call can recover that call's transport or decoding failure. A client
implemented as an operation handler has different semantics: its clause fails
through a handler outside the runner. The existing HTTP transport deliberately
[returns results to caller code](design/http.md#the-client-transport) for this
reason. An effect-based client runner remains optional follow-up work.

`Endpoint request response` associates both types without type families or
GADTs. A server adapter consumes an endpoint and a typed function to produce an
ordinary route; different endpoints can then coexist in a homogeneous route
list. Effect declarations provide a concise authoring and implementation layer
over that foundation. A union of route constructors would need a different
way to express each constructor's response type; it is not needed here.

One shared description owns parameter binding, codecs, statuses, headers, and
schemas. Generators must not maintain independent result-type dispatch tables.
Shared declarations prevent accidental duplication, but codec/schema agreement
and compatibility between separately deployed versions still need verification.

The first vertical slice is a buffered JSON GET and POST API with typed domain
errors, typed links, and client/server round trips. Forms, redirects, extensible
responses, and handler syntax improvements remain later work. The prototype can
live beside an example before official library distribution is settled.

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

-- Users of one organization.
effect Users
    -- The user with this ID.
    #[Web.Get "/users/:id"]
    getUser : GetUser -> Result UserError User
```

`GetUser` needs no JSON instance: it describes path and query inputs. A POST
request record can instead contain a `#[Web.Body]` field whose type implements
JSON encoding and decoding. `Web.Schema` is explicit schema evidence for docs,
not a claim that reflection can identify the selected codec's implementation.

A named mount selects the public location. Endpoint names stay explicit source
bindings; expression splices introduce no hidden public declarations:

```fango
module Shop.Api exposing (api, getUserEndpoint)

import Shop.Users exposing (GetUser(..), User(..), UserError(..), Users(..))
import Web

users = Web.mount "users" "/orgs/:org" (effectOf Users)
api = Web.api [users]

getUserEndpoint : Web.Endpoint GetUser (Result UserError User)
getUserEndpoint = $(Web.endpoint users `Shop.Users.getUser`)
```

The server implements the effect as usual:

```fango
import Db
import Shop.Api
import Shop.Users exposing (Users(..))
import Web.Server

serveUsers : (() ->{Users, e} a) ->{Db, e} a
serveUsers action =
    handle action() on
        getUser request -> resume (Db.findUser request.org request.id)

dispatch = $(Web.Server.dispatch Shop.Api.api)

app request =
    Db.run { serveUsers { dispatch request } }
```

An endpoint can also be attached to a function directly, without an API handler:

```fango
route = Web.Server.route Shop.Api.getUserEndpoint {
    request -> Db.findUser request.org request.id
}
```

Client code uses an ordinary function:

```fango
import Fail exposing (Fail)
import Http.Client
import Shop.Api
import Shop.Users exposing (GetUser(..), User, UserError)
import Web.Client

fetchUser : String -> Int ->{Http.Client.Http, Fail Web.Client.Error} Result UserError User
fetchUser org id =
    Web.Client.call Shop.Api.getUserEndpoint "https://shop.example"
        (GetUser { org = org, id = id, verbose = Nothing })
```

Inside `Http.Client.run`, `Fail.attempt { fetchUser "acme" 7 }` catches that
call's client failure. Its result distinguishes `Ok (Ok user)`,
`Ok (Err NoSuchUser)`, and `Err clientError`. A second call can proceed after
the first fails.

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
| `Web.Client` | Ordinary `call` and client errors; an optional generated runner later. |
| `Web.Docs` | `openApi`, schema data, and documentation attributes. |
| `Web.Testing` | A generated strict base handler, after handler builders are available. |

Forms and result wrappers belong to the later stages below. Official shipping
outside `stdlib/` is [W6](#w6-official-library-home); a local prototype does not
need a new library search mechanism.

### API effects

Every API operation has exactly one route attribute and exactly one argument:
a concrete visible record, or `()` for no inputs. Reject missing/duplicate
route attributes, abort operations, operation-local type variables, parameterized
effects, and other argument shapes. Wire fields and results must have supported
concrete codecs; function and resource values are not wire values.

Operations retain their ordinary module-level names. Endpoint identity includes
the selected mount and the operation's compiler identity. Operation comments
become descriptions when [W5](#w5-doc-comments-in-reflection) lands.
API effects describe externally callable operations; application workflows may
use ordinary functions and narrower service effects internally.

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
`:`, `*`, `?`, and `#` as template syntax; URL escaping and matching must use
one normalization rule in links, clients, and servers. Captures are decoded
once. Encoded `/` and `\` in a segment are rejected with 400, following
[`Http.Server.Route`](../stdlib/Http/Server/Route.fango).

Names are unique across the full mount and operation path. Matching compares
complete paths, preferring literal segments over captures and captures over a
rest capture at the first differing position; a dead-end literal branch must
not hide a matching capture branch. Equal shapes after capture-name erasure
conflict for the same method. Effective methods include generated `HEAD` routes
and raw routes. Path matching and method selection must agree on 404 versus
405 and the `Allow` set. Diagnostic fixtures cover intersecting patterns,
rest captures, and explicit raw `HEAD` collisions.

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
is not an implicit Boolean flag in the first version.

Specify and test `parseParam (printParam value)` round trips for supported
values. Path strings containing forbidden separators or other unrepresentable
values require a client-side validation failure; they must not silently produce
a different target. Header/cookie duplicate policy and flag/checkbox shorthand
are later decisions, not inferred from JSON rules.

### Request bodies

The initial `Body` encoding is JSON for every supported type, including
`String`. Content types are `application/json` and application subtypes ending
in `+json`, with media-type parameters parsed separately. A mismatch answers
415; malformed JSON or invalid field values answer 400. Bodies are bounded by
the server's `bodyLimit` and decoded before the operation runs.

Text, bytes, and forms need explicit encoding selection when added; a type must
not silently switch from JSON to text because its representation is `String`.
Streaming and uploads use [raw routes](#raw-routes).

JSON field defaults remain the [codec's contract](../stdlib/Json/Field.fango).
A `Maybe` field accepts `null` but is still required unless it has
`#[Json.Default ...]`. An optional patch field therefore needs an explicit
``#[Json.Default `Nothing`]``. Missing, null, and a new value need a distinct
patch representation when the application must distinguish all three.

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
set; a handwritten mapping needs explicit matching status metadata. Error
encoding needs `Json.Encode`; client decoding needs `Json.Decode`.

Unexpected statuses are client errors. A declared status with the wrong media
type, malformed body, or inconsistent error constructor is an invalid response.
A framework 400/404 response is not automatically a domain error merely because
its status also appears in the contract. Server encoding failures produce 500
before any response bytes are committed; decoder details from client input are
400, not server encoding errors.

`Html`, `Text`, `Redirect`, `Created`, and other status-bearing wrappers are
follow-up work in W12/W13. Each must supply the complete response description,
including a matching client decoder and documentation. In particular, a redirect
value must preserve `Location`; following it would lose that value.

### API trees and endpoints

Proposed compile-time builders:

```fango
api : List Entry -> Api
mount : String -> String -> EffectRepr -> Entry
root : String -> EffectRepr -> Entry
nest : String -> List Entry -> List Entry
raw : String -> String -> Code -> Entry
endpoint : Entry -> Code -> Code
```

The first string of `mount`, `root`, and `raw` is a stable, unique name in the
API tree. `mount` also takes a prefix; `root` uses `/`. `raw` takes a
`"METHOD template"` and a quoted handler. Prefixes cannot contain query or rest
captures. `nest` returns entries with the prefix applied, so an endpoint must
select the resulting mounted entry, not an unprefixed source entry.

One effect may be mounted several times. Each is selected explicitly by its
entry; there is no client lookup by effect alone. Duplicate entry names and
conflicting effective routes are compile-time errors. Joining a prefix and an
operation path of `/` drops the extra slash. `/orgs/acme` and `/orgs/acme/` remain
distinct, without an automatic redirect.

`Api` and `Entry` are compile-time-only values holding reflection and code.
`endpoint entry quote` requires a quoted operation of that entry's effect and
generates an ordinary `Endpoint request response`. Its public binding is written
explicitly by the user. Direct server adapters, client calls, and links use this
runtime value without `Code`, `EffectRepr`, or runtime reflection.

For the buffered first version, endpoint functions convert between typed values
and ordinary buffered HTTP data: method, target, headers, and bytes. They return
validation/codec results. Reader/writer effects and HTTP framing live in the
server and client drivers. This avoids requiring row-polymorphic fields in an
endpoint or a `Respond` class. Docs use the same normalized compile-time model
and explicit schema evidence. The internal model's concrete representation is an
implementation decision; consistency across all consumers is an invariant.

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

`dispatch` generates a request handler whose effects include the mounted API
effects, protocol effects, and any effects needed by raw routes. Each generated
operation adapter uses the same endpoint decoding/encoding as `route`. The latter
checks the implementation's input and output before erasing those types into a
homogeneous HTTP route.

For each request: match path and method, return 404 or 405 with `Allow` as
appropriate, decode inputs, perform the operation, and encode its result.
Built-in validation errors are handled at this boundary.

Application response failures use a server-specific error:

```fango
type Error = { status : Int, message : String }
```

`Web.Server.respond` converts `Fail Web.Server.Error` to a JSON error response.
It must enclose installation of application handlers whose clauses can fail;
placing it only around the dispatcher cannot catch failures originating in
those enclosing clauses. Validate error statuses and distinguish this error
from client failures that may have no HTTP status.

`Web.Server.run` wraps each application callback with `respond` and delegates
to `Http.Server.serve`, preserving its explicit stop channel and scoped request
lifetime. Process-interrupt integration is a separate optional convenience that
needs an OS signal API; it is not an assumed capability of `Process`.
Request/response middleware such as `Http.GZip.wrap` composes normally. Typed
operation middleware can use complete handlers today; W14 is optional ergonomics.

### Client

```fango
call : Endpoint request response -> String -> request ->{Http.Client.Http, Fail Error} response

type Error
    = Transport Http.Client.Error
    | UnexpectedStatus Int String
    | InvalidResponse String
    | InvalidRequest String
```

`Web.Client.call` builds the request through the endpoint, sends it through
`Http.Client`, bounds the buffered response using the client configuration, and
decodes it through the same response description used by the server. It maps
underlying client failures to `Transport`, input/encoding failures to
`InvalidRequest`, and declared-but-invalid responses to `InvalidResponse`.
Unexpected response text is bounded by the response limit.

Failures are raised by the ordinary call in the caller's context. Domain errors
are returned as `Result` values. `Fail.attempt { call endpoint base input }`
therefore handles one call and permits later calls to proceed. The first version
disables automatic redirect following so status validation observes the actual
endpoint response. Any future following policy must be explicit and compatible
with the endpoint's result contract.

An optional generated effect runner belongs to W10's later work. It must select
a mount, and document that failures in its clauses escape to a handler outside
the runner. It must not promise per-operation recovery inside the runner without
changing the operation result contract. Ordinary `call` remains the default.

### Testing

The first tests need neither partial handlers nor a generated strict handler:

```fango
alice = User { id = 7, name = "Alice" }

testApp request =
    handle dispatch request on
        getUser query ->
            resume (
                if query.org == "acme" && query.id == 7 then Ok alice
                else Err NoSuchUser
            )

result = Http.Client.mock testApp {
    Fail.attempt { fetchUser "acme" 7 }
}
-- result == Ok (Ok alice)
```

This tests the generated request, framing, binding, operation dispatch, response
encoding, and client decoding. A separate fixture must fail the first call and
successfully perform a second under the same transport handler.

Application unit tests invoke a real workflow function under stubbed service
effects. Stubbing `getUser` does not implement another endpoint such as
`userPage`. Partial handlers may later override selected calls over a real
handler or a strict base. `Web.Testing.strict` is a generated handler that fails
unstubbed calls through `Expect.failWith`, reporting the operation and `inspect`
arguments. Recording uses ordinary effects such as `State`.

### Links

```fango
link : Endpoint request response -> request -> String
```

Links use only the request's URL fields. Optional query values are omitted;
repeated values keep their order. Header and body fields neither participate in
URL construction nor make the endpoint unlinkable. The first API accepts the
whole request; a URL-only projection can follow a consumer.

URL generation and validation share the client encoder. A total `String` result
can encode a target even when a field value is not accepted by the endpoint
(for example a forbidden path separator); document that limitation and validate
before sending. A checked link API or validated path types remain an open
choice before exposing a stronger guarantee that every generated link matches.

### API documentation

```fango
openApi : Api -> Info -> Code
type Info = { title : String, version : String }
```

`Web.Docs.openApi` generates a pure expression producing an OpenAPI 3.1 string.
Route metadata is resolved at compile time; schema methods are ordinary typed
calls in generated code. Producing a fully folded string literal is optional,
not a reason to add instance-provenance reflection or a second staging level.

`Web.Schema` supplies explicit JSON schema evidence, with separate input and
output views when codec defaults or omissions make them differ. Its methods
must mention the class parameter through a type witness; the schema data is
ordinary data with named references for recursive/shared types. W11 settles the
precise schema API. A deriver uses the same field-option interpretation as JSON:

- `Json.Key` renames fields and `Json.Skip` omits them;
- input fields without `Json.Default` are required, including `Maybe` fields;
- `Maybe` makes a value nullable, independently of whether its field is optional;
- output requiredness follows what the encoder actually emits;
- nullary constructors are strings; other constructors are single-key objects;
- lists are arrays and `Json.Value` has an unconstrained schema.

The library does not infer whether a selected codec is handwritten from type
structure. Custom codecs require matching explicit schema evidence; opaque types
can export that evidence without exposing their representation. Deriving a
schema is an explicit promise that the codecs follow those rules, backed by
codec/schema tests. Existing reflection has no instance-provenance query.

Operation descriptions use W5 when available; `#[Web.Docs.Tag "name"]` groups
operations. Error responses and parameter schemas come from the shared contract.
Raw routes are identified as untyped. A schema attribute override may be a
convenience for custom evidence, but is not an automatic codec detector.

### Diagnostics

Generators use `Meta.failAt` where source provenance is available:

| Problem | Site |
| --- | --- |
| Missing/duplicate route, invalid operation shape or wire type | operation |
| Missing/duplicate source, bad source type, unknown capture name | request field or attribute |
| Missing capture binding or conflicting capture types | operation and mount context |
| Malformed template, duplicate mount name, conflicting effective route | available mount/operation site with both route names |
| Missing/invalid `Status` | error constructor |
| Endpoint quote does not belong to its selected mount | quote |
| Missing codec or schema evidence | generated use/splice, with endpoint context |

Ordinary typechecking verifies generated instance requirements; generators do
not promise to discover all missing instances in advance. A plain compile-time
string value does not itself preserve the source site of its construction, so
precise mount-call blame needs explicit provenance or a quote/site-bearing API.

## Language prerequisites

### W1 Attributes on effect operations and parameters

First add attributes before operation signatures: parser target rules, storage,
evaluation timing, formatter layout, REPL rollback, and the TextMate grammar.
Visibility follows [effect attributes](reference/metaprogramming.md#effect-reflection),
readable only where operations are exposed. Add a grammar tokenization case.

Request-field source tags use existing record-field attributes. Tags before
operation parameter type atoms remain deferred within this milestone until a
consumer needs them; they are not needed for named request records.

### W2 Operation reflection

Add `effectOf E : Meta.EffectRepr` for a fully applied effect, and:

```fango
operations : EffectRepr -> Items Operation
type Operation = { name : String, symbol : String, index : Int, params : Items OpParam, result : TypeRepr, isAbort : Bool, attributes : Attributes, site : Site }
type OpParam = { index : Int, ty : TypeRepr }
```

Parameter types are instantiated at the effect's arguments. Visibility follows
operation exposure; request field reflection retains the corresponding schema
visibility. Parameter attributes can be added with W1's deferred part.
The module that writes `effectOf` must expose the request record's fields to
reflection; exposing an effect does not grant access to otherwise opaque types.
`Meta.isReference` and `Meta.declaredType` accept quoted operations, and
`Meta.operationOf : Code -> Operation` identifies a quoted operation. No runtime
reflection or declaration generation is required for explicit endpoint bindings.

### W3 Operation and handler code builders

First provide `perform : Operation -> Items Code -> Code`, a saturated call used
by the server dispatcher. The ordinary client does not need a handler builder.

Later, generated strict handlers and optional client runners need:

```fango
handleAll : EffectRepr -> Code -> (Operation -> Items Code -> (Code -> Code) -> Code) -> Code
```

The subject is a Unit-taking computation. Each callback receives the operation,
compiler-owned argument binders, and a builder for `resume value`. Follow the
ordinary resume discipline and hygiene rules, as `Meta.match` does. Abort
clauses and `return` remain outside this builder until a consumer needs them.

### W4 Open effect rows in class methods

Deferred general class-system work: method-local row variables, per-method
quantification, dictionary representation, and deriver support. The endpoint
foundation needs no such feature; its buffered codecs operate on plain data.

This alone does not solve W12. A method `respond : a -> Http.Server.Response e`
quantified over arbitrary `e` cannot require `FileSystem` merely because a
particular response streams a file. The value's effects and response row need
an explicit relationship. Settle that relationship against concrete examples
before treating this milestone as sufficient for streamed response instances.

### W5 Doc comments in reflection

`Meta.docOf : Code -> Maybe String`, and doc text on `TypeInfo`, `Ctor`,
`Field`, `Effect`, and `Operation`, using the comments
[`fango doc`](reference/commands.md#api-documentation) already extracts. Keep
existing visibility rules. OpenAPI works without descriptions until this lands.

### W6 Official library home

Deferred until official distribution. The library should live outside `stdlib/`;
a local example can carry its modules during the first prototype. A proposed
`libs/` directory would contain one directory per official library, enabled by
a build option or entry-file pragma. A separate repository with a search path
is the alternative. Preserve reserved-module and caching rules without
pre-empting a future package manager. This decision gates shipping, not W8's
local implementation or W7's syntax work.

### W7 Tail-resumptive clause shorthand (optional)

Deferred ergonomics. A possible `getUser request => Db.findUser request.org request.id`
clause resumes with its body's value. The existing checker already proves tail
resumption; shorthand provides no additional optimization guarantee. It is not
a prerequisite for any library stage.

### W14 Partial handlers and `super`

Deferred independent language work. The first endpoint/client/test slice does
not depend on this stage. Wildcard forwarding can precede expression-level
`super`; its evaluation and lambda-capture semantics need a separate review.
Named clauses can already forward with explicit operation calls.

Today a handler [needs a clause group for every operation](reference/effects.md#handler-clauses-and-abort-routing).
Overriding one operation of a six-operation API therefore takes six clauses,
five of which perform their operation again for the enclosing handler. Tests
that stub one call and operation-level middleware both pay this cost, and it
grows with every operation added to the API. Two additions remove it:

- a **wildcard clause** `_ -> super`, written last, which covers every
  operation and argument pattern the earlier clauses leave unmatched;
- the keyword **`super`**, meaning the same operation performed again, with
  the same arguments, for the next handler out.

```fango
effect Catalog
    lookup : Int -> Maybe String
    names : () -> List String

withKnownName : (() ->{Catalog, e} a) ->{Catalog, e} a
withKnownName action =
    handle action() on
        lookup 7 -> resume (Just "Alice")
        names () -> resume (List.filter isActive super)
        _ -> super
```

Here `lookup 8` falls through to the wildcard. `names` forwards and filters
the outer answer; `isActive` is an application predicate on names. This
standalone effect illustrates the language feature independently of Web.

#### Wildcard clause

- `_` matches every operation, of every handled application, not covered by
  an earlier clause. This includes argument patterns of named groups that left
  some calls unmatched.
- Its body must be exactly `super`. No other body type-checks for operations
  with different result types and disciplines.
- It is the last clause before `return`. A clause after it is redundant, and a
  wildcard with nothing left to cover is a redundant-clause error.
- A handler needs at least one named clause, which decides the handled
  effects. A handler whose only clause is `_ -> super` is rejected.
- Coverage checking is unchanged: without a wildcard, a missing group or
  pattern is still an error. A handler opts in visibly, clause by clause, not
  through a handle-level keyword that would also hide mistakes.
- `_` never matches the `return` group, and carries no
  [operation signature](reference/effects.md#operation-signatures). Under
  several applications it covers each one independently.

#### `super`

`super` is valid only inside a handler clause, and refers to that clause's
operation and arguments. As in Ruby, it takes no arguments. Performing the
operation with different arguments needs no new syntax: clauses already run
outside their own activation, so calling the operation by name reaches the
next handler out.

- **As a whole clause body, it forwards.** A resumptive operation resumes with
  the outer handler's answer. A [stateful handler](reference/effects.md#stateful-handlers)
  resumes with its snapshot unchanged. An abort operation propagates the
  outer abort. The body counts as the clause's one tail resume, or as its
  abort terminal, under
  [resume discipline](reference/effects.md#resume-discipline).
- **Inside an expression, it is the outer answer.** It has the operation's
  result type, so a resumptive clause can post-process it
  (`resume (List.filter isActive super)`) or record a call before forwarding.
- In a nested `handle` inside a clause body, `super` refers to the innermost
  enclosing clause. A lambda inside a clause body may capture `super`, as it
  captures the clause's argument binders.
- Outside a clause, `super` is a `SUPER OUTSIDE CLAUSE` error.

#### Typing

A handler that can forward an operation of an application, through a wildcard
or through `super`, keeps that application in its own effect row, because an
outer handler still has to answer it. A handler that never forwards removes
the application, as it does today. This is the shape of Haskell's `interpose`,
and matches the forwarding rule of open handlers in Eff and Links.

#### Lowering

Forwarding is tail-resumptive, so it fits the
[synchronous effect execution](design/effects.md) without continuation
objects:

- a forwarding body is a call through the outer handler's evidence, followed
  by `resume`;
- an operation reached only by the wildcard can bind its evidence slot directly
  to the outer handler's, with no clause frame;
- an abort is forwarded by aborting outward with the same payload.

Core sees ordinary clauses, so the Core linter's coverage and resume checks
apply unchanged.

#### Surface changes

- `super` becomes a reserved word, in the lexer and the TextMate grammar, with a
  `tokenize.cjs` case.
- The parser accepts the wildcard clause.
- The formatter keeps it last.
- The [effects reference](reference/effects.md) documents both forms and their
  diagnostics.

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

First implement ordinary `Web.Client.call`, client error types, and `Web.link`
over typed endpoints. Verify named mount selection, per-call failure recovery,
response limits, and round trips through `Http.Client.mock`. This part needs
neither generated handlers nor partial handlers.

The optional client runner and `Web.Testing.strict` remain later parts of this
milestone, depending on W3's handler builder. A runner selects a mount and
states its failure scope explicitly. W14 may improve test/middleware ergonomics,
but is not a dependency of ordinary calls, complete stubs, or round-trip tests.

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
`Maybe`, repeated `List`, and checkbox rules specified explicitly. Honoring
`Json.Key` as a form key remains a possible shared convention.

`Redirect` must encode 303 plus `Location` and decode that same value without
following it. Browser navigation and a typed client's observation are separate
policies. The `Html` wrapper remains a raw string; escaping builders and
templates are separate decisions. Content negotiation waits for a consumer.

### Dependencies

Existing milestone IDs and titles remain unchanged. Where a stage has a first
and later part, completion requires both or an explicit later scope decision.

| Stage | Depends on |
| --- | --- |
| W1 Attributes on effect operations and parameters | —; parameter tags deferred |
| W2 Operation reflection | W1 operation attributes |
| W3 Operation and handler code builders | W2; handler builder deferred |
| W4 Open effect rows in class methods | —; deferred |
| W5 Doc comments in reflection | —; optional descriptions |
| W6 Official library home | —; gates official distribution only |
| W7 Tail-resumptive clause shorthand (optional) | —; deferred |
| W8 Contract, templates, and binding | W1 operation attributes, W2 |
| W9 Server dispatcher | W3 operation calls, W8 |
| W10 Client runner and links | W8; W9 for round-trip acceptance; W3 handler builder for later runner/strict handler |
| W11 OpenAPI | W8; W5 for descriptions |
| W12 Extensible responses | W8, W9, W10; W4 only if the chosen class design requires it, plus a resolved effect-row relationship |
| W13 Web app support | W9, W10, relevant W12 response descriptions |
| W14 Partial handlers and `super` | —; deferred independent language work |

Deliver W1's operation attributes, W2, W3's operation calls, then the minimal
W8/W9/W10 slice together. First prove local failure recovery and round-trip
agreement. W11 follows with explicit schema evidence. W4, W6, W7, and W14 do
not block that experiment. W5 enriches docs independently.

## Verification

Language stages keep the Core linter, interpreter/compiler differential suite,
functional tests, and `go vet`. Run `make test-grammar` for syntax changes in
W1, W7, or W14, and keep `fango doc --stdlib --strict` clean for new `Meta`
declarations. Ordinary development checks benchmark builds with
`go vet ./benchmarks`; elapsed-time benchmarks remain governed by the
repository's measurement policy.

Library coverage includes:

- generator diagnostics, schema visibility, and explicit public endpoint names;
- named bindings under field reordering, repeated mounts, and conflicting routes;
- GET/POST round trips, typed domain errors, and one failed call followed by a
  successful call under the same transport handler;
- invalid input, missing/duplicate parameters, media-type checks, 404, 405,
  `HEAD`, body limits, and server encoding failures;
- timeouts/transport errors, unexpected status, malformed declared response,
  error-constructor/status mismatch, and disabled automatic redirects;
- JSON nullable/omittable fields, explicit defaults, custom schema evidence,
  input/output schema differences, and offline OpenAPI validation;
- one example under both backends, retaining real framing through
  `Http.Client.mock` and the existing network acceptance gates.

## Open decisions

- **Endpoint authoring without effects.** The typed foundation supports direct
  server adapters. A public descriptor builder independent of effect reflection
  can follow a consumer; it does not require type families or GADTs.
- **Checked links.** Choose a checked link helper or validated path types before
  promising every generated URL is accepted. URL-only request projections may
  avoid supplying irrelevant body/header fields.
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
- **Handler-row ergonomics.** Effect aliases name a row, but installing its
  handlers remains explicit. Combined runners and W14 remain separate work.
