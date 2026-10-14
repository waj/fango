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

## Resource representation

Resources use domain-specific handles. Standard streams are Console
operations: input has a single source, and output takes a closed
`Stdout`/`Stderr` choice. File handles are
`File.Handle` values read and written through File operations, and network
connections, listeners, and pools remain Net values with Net operations.
Generic code receives a `Bytes.Source`, `Bytes.Sink`, `Reader`, or `Writer`
whose row names the service behind it.

### Why not a shared handle with a generic stream effect

A handler is selected by an operation's effect, not by the values passed to
it. If every handle were an `IO.Handle` read and written through one stream
effect, each runner able to create handles would have to handle that effect,
and the innermost one would receive operations on every handle, including
those it did not create:

```text
IO.runStandardTest input {
    File.runSystem { app() }      -- would also handle the stream effect
}
```

A write to standard error inside `app` would reach the real file runner and the
real terminal instead of the outer capture. With a memory file runner the write
would be rejected as foreign, or need explicit forwarding in every runner.
Reversing the nesting would make the standard-stream runner receive file
reads. Only a combined runner owning every handle kind would compose.

With domain-specific operations, a standard-stream write is a Console
operation that file runners never handle, and a file read is a File operation
that the console runner never handles. Runners of different services compose
in either order.

### Provider identity within a domain

A wrong-provider handle remains possible only between two runners of the same
domain, such as a memory filesystem nested inside the real one. Standard
streams cannot be forged: Console's stream choice is a closed type with no
payload.

A virtual `File.Handle` or Net value needs an unforgeable association with its
provider, an open or closed state, resource kind, and position or other
resource-specific state. Do not assume that a plain integer index is enough.
Reusing an index after a runner exits must not allow an escaped handle to
alias an unrelated new file. Two independently created memory runners must not
interpret equal integer indices as the same resource. The current
`Http.Client` `Memory Int` connection has this weakness and is not the model.

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

### Scoped capabilities remain deferred

Acquisition could instead pass a row-indexed Source/Sink record whose
operations call the provider selected at acquisition, so no ambient handler
dispatch happens per read. The acquisition operation would then have to return
a record whose row the handler chooses: a real provider's `{IO}`, or a mock's
fresh local permission. Operation signatures cannot express that today; it
needs row-kinded effect parameters or existential rows. Revisit capabilities
only if that language work is undertaken for other reasons.

## Nested providers and foreign handles

Installing a handler changes lexical operation dispatch; it does not grant
ownership of every payload with the same nominal handle type. Because each
domain has its own operations, this question arises only between runners of
the same domain, such as an inner `File.runMemory` seeing a handle opened
under an outer `File.runSystem`, or two nested Net handlers.

Candidate policies:

1. A closed mock rejects every foreign handle with a specified typed error or
   documented resource fault. It never falls back to native IO.
2. A forwarding adapter interprets owned handles and explicitly calls the outer
   operation for foreign handles. The outer effect remains in its row.

The baseline memory runner is closed. A simulated world of standard streams
and files is the ordinary nesting of `Console.runTest` and `File.runMemory`,
in either order.

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

Any retained explicit raw acquisition API needs a stated close obligation and
leak behavior.

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

These guarantees do not apply to a Go panic, to `Process.exit`, which
terminates without Fango cleanup, or to release code that never returns. A
status returned from `main` takes effect only after cleanup has run.

## Mutable fixtures and concurrency

Use immutable values for fixture inputs and returned observations. Mutable
execution state belongs to a runner activation or checked resource backend.
Starting two runners from the same fixture must not share mutable state.
Publishing a snapshot must not expose a buffer that later writes can mutate.

Open question; the decision is a prerequisite of IOI7.

