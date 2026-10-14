# Roadmap: IO interception through domain effects

Status: proposed API and implementation direction, not an implemented language
contract. The purpose is to review the complete caller and handler experience
before choosing representations or changing library APIs. Names, effect
boundaries, fixture types, and signatures below remain candidates.

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
are omitted for readability. In proposed module-local signatures, `Access`
means that module's effect, not one shared global effect. Public reexports of
driver-owned effects must preserve nominal identity.

## Intended outcome

Programs should call familiar functions such as `print`, `File.read`, and
`Net.withClient`, while a surrounding runner decides whether requests reach
the OS, an in-memory filesystem, or a scripted transport. Tests should be able
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
| Console | Add `Console.Access`, system and test runners | Text output and line input; `print` remains a helper |
| Files and directories | Add `File.Access` and an in-memory runner | Acquisition, metadata, enumeration, whole-file fast paths |
| Generic handle IO | Add `IO.Stream`, if retaining the current handle API | Line/byte reads and writes on standard or file handles |
| Networking | Add an interceptable Net driver and test fixtures | Listeners, connections, TLS, deadlines, pools, cancellation |
| Process arguments | Add resumptive `Process.Arguments` | Whole argument list rather than exposing count/index natives |
| Process exit | Consider separate abort-only `Process.Exit` | Test capture and cleanup; deliberate behavior change |
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
app : () ->{Console.Access, File.Access} ()
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
main : () ->{IO} ()
main() =
    use Console.runSystem
    use File.runSystem
    app()
```

`use` already exists. No new runner syntax is proposed. A project may name
this composition with an ordinary helper, such as `runApp`, to avoid repeating
it at each entry point. Such a helper handles a specific chosen set of effects;
it is not a universal eliminator for arbitrary rows.

The conservative file-runner candidate also supplies `IO.Stream` for streaming
file access. Callbacks that only use whole-file operations need not perform
that additional effect themselves.

### Simulated services and observable results

```text
fixture = File.withTextFile "greeting.txt" "Hello!" File.empty

test = Console.runTest [] {
    File.runMemory fixture { app() }
}

test.output == "Hello!\n"
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
main() =
    use Console.runSystem
    File.runMemory fixture { app() }
    ()
```

This uses real output and simulated files. Explicit composition also makes
handler dependencies visible: a handler that logs through Console must be
installed inside the Console runner it relies on. Merely handling two effects
in sibling clauses does not allow those clauses to call each other; clauses
run outside all applications handled by their own activation.

## Handler completeness and forwarding

The candidate initially preserves today's complete-handler rule. Every
operation in a handled effect needs a clause. To override one operation and
pass another onward, call the outer operation explicitly:

```text
quiet action =
    handle action() on
        Console.Driver.writeText _ -> resume ()
        Console.Driver.readInput () ->
            resume Console.Driver.readInput()
