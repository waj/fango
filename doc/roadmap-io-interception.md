# Roadmap: IO interception through domain effects

Status: proposed API and implementation direction, not an implemented language
contract. The purpose is to review the complete caller and handler experience
before changing library APIs. Domain-specific resource handles are selected;
names, fixture types, and signatures below remain candidates.

This proposal addresses the builtin IO interception question in the
[effects roadmap](roadmap-effects.md#deferred-topics). It uses the current
[synchronous handler architecture](design/effects.md), not the historical
owned-coroutine proposal. It does not require non-tail resume, continuation
capture, mixed-discipline effects, or a virtual task scheduler.

## Navigation and ownership

- [API catalogue](roadmap-io-interception-api.md): proposed calls, operation
  boundaries, runners, fixtures, and the disposition of existing public APIs.
- [Resources and execution](roadmap-io-interception-resources.md): virtual
  handles, ownership, scopes, state, cancellation, and native boundaries.
- [Delivery and acceptance](#delivery-and-acceptance): dependencies and
  implementation gates.

These roadmap pages own the proposal. Implemented language behavior remains in
the [effects reference](reference/effects.md), [native reference](reference/native.md),
and [resource reference](reference/resources.md). Implemented library APIs
remain documented in their source comments. As parts land, promote their
architecture and language rules into design/reference, document the library
declarations in source, and remove the corresponding proposed explanation or
mark its milestone DONE without changing its heading.

All code blocks in these proposal pages use `text`: they are design examples,
not fixtures claimed to compile today. Imports and some qualified type names
are omitted for readability. The service effects are `Console`, `FileSystem`
(declared in File), `Network` (declared in Net), and `Arguments` (declared in
Process); examples write them unqualified. Public reexports of
driver-owned effects must preserve nominal identity.

## Intended outcome

Programs should call familiar functions such as `print`, `File.read`, and
`Net.withClient`, while a surrounding runner decides whether requests reach
the OS, an in-memory filesystem, or a custom transport handler. Tests should be able
to replace one service independently and keep the rest real.

The candidate uses small domain effects and explicit runners. `IO` remains the
permission for real native interactions. A real runner translates a domain
effect into `IO`; a completely simulated runner can discharge that domain
without introducing `IO`. It cannot remove unrelated `IO` already present in
the callback's residual row.

This is a migration of selected library boundaries, not a claim that an
arbitrary function annotated with `{IO}` becomes interceptable. In particular,
private OS calls made inside a Go sidecar are not visible to a Fango handler
unless that sidecar exposes a checked boundary for them.

### Why one operation per public function is unnecessary

Formatting and convenience functions remain ordinary Fango. For example,
`Console.print` can display a value, append a newline, and call the console
write operation. The handler sees text, not a polymorphic value plus a
`Display` dictionary. There is no extra operation for every formatting helper.

The same rule applies to buffering, text decoding, HTTP framing, and traversal:
intercept the chosen primitive boundary and let ordinary library code execute
above it. If a convenience function has a native fast path that bypasses those
primitives, either give that fast path an explicit operation or deliberately
reimplement it over the boundary. Do not claim coverage based only on the
function's annotation.

### Existing examples of the pattern

[Random](../stdlib/Random.fango) already separates seeded and system runners.
[Time.Clock](../stdlib/Time/Clock.fango) already separates fixed, stepped,
offset, and system clocks. [Http.Client](../stdlib/Http/Client.fango) already
has a transport effect, raw-byte stubbing, and server-based mocking. Their
contracts are the starting point, not features to reimplement under new names.

The compiler also supports a restricted
[native operation default](reference/native.md#native-effect-operations).
That is a distinct design option from an ordinary Fango handler that calls
private natives. Explicit system runners allow an initial implementation to
use the latter without first generalizing the native operation ABI.

## Scope and coverage

| Area | Candidate disposition | Important boundary |
| --- | --- | --- |
| Console | Add the `Console` effect covering all standard streams, system and test runners | Prelude helpers, stdin source, stdout/stderr sinks; calls never raise errors; `print` remains a helper |
| Files and directories | Add the `FileSystem` effect with file-specific handles and an in-memory runner | Acquisition, handle reads/writes, metadata, enumeration, whole-file fast paths |
| Generic handle IO | Retire the shared `IO.Handle` | Generic code takes `Bytes.Source`/`Sink` or `Reader`/`Writer` |
| Networking | Add the interceptable `Network` effect and system runner | Listeners, connections, TLS, deadlines, pools, cancellation; scripted fixtures are left to libraries |
| Process arguments | Add resumptive `Process.Arguments` | Whole argument list rather than exposing count/index natives |
| Process exit | Status returned from a function-only `main`; `Process.exit` stays a hard native | Normal completion runs cleanup before the status applies; `exit` skips cleanup and requires `IO` |
| Clock and randomness | Keep existing domain effects and runners | Do not equate frozen clock readings with virtual sleep |
| HTTP | Preserve domain mocks; add a runner over interceptable Net | Test either HTTP behavior or the lower transport |
| Async | Preserve native tasks and inherited handlers | Mocked services do not imply deterministic scheduling |
| Reader/Writer/Text adapters | Preserve row-polymorphic APIs | Propagate the effects of their source or sink |
| Other Go sidecars | Retain declared native boundaries | No interception of arbitrary internal Go operations |

New environment-variable, subprocess, filesystem mutation, or OS observation
APIs are outside this inventory. Add them when programs need them and decide
their operation boundary then. The proposal is not a universal sandbox or
security isolation mechanism.

## Complete program experience

### Application code

```text
app : () ->{Console, FileSystem} ()
app() =
    case File.read "greeting.txt" of
        Ok text -> print text
        Err error -> print (IO.describeError error)
```

`File.read` keeps its `Result` contract. This example therefore does not need
`Fail`. The names and call syntax are familiar; the annotation exposes the two
replaceable services instead of collapsing them into `IO`.

### Real services

```text
main : () ->{Console, FileSystem} ()
main() = app()
```

`main`'s row, written here or inferred, lists the services the program uses,
and [entry assembly](#entry-assembly) installs each one's declared default
runner around it. A runner that needs configuration is installed explicitly inside
`main` with `use`, and its effect then does not appear in `main`'s row.

File handles are read and written through File operations, so `File.runSystem`
handles only file access. Standard streams stay with the console runner,
whatever order the runners nest in.

### Simulated services and observable results

```text
fixture = File.withTextFile "greeting.txt" "Hello!" File.empty

test = Console.runTest Bytes.empty {
    File.runMemory fixture { app() }
}

test.stdout == Bytes.fromString "Hello!\n"
```

The candidate console runner returns a transcript; the file runner returns a
snapshot. With this nesting, `test.value.value` is the application's result and
`test.value.image` is the final virtual filesystem. This nesting is an explicit
ergonomic cost. A project-specific test-world helper can flatten it; do not
introduce a compiler-owned universal world merely to hide record projections.

The run is pure only if no residual effects remain. Installing a fake console
around `Async.run` still leaves `IO` for the task runtime. Installing a fake
filesystem does not authorize or erase unrelated native calls.

### Mixed services

```text
main : () ->{Console} ()
main() =
    File.runMemory fixture { app() }
    ()
```

This uses real output and simulated files: the entry supplies the real
Console, and `main` itself handles files. Composition makes handler
dependencies visible: a handler that logs through Console must be
installed inside the Console runner it relies on. Merely handling two effects
in sibling clauses does not allow those clauses to call each other; clauses
run outside all applications handled by their own activation.

## Entry assembly

`main` is a nullary function returning `()` or `Process.Status` (see
[entry points](reference/commands.md#entry-points)). Its row may
contain `IO` and any effect that declares a default handler. The program entry
installs those defaults; a default never applies anywhere else, so a missing
handler in ordinary code remains a compile error.

Like any other definition, `main` may be annotated or not. Without an
annotation, its inferred row selects the defaults, so every service the program
uses is provided. An annotation is exact, as everywhere else, so it limits the
program: a service used anywhere below `main` but missing from its row is a
compile error. An effect without a default is rejected in either case.

An effect declares its default with an ordinary typed attribute whose payload
quotes the runner:

```text
#[DefaultHandler `runSystem`]
effect Console
    readLine : () -> Maybe IO.Line
    readBytes : Int -> Maybe Bytes
    writeBytes : Output -> Bytes -> ()
```

Third-party effects use the same attribute, so a library's `Database` effect
can name its own default. Standard-library and user effects are treated alike.

The compiler hardcodes as little as possible. It knows only that:

- `main` must have the shape above, whether annotated or inferred;
- the program entry is the splice of `Runtime.Entry.assemble`, given `main`'s
  type and a quote of `main`;
- the assembled entry's row may contain only `IO`, and its result type is
  `main`'s.

Everything else is Fango source in `Runtime.Entry`: the `DefaultHandler`
attribute type, and an `assemble` metaprogram that

- reads `main`'s effect row and passes `IO` through;
- reads each other effect's `DefaultHandler` attribute, rejecting an effect
  with none or with several;
- checks each runner's declared type has the shape
  `(() ->{E | e} a) ->{r | e} a`, where `r` contains only `IO` and other
  effects with defaults;
- nests the runners so each one is inside the runners its `r` requires,
  rejecting cycles;
- reports failures at the attribute or at `main`.

This uses the implemented [effect reflection](reference/metaprogramming.md#effect-reflection)
in `Meta` and attribute tags on `effect` declarations.

`main`'s row, annotated or shown by `fango doc` when inferred, states every
service the program can reach. An annotated function deeper in the program
limits its own callees in the same way. `Test.run` is unchanged: a test
program's `main` row decides which real services, such as a database, its
tests may reach, and services a test mocks itself never reach `main`. Annotate
a test program's `main` when tests must not reach real services they forgot
to mock; an inferred row provides them.

## Handler completeness and forwarding

The candidate initially preserves today's complete-handler rule. Every
operation in a handled effect needs a clause. To override one operation and
pass another onward, call the outer operation explicitly:

```text
quiet action =
    handle action() on
        Console.Driver.writeBytes _ _ -> resume ()
        Console.Driver.readLine () -> resume Console.Driver.readLine()
        Console.Driver.readBytes count ->
            resume (Console.Driver.readBytes count)
```

The forwarded calls select the enclosing handler, not `quiet` again. Their
`Console` requirement remains in `quiet`'s surrounding row. A handler that
answers every operation itself can discharge the effect completely.

Automatic omission-and-forwarding is a separate possible language feature.
It would need rules for completeness, residual effects, multiple applied
instances, exhaustiveness, and diagnostics. Do not smuggle it into this library
proposal. Complete handlers mean that adding a public operation requires updating every
handler of the effect; prefer ordinary derived helpers when they suffice.

Driver modules are candidate public authoring APIs, not merely private
implementation names. If users are expected to write custom handlers, the
effect's operation names and all argument/result types they need must be
accessible. Internal native functions and raw runtime tokens need not become
public to accomplish that.

## Design choices requiring review

| Question | Candidate for review | Alternative and cost |
| --- | --- | --- |
| Effect grouping | Small domain effects | One large IO effect shortens rows but makes custom handlers broad; one effect per operation makes rows long |
| Real default | Explicit system runners | Native fallback needs an explicit permission and effect-row story; missing handlers must not silently access the OS in tests |
| Resource API | Selected: domain-specific handles; standard streams are Console operations and generic code uses Source/Sink | A shared handle with a generic stream effect sends every handle to the innermost handler of that effect, whichever runner created it; scoped capabilities need handler-chosen rows ([resources](roadmap-io-interception-resources.md#resource-representation)) |
| File acquisition | Selected: one File effect for acquisition and handle IO | Code choosing between a file and a standard stream takes a sink or writer rather than a shared handle |
| Net grouping | Inventory one driver before splitting | Separate listener/connection/pool effects reduce handler size but complicate dependencies |
| Test results | Typed transcript/snapshot per runner | A generic world is convenient but can obscure residual effects, lifetimes, and error ownership |
| Error policy | Preserve existing Result/Fail boundaries; Console calls never raise and the runner owns stream failures | Uniformly switching to Fail is a separate API migration |
| Exit | Selected: `main` returns `()` or `Process.Status`; `exit : Status ->{IO} a` terminates at once | An abort-only Exit effect would duplicate `Fail` with a status payload and could not cross task boundaries |
| Prelude and entry points | Selected: keep helper names; `main`'s annotated row selects default runners through [entry assembly](#entry-assembly) | Explicit `use` of every runner in `main` is boilerplate in every program; hardcoding standard effects in the compiler would make user effects second-class |

Effect-row aliases are deferred. They could shorten long rows later; no part of
this proposal needs them.
Operation-local class constraints are not needed for console printing when
formatting stays outside the operation. Domain-specific handles need neither
row-kinded effect parameters nor existential rows.

## Migration

Fango has no external users, so this proposal changes signatures and entry
rules directly, with no compatibility wrappers or migration diagnostics.
Bundled code, examples, tests, and goldens are rewritten in the same change.
Functions annotated `{IO}` that use a service change their annotation to the
service's effect; `IO` remains only on code that reaches the host directly.
Preserve the ordering of formatting, argument evaluation, failures, writes,
and cleanup throughout.

Retiring the shared handle changes these calls. Calls such as
`IO.write IO.stderr text` become Console calls, `IO.readLine file` becomes
`File.readLine file`, and functions that accepted any `IO.Handle` take a
`Bytes.Source`, `Bytes.Sink`, `Reader`, or `Writer`. The
[API catalogue](roadmap-io-interception-api.md#retiring-the-shared-handle)
lists the removed declarations.

The entry point becomes function-only, and `main`'s value is no longer printed
by any backend. Value-style fixtures, about half of `testdata/run` and the
`String`-valued multi-module fixtures, become `main() = print (...)` with
unchanged expected output. The WASI build stops printing the value, and the
differential harness drops its hidden print-main compilation mode.

Audit bundled programs, standard-library examples, test framework reporting,
REPL output, debugger/host output, and native-worker transport separately.
Runtime diagnostics and the worker's protocol channel must not accidentally
become application output or be swallowed by an application console handler.
Keep user program output and runtime control communication distinct.

The mock fixture specification must match observable semantics rather than
the physical implementation of the host OS. In particular, define relative
path resolution, text replacement/decoding, line endings, EOF, append/truncate,
directory iteration, timeout units, error identities, and use-after-close.
Document any intentionally unsupported host behavior rather than returning
success with misleading semantics.

## Delivery and acceptance

Milestone IDs and titles below are stable. They form a dependency graph, not
a requirement to delay useful console work until network interpretation is
designed. IOI6 and IOI7 wait for a concrete consumer and the
[shared handler state](roadmap-io-interception-resources.md#mutable-fixtures-and-concurrency)
decision.

| Milestone | Depends on | Deliverable |
| --- | --- | --- |
| IOI1 API and coverage contract | — | Select boundaries, naming, and defaults |
| IOI2 Console interpretation | IOI1 | Console effect covering standard streams, real runner, deterministic input/output runner |
| IOI3 Resource boundary | IOI1 | Provider identity and lifetime for domain-specific File and Net handles |
| IOI4 Files and standard handles | IOI3 | File interpretation, filesystem fixtures, retirement of the shared `IO.Handle` |
| IOI5 Process arguments and exit | IOI1 | Argument runner, function-only `main` returning a status, hard-exit contract |
| IOI6 Network interpretation | IOI3 | Public Net driver with a checked cancellation boundary; no stdlib scripted fixtures |
| IOI7 HTTP and task integration | IOI6, [shared handler state](roadmap-io-interception-resources.md#mutable-fixtures-and-concurrency) | HTTP transport path and inherited mock behavior under real tasks |
| IOI8 Migration and completeness audit | IOI2, IOI4, IOI5, IOI7, IOI9 | Bundled caller migration, documentation, and verified coverage boundary |
| IOI9 Entry assembly | IOI5, [effect reflection](reference/metaprogramming.md#effect-reflection) (done) | `DefaultHandler` attribute, `Runtime.Entry.assemble`, compiler entry splice |

### Implementation order

Land the work in these phases. Each phase leaves every verification gate green,
updates design, reference, and source documentation for what it lands, and is
committed separately.

1. **Effect reflection** in `Meta`. DONE: see
   [effect reflection](reference/metaprogramming.md#effect-reflection) and
   [attributes](reference/metaprogramming.md#attributes).
2. **IOI5.** DONE: see [Process](../stdlib/Process.fango) and
   [entry points](reference/commands.md#entry-points). Until phase 3, programs
   install the Arguments runner explicitly.
3. **IOI9.** Entry assembly, with defaults declared for `Arguments`, `Random`,
   `Clock`, and `Http`. Remove the explicit runner installs that defaults now
   cover.
4. **IOI2.** The `Console` effect and its runners, with its default declared.
   Settle public driver visibility here, the first published driver, and apply
   the same rule to later ones.
5. **IOI4, whole-file part.** Whole-file and directory operations behind
   `FileSystem`, with `runSystem`, `runMemory`, the `Image` fixture API, and
   its default. These need no handles, so they precede IOI3.
6. **IOI3 and the rest of IOI4.** `File.Handle` with provider identity, handle
   operations, `File.source`/`File.sink`, and memory-backed handles. Then
   retire `IO.Handle`, the standard handles, and the generic handle
   operations, migrating every caller.
7. **IOI8, partially.** Migrate the remaining bundled callers and `{IO}`
   annotations, and publish the coverage map for what has landed. The parts
   that depend on IOI6 and IOI7 stay open.

### IOI1 API and coverage contract

Resolve the remaining design choices, including public driver visibility. Turn the operation inventory
into a traceable map from public calls to interpreted operations or explicit
native exceptions. Review representative console, streaming-file, HTTP,
concurrent-server, and test examples before settling internal representations.

Acceptance: no function is called interceptable merely because it has an IO
annotation; unsupported features and behavior changes are enumerated; the
chosen runner signatures preserve residual rows and distinguish pure mocks
from real IO. The other effects-roadmap items remain independently scoped.

### IOI2 Console interpretation

Implement the three-operation `Console` effect covering the Prelude helpers
and all standard-stream access, preserving public printing and line semantics,
and document the test transcript. Implement the
[runner-owned failure policy](roadmap-io-interception-api.md#failure-policy):
the system runner ends the program on a stream error with today's diagnostic,
and Console calls never raise. Verify forwarding, nested handlers, partial
application, missing handlers, EOF, and unrelated residual effects. The
standard-stream `IO` declarations may remain alongside until IOI4 retires them.

Acceptance: the same greeting program runs with real and scripted input;
captured stdout and stderr are exact, including `writeTo` and `Console.sink`;
a real stream failure produces the existing diagnostic outside program output;
the REPL and interpreter worker still own the program's streams; no scheduler
or continuation machinery is added.

### IOI3 Resource boundary

Specify provider identity for the
[domain-specific handles](roadmap-io-interception-resources.md#resource-representation):
`File.Handle`, Net connections, listeners, and pools. Cover construction by
custom handlers, foreign handles within a domain, close behavior, escape,
nested providers, and sharing before implementing a virtual handle payload.

Acceptance: no forged or stale handle can alias a later resource; provider
identity survives module/evidence adaptation; mock-owned state cannot escape
as an apparently pure callable; cleanup and cross-task ownership are explicit.

### IOI4 Files and standard handles

Implement the File boundary with file-specific handles, immutable fixture
construction, and final snapshots. Cover both streaming and whole-file native
fast paths. Specify the handling of handles owned by another File provider
and retain structured error identities. Retire `IO.Handle`, the standard
handles, and the generic handle operations, moving their callers to Console,
File, or Source/Sink parameters. Test-suite programs that write an empty string
to `IO.stdout` only to require `IO` need another small `{IO}` call.

Acceptance: memory fixtures exercise open/read/write/append/close, metadata,
enumeration, EOF, closed/foreign handles, failure after partial progress, and
cleanup precedence. The same parsing helpers work over real and virtual data
through `Bytes.Source` and `Bytes.Sink`. Console and file runners compose in
either nesting order. Mocks never send unsupported resources to the OS as an
implicit fallback.

### IOI5 Process arguments and exit

Status: DONE. The contract lives in [Process](../stdlib/Process.fango) and
[entry points](reference/commands.md#entry-points).

Add independently replaceable arguments. Add `Process.Status`, let a
function-only `main` return `()` or a status, and stop printing `main`'s value,
as described in the
[entry points reference](reference/commands.md#entry-points). Change
`Process.exit` to `Status ->{IO} a` with the documented no-cleanup contract.
Update the entry-point reference and its diagnostics for rejected value-style
and wrongly typed `main` declarations.

Acceptance: a program can finish normally with a non-zero status after its
scopes release and its tasks drain; out-of-range statuses fault at
construction; `exit` skips Fango cleanup in both backends; the compiled
program, `fango run`, the WASI build, the REPL, and the test harness agree on
statuses; argument tests need no real process arguments.

### IOI6 Network interpretation

Publish the Net driver's operation names and types so custom handlers can
interpret networking. Specify a checked public cancellation contract rather
than exposing raw native context payloads. Include TLS, pooling, deadlines,
synchronous and async paths, and foreign connection checks in the coverage
audit. Scripted network fixtures are not a standard-library deliverable;
libraries can build them on the public driver.

Acceptance: a custom handler can answer connections with in-memory bytes that
reach the same framing code as real sockets; cancellation interrupts supported
blocking operations without skipping cleanup; pool reuse cannot cross provider
identities; scheduling simulation remains out of scope.

### IOI7 HTTP and task integration

Add the candidate `Client.runNet` path while keeping existing `run`, `stub`, and
`mock` contracts. Route server transport through the selected Net boundary.
Exercise inherited handlers, cancellation, shared fixture state, and connection
lifetime under existing native tasks.

Requires a decision on [shared handler state](roadmap-io-interception-resources.md#mutable-fixtures-and-concurrency)
before claiming concurrent mock tests.

Acceptance: HTTP domain mocks remain concise; lower-level transport tests reach
the real parser; task mocks preserve operation-level consistency under the
selected concurrency mechanism, without claiming deterministic task ordering; application abort dependencies continue
to obey the checked task boundary.

### IOI8 Migration and completeness audit

Migrate public signatures, bundled callers, Prelude/entry integration, and
native wrappers according to IOI1, annotating every bundled `main` with the
services it uses. Publish the exact coverage map, including
raw sidecar and scheduler exceptions. Move landed contracts to their proper
design/reference/source homes rather than leaving duplicate API specifications
in the roadmap.

Acceptance: all existing correctness gates remain; missing service handlers
produce deliberate diagnostics; no real implementation is accidentally selected
inside a mock; the interpreter and compiler agree on each supported behavior.

### IOI9 Entry assembly

Implement [entry assembly](#entry-assembly): the `Runtime.Entry` module with
its `DefaultHandler` attribute type and `assemble` metaprogram, and the
compiler's splice of the assembled entry. Declare defaults for the standard
effects that have system runners: Console, File, Net, Arguments, Random, Clock,
and Http. Use the same assembled entry for compiled programs, `fango run`, the
WASI build, and the test harness.

Acceptance: a user-defined effect with a default runs exactly like a
standard-library one; an effect in `main`'s row without a default, with two
defaults, with a wrongly typed runner, or in a dependency cycle is a
compile-time failure at the attribute or `main`; annotated and unannotated
`main` select the same defaults for the same row; nested defaults
such as HTTP over Net are ordered correctly; the compiler contains no list of
standard effects.

## Verification strategy

For implementation, preserve the [verification contracts](design/verification.md):
independent Core lint, malformed-IR rejection, interpreter/compiler differential
coverage, functional tests, intentional goldens, deterministic emission, and
`go vet`. Keep `fango doc --stdlib --strict` clean and make source API examples
true under both backends. Attribute tags before `effect` declarations were the
one syntax change, and have landed with the effect reflection phase.

Tests must distinguish mock correctness from handler wiring. A memory fixture
alone cannot establish real socket cancellation or TLS behavior; retain real
loopback acceptance checks. Run the full suite or known network tests with the
repository's required escalated TCP-bind permission on their first attempt.

The callback and operation fast paths must preserve synchronous Direct/Exit
execution. Do not add a goroutine or channel per operation. Measure hot byte IO,
text scanning, HTTP/JSON paths, allocations, and compilation only when the
implementation can plausibly affect performance, on an otherwise idle host.
Ordinary documentation work needs link/anchor and consistency checks, not
timing benchmarks. During ordinary implementation, build-check benchmarks with
`go vet ./benchmarks`; preserve the repository's restrictions on timing runs.