The current [task runtime](design/tasks.md#async-runtime-foundation) can inherit
resumptive handlers and publish their state for synchronized access. Protected
cell loads and stores keep every observed value complete, but do not make an
entire handler clause an atomic read-modify-write operation. Under tasks, a
clause that appends to a snapshot can overwrite another task's commit. This
applies to every stateful handler, not only mocks: `State.run`,
`Random.runSeeded`, `Clock.runStepped`, and `Http.Client.stub` lose updates the
same way. Even atomic operations do not make a sequence such as `State.get`
followed by `State.put` atomic; that needs a single operation such as `modify`.

Candidate mechanisms:

1. Restrict the test runners to sequential use, documenting that concurrent
   use may lose updates.
2. Implement the stdlib test runners natively with internal locks. User mocks
   remain unprotected.
3. Run clauses that perform no effects before their `resume` atomically by
   holding the cell's lock from snapshot to commit. A clause that performs
   effects, such as one with an added debug `print`, silently loses atomicity.
4. Let a handler declare itself atomic, and reject any of its clauses that
   performs effects at compile time. Undeclared handlers keep today's rule.
5. Hold the lock across every clause of a shared handler, faulting on task
   waits inside a clause. Waits through real IO can still deadlock, and slow
   clauses serialize every task using the handler.

Unshared cells use plain accesses and would not pay for any of these. A choice
must answer the following questions for each operation:

| Operation | Required consistency question |
| --- | --- |
| Console write | Is each write appended as one indivisible event? |
| Console read | Can two tasks consume the same scripted line? |
| File read | Does one successful read advance the shared offset exactly once? |
| Append | Are placement and data publication atomic at the promised operation boundary? |
| Close versus read | Which operations may complete, and which observe closed state? |
| Connection write | Can bytes from separate calls interleave? |
| Pool release/acquire | Can a connection be leased to two callers? |

Whatever is chosen, no handler may claim concurrent correctness the mechanism
does not provide, and no task or channel is added to every synchronous
operation simply to serialize it.

Tests should assert per-resource order and valid causal relationships. A
global trace can record observed order but does not make that order repeatable
across native schedules. Fixed clock readings and seeded randomness do not
establish deterministic ordering between tasks.

## Cancellation

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
after cancellation. A custom handler that immediately returns a
cancellation-shaped error tests application handling but does not establish
that the real native operation can be interrupted.

Keep application failures, EOF, timeouts, and cancellation distinct. Decide whether task cancellation is observed through the existing
task cancellation boundary or translated to a transport error at a particular
API; preserve whichever distinction the public operation promises. Retain
the current restrictions on abort-only handlers in child tasks.

Whole-request timeout policy, early HTTP responses during upload, and other
HTTP-specific choices remain in the
[client roadmap](roadmap-http-client.md#open-questions). A custom Net handler
can provide acceptance scenarios without settling those policies by accident.

## Native and compiler boundary

Prefer ordinary source-defined effect operations plus ordinary system handlers
that call existing private natives. Source operations may exchange the Fango
values supported by the language; that does not mean a new direct native ABI
for every such value is required. The current bundled natives already provide
some special byte, handle, and fallible-result boundaries.

Separating Console from File follows the existing runtime split.
`runtime/fangort/io_handle.go` already routes the standard streams to the
session host, so the REPL and interpreter worker own the program's console,
and only file handles use a buffered `*os.File`. Console natives call the
session host directly. File natives keep the buffered handle without its
standard-stream branches. The bounded byte read, the full-write check, error
relabeling, line splitting, and UTF-8 replacement stay shared helpers.

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

## Review checklist for provider identity

1. Can the same ordinary function run with real, fake, and mixed services?
2. Do its effect rows reveal every residual real IO dependency?
3. Can a custom handler be written using public checked types alone?
4. Can two providers and two live resources be used together unambiguously?
5. What happens to a foreign, closed, escaped, or forged resource?
6. What exactly is observable after a failure?
7. Can inherited handlers share state without lost updates or deadlocks?
8. Which cancellation behavior is simulated, and which is verified on real IO?
9. Do whole-file, TLS, pool, and other native fast paths cross the boundary?
10. Are unsupported sidecar actions and scheduler behavior clearly outside the
    interception claim?