```

The forwarded call selects the enclosing handler, not `quiet` again. Its
`Access` requirement remains in `quiet`'s surrounding row. A handler that
answers both operations itself can discharge the effect completely.

Automatic omission-and-forwarding is a separate possible language feature.
It would need rules for completeness, residual effects, multiple applied
instances, exhaustiveness, and diagnostics. Do not smuggle it into this library
proposal. Complete handlers mean that adding a public operation is a source
compatibility event; prefer ordinary derived helpers when they suffice.

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
| Resource API | Preserve handle calls as the baseline | Scoped Source/Sink callbacks reduce generic handle dispatch but change caller APIs |
| File acquisition | One File effect plus shared handle IO | A unified file effect avoids two rows but separates file handles from generic IO operations |
| Net grouping | Inventory one driver before splitting | Separate listener/connection/pool effects reduce handler size but complicate dependencies |
| Test results | Typed transcript/snapshot per runner | A generic world is convenient but can obscure residual effects, lifetimes, and error ownership |
| Error policy | Preserve existing Result/Fail boundaries | Uniformly switching to Fail is a separate API migration |
| Exit | Distinct abort-only effect | Preserving immediate hard exit gives weaker test and cleanup behavior |
| Prelude and entry points | Keep helper names; require explicit runners | An implicit entry runner is convenient but requires a precisely defined installation boundary |

Effect-row aliases could shorten annotations later, but are not a dependency.
Operation-local class constraints are not needed for console printing when
formatting stays outside the operation. Row-kinded effect parameters are not
assumed by the handle-preserving baseline. A capability-based alternative must
prove its callback rows using the existing scoped-callback contract or name the
additional language work it requires.

## Migration and compatibility

The public names can remain familiar while inferred and written types change.
That is still a source compatibility change. Existing callers annotated with
`IO` alone cannot automatically call a new `Console.Access` function: an effect
row is not an inheritance relation in which every domain effect is a subtype
of IO. Runners supply the interpretation.

Before migrating the Prelude, choose one of these explicitly:

- Migrate bundled callers to explicit runners and accept the signature change.
- Introduce opt-in effectful APIs alongside compatibility wrappers that install
  real runners. Such wrappers must be visibly outside the interception claim;
  silently installing a real runner can shadow a test handler.
- Define an entry-point convenience policy, separately specified for compiled
  programs, the interpreter, the REPL, and tests. This must not imply ambient
  defaults for arbitrary missing user effects.

The first option is the clearest candidate; this document does not silently
select a backwards-compatibility shim. Preserve the ordering of formatting,
argument evaluation, failures, writes, and cleanup throughout migration.

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
a requirement to delay useful console work until network simulation is solved.

| Milestone | Depends on | Deliverable |
| --- | --- | --- |
| IOI1 API and coverage contract | — | Select boundaries, naming, defaults, and compatibility policy |
| IOI2 Console interpretation | IOI1 | Console effect, real runner, deterministic input/output runner |
| IOI3 Resource boundary | IOI1 | Chosen handle or capability contract with checked ownership and lifetime |
| IOI4 Files and standard handles | IOI3 | File/handle interpretation, filesystem fixtures, stream capture |
| IOI5 Process arguments and exit | IOI1 | Argument runner and separately reviewed exit semantics |
| IOI6 Network interpretation | IOI3 | Checked transport/cancellation boundary and scripted fixtures |
| IOI7 HTTP and task integration | IOI6 | HTTP transport path and inherited mock behavior under real tasks |
| IOI8 Migration and completeness audit | IOI2, IOI4, IOI5, IOI7 | Bundled caller migration, documentation, and verified coverage boundary |

### IOI1 API and coverage contract

Resolve the design-choice table, including public driver visibility and
whether Prelude functions change immediately. Turn the operation inventory
into a traceable map from public calls to interpreted operations or explicit
native exceptions. Review representative console, streaming-file, HTTP,
concurrent-server, and test examples before settling internal representations.

Acceptance: no function is called interceptable merely because it has an IO
annotation; unsupported features and behavior changes are enumerated; the
chosen runner signatures preserve residual rows and distinguish pure mocks
from real IO. The other effects-roadmap items remain independently scoped.

### IOI2 Console interpretation

Implement the two-operation console candidate, preserving public printing and
line semantics, and document the test transcript. Verify forwarding, nested
handlers, partial application, missing handlers, EOF, and unrelated residual
effects. Keep native error handling in the system implementation and specify
how test failures differ from real terminal failures.

Acceptance: the same greeting program runs with real and scripted input;
captured output is exact; raw `IO.write` is not falsely claimed as covered;
no scheduler or continuation machinery is added.

### IOI3 Resource boundary

Choose between the resource alternatives in
[resources and execution](roadmap-io-interception-resources.md). Specify
construction, identity, foreign handles, close behavior, escape, nested
providers, and sharing before implementing a virtual handle payload.

Acceptance: no forged or stale handle can alias a later resource; provider
identity survives module/evidence adaptation; mock-owned state cannot escape
as an apparently pure callable; cleanup and cross-task ownership are explicit.

### IOI4 Files and standard handles

Implement the selected File and raw-handle boundaries, immutable fixture
construction, final snapshots, and standard-stream transcripts. Cover both
streaming and whole-file native fast paths. Specify the handling of handles
owned by another provider and retain structured error identities.

Acceptance: memory fixtures exercise open/read/write/append/close, metadata,
enumeration, EOF, closed/foreign handles, failure after partial progress, and
cleanup precedence. The same parsing helpers work over real and virtual data.
Mocks never send unsupported resources to the OS as an implicit fallback.

### IOI5 Process arguments and exit

Add independently replaceable arguments. Decide whether exit becomes a
language abort that unwinds cleanup, and expose that semantic change explicitly.
Separate hard termination if retained; it must not masquerade as a capturable
normal return. Define capture results for success, exit status, and unrelated
failures, and the behavior when exit is requested by a child task.

Acceptance: tests inspect an exit request without terminating the test host;
cleanup and task-local abort boundaries follow the selected contract; native
program, interpreter, and REPL entry behavior agree where promised.

### IOI6 Network interpretation

Specify a checked public cancellation contract rather than exposing raw native
context payloads. Include TLS, pooling, deadlines, synchronous and async paths,
and foreign connection checks in the coverage audit. Choose the scripted
fixture vocabulary and event/ordering semantics before claiming deterministic
tests for blocked or concurrent operations.

Acceptance: happy-path bytes and failures use the same framing consumers;
cancellation interrupts supported blocking operations without skipping cleanup;
pool reuse cannot cross provider identities; unsupported scheduling simulation
remains explicitly out of scope.

### IOI7 HTTP and task integration

Add the candidate `Client.runNet` path while keeping existing `run`, `stub`, and
`mock` contracts. Route server transport through the selected Net boundary.
Exercise inherited handlers, cancellation, shared fixture state, and connection
lifetime under existing native tasks.

Acceptance: HTTP domain mocks remain concise; lower-level transport tests reach
the real parser; task mocks preserve operation-level consistency without
claiming deterministic task ordering; application abort dependencies continue
to obey the checked task boundary.

### IOI8 Migration and completeness audit

Migrate public signatures, bundled callers, Prelude/entry integration, and
native wrappers according to IOI1. Publish the exact coverage map, including
raw sidecar and scheduler exceptions. Move landed contracts to their proper
design/reference/source homes rather than leaving duplicate API specifications
in the roadmap.

Acceptance: all existing correctness gates remain; missing service handlers
produce deliberate diagnostics; no real implementation is accidentally selected
inside a mock; the interpreter and compiler agree on each supported behavior.

## Verification strategy

For implementation, preserve the [verification contracts](design/verification.md):
independent Core lint, malformed-IR rejection, interpreter/compiler differential
coverage, functional tests, intentional goldens, deterministic emission, and
`go vet`. Keep `fango doc --stdlib --strict` clean and make source API examples
true under both backends. Add grammar/formatter coverage only if a chosen
language extension actually changes syntax; this baseline proposes none.

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
