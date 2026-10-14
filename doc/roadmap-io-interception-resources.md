# Roadmap: resources and execution for IO interception

This companion to the [IO interception proposal](roadmap-io-interception.md)
owns unresolved representation and execution choices. The
[API catalogue](roadmap-io-interception-api.md) owns proposed public signatures.
Nothing here expands the implemented resource or task contracts by itself.

## Required separation of identities

Keep three questions distinct:

| Identity | Question |
| --- | --- |
| Handler activation | Which interpretation does this operation call select? |
| Resource owner/provider | Which backend created this handle and may interpret its payload? |
| Lifetime owner | Which scope closes this resource, and when? |

A fresh handler activation does not automatically create a new resource, and
closing a resource does not retire every operation handler of that effect.
Two handles of the same nominal type may belong to different providers. Two
tasks may share one resource under a handler inherited from the parent.

The current [closure rule](reference/effects.md#closures-and-handler-effects)
still applies: an ordinary effectful closure selects evidence at invocation.
Creating a closure under a handler does not make it pure or automatically bind
every later operation to that handler. Scoped activation binding carries a
fresh permission and has its own escape checks.

## Resource representation alternatives

### Retain opaque handles and interpreted operations

The API catalogue starts with the familiar `File.withFile` callback receiving
an `IO.Handle`, followed by `IO.readLine handle`. Acquisition and generic handle
operations belong to separate proposed effects. This minimizes changes to
call sites, but requires a representation shared by real and virtual handles.

A virtual handle needs an unforgeable association with its provider, an open
or closed state, resource kind, and position or other resource-specific state.
Do not assume that a plain integer index is enough. Reusing an index after a
runner exits must not allow an escaped handle to alias an unrelated new file.
Two independently created memory runners must not interpret equal integer
indices as the same resource.

Possible representations include a library-owned sum of real and virtual
payloads with checked owner tokens, or a checked opaque provider payload. The
existing native `Runtime.Native.Any` wrapper is not already a safe public
virtual-handle API. Native sidecars must project only the real case after
ownership validation; passing a mock payload to Go and hoping a type assertion
catches it is not the language contract.

If driver operations are public, custom handlers need a way to create valid
resources without constructing the native representation. That construction
API is a required deliverable, not hidden implementation work. Its result must
retain the relevant identity, effects, and lifetime obligations.

### Use scoped source and sink capabilities

An alternative is to expose row-indexed callback records instead of a generic
handle at the application boundary:

```text
use source <- File.withSource "config.txt"
use reader <- Text.Reader.over ... source
Text.Reader.readLine reader
```

This example is schematic; the exact acquisition and granted callback rows
remain to be selected. The existing [Bytes](../stdlib/Bytes.fango) Source/Sink
records and [scoped callbacks](reference/functions.md#scoped-callbacks) are
building blocks, not proof that any proposed signature will type-check.

Operations on the capability can call the backend selected at acquisition.
Two sources can coexist without dispatching each read through a newly selected
ambient file handler. The rows of their fields must retain all backend effects;
mutable state cannot be presented as an empty-row callback. The scoped runner
must discharge only its fresh local permission and preserve ambient effects.

Questions before selecting this design:

- Does the callback receive a Source, a Sink, a duplex pair, or a higher-level
  Reader/Writer? Buffering should not be forced into the acquisition layer
  merely to simplify one example.
- How does a real or mock acquisition handler produce a capability whose
  effects are valid inside the selected callback? A row-indexed record type
  alone does not solve the effect-operation signature.
- Can existing granted-effect callback rows express the provider contract,
  or is a scoped higher-order operation, hidden type, or other extension needed?
- Which values may leave the callback? Scoped local permissions must not
  escape through results, records, closures, or outer mutable storage.
- How do two resources with distinct permissions compose in parsers, copy
  functions, HTTP bodies, and tasks?
- How are deadlines, close, listener accept, pool reuse, and other operations
  beyond byte pull/push represented?

Do not silently make row-kinded effect parameters, non-tail resumptions, or
new rank-polymorphic native callbacks prerequisites. Name any actual gap and
compare it with the handle baseline before expanding the compiler.

### Use domain-specific handles

A third option keeps opaque handles but makes reads/writes members of the File
effect and network operations members of Net. That removes the generic
`IO.Stream` requirement from file code but changes calls such as `IO.readLine
file`. Sharing ordinary Source/Sink adapters can preserve generic parsing
without making the handle types interchangeable.

Compare all three options on a file-to-socket copy, two simultaneous files,
stdin-to-file copying, streamed HTTP bodies, and a nested mock that delegates
some resources. Choose using complete caller and handler examples rather than
the number of fields in an internal representation.

## Nested providers and foreign handles

Installing a handler changes lexical operation dispatch; it does not grant
ownership of every payload with the same nominal handle type. The selected
design must define what happens when an inner mock sees an outer provider's
handle.

Candidate policies:

1. A closed mock rejects every foreign handle with a specified typed error or
   documented resource fault. It never falls back to native IO.
2. A forwarding adapter interprets owned handles and explicitly calls the outer
   operation for foreign handles. The outer effect remains in its row.
3. A capability value selects its own provider, retaining that provider's
   effects and lifetime permission in its type/closure contract.

The baseline memory runner is closed. Therefore merely nesting
`File.runMemory` and `IO.runStandardTest` does not promise that each will
automatically forward the other's handles. They both handle the proposed
`IO.Stream`, so the inner handler would otherwise intercept both kinds.
Provide an explicit combined runner or an ownership-aware forwarding adapter,
or choose the capability alternative. This composition issue must be resolved
before claiming a full simulated stdin/files/stdout world.

`Console.runTest` avoids that particular collision by handling a distinct
Console effect, which is one reason to retain the higher-level seam. Conversely,
it does not capture raw standard-handle writes. The two scopes have different
coverage; neither should silently impersonate the other.

Pure fallback must remain pure. A forwarding mock that can use a real outer
provider retains IO through that provider's runner. Missing handlers remain
errors under the explicit-runner policy. Existence of a native default for
some unrelated operation is not permission to access an unknown foreign
resource.

## Lifetimes, escape, and cleanup

Preserve [cleanup ordering and failure precedence](reference/resources.md).
Use `Runtime.Scope.bracket` or `finally` around acquisition and use; a normal
handler return clause is insufficient because it is skipped by language
aborts. Release must run with the evidence belonging to its lexical scope,
not whatever handler happens to be active at an unrelated failure site.

The existing handle contract uses runtime validity checks after close; it is
not a compiler-wide proof that resources cannot escape. The baseline must
preserve that distinction. A virtual handle that escapes a callback or an
entire mock runner must become unusable at the required lifetime boundary.
It must never keep live mutable fixture state usable through an apparently
pure returned value. Provider teardown, stale-handle checking, and ownership
tokens need a specified implementation even when the common path uses brackets.

If the capability alternative provides stronger static non-escape behavior,
document that as part of the new API, not as a retroactive assertion about all
existing resources. Any retained explicit raw acquisition API needs a stated
close obligation and leak behavior.

Required cases include:

- Failed acquisition releases no unacquired handle; partial native acquisition
  cleans up its own partial work.
- Successful body and failed close report the close failure.
- Failed body and failed close preserve the body failure and record the close
  failure according to existing suppressed-failure rules.
- Nested resources release in reverse acquisition order.
- Explicit close followed by scope cleanup has a deliberate repeated-close
  policy matching the API; it does not accidentally run a release twice.
- A language abort to an outer handler triggers cleanup before that outer
  handler observes it.
- A returned Source or closure cannot evade closed-resource or scope checks.
- A task cannot continue using a mock after its provider lifetime has ended;
  establish the relationship between task drain and provider teardown.

Capturing process exit as a language abort would apply these guarantees to
that exit path. It does not apply them automatically to a Go panic, hard process
termination, or release code that never returns. The process-exit proposal must
decide how cleanup failures interact with an already selected exit status.

## Mutable fixtures and concurrency

Use immutable values for fixture inputs and returned observations. Mutable
execution state belongs to a runner activation or checked resource backend.
Starting two runners from the same fixture must not share mutable state.
Publishing a snapshot must not expose a buffer that later writes can mutate.

The current [task runtime](design/tasks.md#async-runtime-foundation) can inherit
resumptive handlers and publish their state for synchronized access. Protected
cell loads and stores do not make an entire handler clause an atomic
read-modify-write operation. Two concurrent console appends or file offset
updates must not lose each other's changes.

Define the unit of synchronization for each operation:

| Operation | Required consistency question |
| --- | --- |
| Console write | Is each write appended as one indivisible event? |
| Console read | Can two tasks consume the same scripted line? |
| File read | Does one successful read advance the shared offset exactly once? |
| Append | Are placement and data publication atomic at the promised operation boundary? |
| Close versus read | Which operations may complete, and which observe closed state? |
| Connection write | Can bytes from separate calls interleave? |
| Pool release/acquire | Can a connection be leased to two callers? |
| Script expectation | Can an expected event be consumed twice? |

Choose a checked locking/atomicity mechanism or initially restrict a fixture to
sequential use with an enforceable diagnostic. A convenient Fango state handler
must not claim concurrent correctness without proving this boundary. Avoid
holding a shared fixture lock across arbitrary user callbacks or indefinitely
blocking operations. Do not add a task or channel to every synchronous effect
operation simply to serialize it.

Tests should assert per-resource order and valid causal relationships. A
global trace can record observed order but does not make that order repeatable
across native schedules. Fixed clock readings and seeded randomness do not
establish deterministic ordering between tasks.

## Cancellation and network scripts

Cancellation must remain attached to the requesting task and supported
operation lifetime. The public Net driver cannot expose an unchecked Go
context payload as its contract. A checked token/capability must specify
whether it can be retained, inherited, copied, and used after its owner ends.

Evaluate synchronous and cancellable variants independently. If a system
handler clause performs Async operations, account for that dependency in the
runner type and in child-handler reconstruction. Do not capture a parent's
task identity and accidentally use it for every child call. Native default
selection must not depend on an undocumented search for optional ambient
effects.

Real cancellation acceptance includes blocked accepts, reads and writes where
supported, deadline races, close interrupting a blocked operation, and cleanup
after cancellation. A script that immediately returns a cancellation-shaped
error tests application handling but does not establish that the real native
operation can be interrupted.

Keep application failures, EOF, timeouts, cancellation, and script mismatch
distinct. Decide whether task cancellation is observed through the existing
task cancellation boundary or translated to a transport error at a particular
API; preserve whichever distinction the public operation promises. Retain
the current restrictions on abort-only handlers in child tasks. Parent-level
`Process.Exit` interception cannot simply bypass them.

Whole-request timeout policy, early HTTP responses during upload, and other
HTTP-specific choices remain in the
[client roadmap](roadmap-http-client.md#open-questions). A Net fixture can
provide acceptance scenarios without settling those policies by accident.

## Native and compiler boundary

Prefer ordinary source-defined effect operations plus ordinary system handlers
that call existing private natives. Source operations may exchange the Fango
values supported by the language; that does not mean a new direct native ABI
for every such value is required. The current bundled natives already provide
some special byte, handle, and fallible-result boundaries.

Generalizing native operation defaults remains separate work. If selected,
check payload and reply representations, type descriptors, nominal resource
identity, structured errors, callback effects, and dictionary transport rather
than relying on unchecked conversion. An IO annotation on an ordinary native
value does not supply hidden handler evidence by itself.

Compiler changes must preserve operation identity through imports, reexports,
partial application, module emission, staging, and the interpreter. Verify
that native helpers used by real runners cannot be accidentally selected when
a mock handler is active at the intended public boundary. Conversely, a real
runner must call its private native helper rather than recursively dispatching
through its own public operation.

Preserve Direct/Exit execution, invocation-time evidence selection, typed
resource checks, independent Core lint, and deterministic emitted modules.
The proposal does not authorize reviving Machine/coroutine machinery or
general lifetime analysis. Optimization may bypass an operation only when it
proves that handler selection and externally observable behavior are unchanged.

## Review checklist before representation selection

1. Can the same ordinary function run with real, fake, and mixed services?
2. Do its effect rows reveal every residual real IO dependency?
3. Can a custom handler be written using public checked types alone?
4. Can two providers and two live resources be used together unambiguously?
5. What happens to a foreign, closed, escaped, or forged resource?
6. What exactly is observable after a failure or captured exit?
7. Can inherited handlers share state without lost updates or deadlocks?
8. Which cancellation behavior is simulated, and which is verified on real IO?
9. Do whole-file, TLS, pool, and other native fast paths cross the boundary?
10. Are unsupported sidecar actions and scheduler behavior clearly outside the
    interception claim?
