# Effect execution and transport

Handler evidence, state, abort routing, cleanup, and callable execution modes.

[Design index](../design.md). Source and checks: [Control contracts](../../internal/core/control.go), [Evidence](../../internal/core/evidence.go), [Runtime outcomes](../../runtime/fangort/outcome.go), [Control tests](../../internal/codegen/control_test.go), [Cleanup tests](../../internal/core/lint_test.go).

## Arrows and operation discipline

`TFun{Arg, Eff, Ret}` associates effects with each curried arrow. Syntactic
multi-parameter workers run only after their final parameter; a function
returning a lambda may instead perform on its outer arrow. Function values
execute only through application. Native value annotations retain their rows
although their scalar sidecar ABI has no hidden evidence parameter.

Effects may have no operations; IO uses this form as an ambient marker for
ordinary native calls. Empty declarations retain nominal identity, row inclusion,
and parameter checking without operation evidence slots.

Rows contain distinct applied effect labels and an optional tail. Two fully
resolved applications of one nominal effect may coexist, including applications
fixed by rigid annotation variables. Occurrences whose arguments still contain
flexible inference variables overlap until inference resolves them;
the solver unifies their arguments and reconciles rows after the group is
solved. An unresolved occurrence that could match several applications is
never matched by label order: the solver retries its constraint after the
others and reports it if it is still ambiguous. Nor is it matched by a
sibling occurrence: an inclusion whose unresolved application enters a row
that may still grow is placed after the other inclusions, so it meets the
row's final applications rather than whichever one a neighbouring call had
already contributed. Core keys lexical evidence by the full application. Residual rows and
inherited handler evidence carry type descriptors so runtime projection makes
the same distinction across module boundaries. Source-defined
resumptive operations may quantify unconstrained variables independently at
each call, including variables inside nominal payloads and callbacks. A
handler clause checks those variables as rigid skolems; they may not escape
through the handler result, residual effects, state, or outer bindings.
Abort operations retain their caller-selected result-variable rule.
Partial operations are pure closures.

The interpreter passes the call's type descriptors into the clause frame. The
Go backend uses a checked request/reply envelope because an evidence record
cannot contain a Go function field polymorphic at each invocation. Request
values are adapted to the clause's uniform representation and replies back
to the caller's representation; nominal ADTs, Lists, and callback values are
adapted recursively. The reply descriptor is checked before reconstruction.
A value built at the clause's uniform representation can also leave through
an abort payload into a failure snapshot. Descriptors of parameterized
inspectable types carry a zero-size rebuilder for the Go instantiation in scope
where they are constructed, and generated constructors expose their fields
independently of instantiation. After `Failure.argument` matches descriptors,
the reader's rebuilder converts the payload to the reader's representation.

Discipline belongs to each operation, so one effect may mix tail-resumptive
and abort operations. Its labels carry whether any operation aborts, which
fixes an Exit floor for every arrow performing it and keeps its activations
out of task inheritance and closure binding. Source checking
and Core lint separately prove that every normal resumptive-clause path ends
in exactly one owned tail resume. An abort terminal may discharge that normal
obligation. Resume ownership survives equation compilation, nested handlers,
and staging. Normal return transformations do not run on abort answers.
The [reference](../reference/effects.md) owns syntax and diagnostics.
Direct/Exit execution uses ordinary calls and tagged results, without goroutines,
channels, panic sentinels, or continuation objects.

## Handler activations and state

Every activation has a compiler ScopeID as well as its nominal effect
identities. One activation may handle several effect applications: Core
`Handle` carries one `EffectInstance` per application, all naming the same
scope capture, and each clause indexes the instance it serves. Lint checks
coverage, duplicates, and operation types per instance. Core evidence names
the activation or an abstract capture variable supplied by a caller. Nested
handlers of the same effect are distinct capabilities. Scope identities erase
at runtime; they are not liveness flags.

Runtime evidence stays per application: the interpreter installs one entry per
instance, and the Go backend emits one record, one origin, and, for aborts, one
exit target per instance. The entries of one activation share its frame, its
outer evidence, and its state cell, and an exit is accepted by the activation
that owns the entry it names. Clauses are found by instance and operation.
Each instance carries the transport of its own clauses, so a resumptive
application beside an abort keeps a Direct record when its clauses are
Direct, while the activation's body runs as an Outcome closure whenever any
clause aborts. An application of a mixed effect is Exit: its record holds
both the exit target and the resumptive clauses' members, and its Direct
view checks those members' results as normal. Only instances without abort
operations get a rebuild path and a share hook; publishing any of them
publishes the shared cell, and an instance with an abort operation remains
uninheritable by tasks.

A parameterized handler owns one mutable cell. Its initializer runs once before
evidence installation. Clauses see immutable snapshots; the body does not. A
stateful resume evaluates result then next state, commits only after both
succeed, and returns through the existing evidence call. Core retains the state
binder/type and update expressions for lint/capture checks. The interpreter
and Go backend use the same generic activation cell. While only the installing
invocation can reach the cell, snapshots and commits are plain accesses. A task
launch publishes every activation its row makes visible, transitively through
clause dependencies, before the task's goroutine starts; from then on the
cell's lock protects each snapshot and commit separately, including the return
clause's snapshot, and is never held while executing a clause. This provides
safe publication of complete values, not atomic read–modify–write operations.
Handler implementations own operation-level synchronization. No continuation
is captured.
The Go backend omits a clause's initial snapshot when its Core body never
mentions the state binder, as in an unconditional cell write. A resume whose
next state is the clause's own state binder commits nothing in any backend:
Core never shadows, so the binder still holds the snapshot, and storing it back
would only overwrite a task's concurrent commit with an older value. Other
commits and their synchronization are unchanged.

An installed activation's evidence carries the transport its own clauses need.
A Direct handler inside an Exit worker still uses Direct evidence; calls adapt
the plain result to the caller's protocol. Open-row callback adapters receive
evidence at invocation, including when constructed inside a matching handler.

Closure compatibility uses effect-row inclusion. Binding a closure to an
activation replaces its nominal label with a fresh local permission plus the
clause effects; it never erases mutable state access to an empty row. Source
escape checks retain that permission until the enclosing inference group is
solved. Optional handler permissions are not introduced into unrelated open
callbacks merely because the handler can supply them. Elaboration captures the
activation evidence when adapting a bound callable. [Resources and
evidence](ownership.md) describes the retained structural metadata.

A scoped runner may receive a callback whose result also mentions its fresh
permission. The runner can consume that result while the permission is active;
its own result, residual effects, and reachable outer bindings remain outside
the scope. Source inference checks those boundaries after solving the group,
and Core lint checks that a scoped call's outward signature has no fresh
permission.

### Atomic state

[`State.runAtomic`](../../stdlib/State.fango) keeps its value outside handler
state, in a native compare-and-swap cell (`fangort.AtomicCell`) that its
clauses capture as an ordinary lexical value. The handler has no `with` state,
so task rebuilding preserves that capture and every inheriting task reaches the
same Go object. Each write publishes a fresh version, and a commit names the
version it read, so a stale commit fails even when the current value is equal
and the state type needs no equality.

Sidecars cannot invoke Fango closures, so `update` is a Fango retry loop over
private natives: load a version, apply the step to its value, and replace the
version or retry from a newer one. The step is therefore pure and may run more
than once; no lock is held while it runs, and no task can block another. The
private natives are typed as pure and reached only through the handler, as
Async's are, and are registered as stage-safe so the runner can execute at
compile time. Runners whose concurrent operations must not lose updates, such
as `Random.runSeeded`, `Time.Clock.runStepped`, `Console.runTest`,
`File.runMemoryWith`, and `Http.Client.stub`, keep a private state type
in an enclosing `runAtomic`, which no caller's own `State` handler can
intercept.

## Abort and cleanup protocol

The [Parser library](../../stdlib/Parser.fango) composes a private resumptive
cursor with aborting failures. A checkpoint saves an immutable token suffix
and its offset. Alternatives determine commitment from the current cursor,
while failures retain their diagnostic offset across explicit rewinds. Each
runner installs its own cursor activation; rollback changes only that cell,
not ambient effects. Repetition rejects successful callbacks that make no
progress. All of this runs through ordinary calls and handler boundaries,
without capturing or replaying a continuation.

Abort evidence carries a fresh runtime target pointer to a non-zero-sized
object. Performing evaluates payloads left to right and returns an ExitRequest;
it does not execute the clause. The matching boundary restores definition-site
outer evidence and invokes the clause after unwinding. Foreign exits propagate;
exits from a clause or return transformation never re-enter that activation.
Canonical effect names keep generated envelopes independent of graph-local IDs.

`Fail.attemptDiagnostic` is a reporting intrinsic like `Fail.attemptReport`,
with an immutable capture-request flag on its abort target and an additional
clause binding for the primary stack. Core lint reserves both the flag and
trace binding for that intrinsic and verifies the `List Trace.Frame` layout;
the ordinary suppressed binding retains the same payload capture summary.
Trace values themselves carry no handler capabilities. An abort resolves its
target and checks that target's policy after evaluating its payload. Only a
requested capture walks the stack, before any unwinding; the exit records both
frames and capture presence, so an empty capture differs from no capture.
Propagation and copying suppression preserve these values. Detached `Failure`
snapshots copy them recursively and inspection never captures a live stack.
Nested ordinary handlers and unrelated effect applications do not inherit the
policy. The public contract belongs to [Fail](../../stdlib/Fail.fango) and
[Failure](../../stdlib/Failure.fango).

Runtime.Scope.bracket introduces a separate cleanup region. Its release closure retains
definition-site evidence. Direct execution is acquire/body/release with plain
results. Exit execution checks each child result: failed acquisition releases nothing;
a body failure remains primary and a failed release is appended through the
copying Suppress operation. Successful-body cleanup failure becomes primary.
Nested cleanup is inner-to-outer. Go defer is not used because ordering depends
on the body's language-level result, not on host function return. A call of
the intrinsic whose three callbacks are literal lambdas over the call's own
lexical evidence is lowered to that sequence at the call site: the lambdas'
row effects bind to the evidence in scope, so no row, callback record, or
evidence lookup is constructed. A call that forwards an open row, passes a
callback value, or whose callback reads its row keeps the ordinary call.

Acquisition, body, and release are synchronous calls and may perform effects.
[Resource semantics](../reference/resources.md) owns failure ordering.

## Direct and Exit

Execution transport is separate from effect rows and operation discipline.
Every Core arrow, callable, application, and evidence slot records a lower bound
and whether its mode is selected by an enclosing control context.

| Transport | Execution |
| --- | --- |
| Direct | Plain value/void result |
| Exit | Normal value or targeted exit; split Go results internally, Outcome at runtime boundaries |

Open rows and abstract custom evidence are transport-polymorphic because their
interpretations may exit. Contracts are per arrow and joined, not
one variant for each combination of callback modes. A resumptive handler's
transport joins its subject, state initializer, clauses, and return clause:
its residual row is only an upper bound, which an enclosing annotation can
widen with effects nothing inside performs. An abort handler consumes its own
subject's exits, so its residual row decides which of them continue outward. Each defining module emits
its available Direct/Exit entries independently of downstream consumers.
[Bounded body sharing](lowering.md#bounded-effect-bodies) lets ordinary workers
share their implementation while retaining selected Direct specializations for
small forwarders, loops, and higher-order functions.

General function values carry typed callable members together in a Go record.
[Invocation-only callback parameters](lowering.md#callback-contracts) can carry
just the selected member with all verified saturated arguments. Members
below the body's/captured evidence's minimum mode are absent; checked contracts
prevent selecting them. Construction runs no body. Pure curried arrows remain
Direct.

ADTs and dictionaries share one value representation across transport families;
exported family type names are aliases. Pure factories run once and return
complete callback records without executing their bodies. Each handler
activation constructs linked Direct/Exit evidence record pointers and its
immutable row binding once. Calls select the corresponding record instead of
rebuilding adapters. These views share the activation origin and preserve
explicit and captured lexical evidence. Child-task rebuilding still creates
the family against rebased dependencies while sharing the inherited activation's
state. When operation closures have no captured evidence or residual rows,
the activation records its immutable family directly and tasks reuse it.
Explicit child overrides take precedence over this reuse.
Names use nominal declarations, not graph-local numbers.

A checked Direct call may select a polymorphic worker's Exit member in an Exit
context. RequireNormal projects it and treats an unexpected exit as a compiler
invariant failure. Calling Direct evidence from Exit code supplies a normal
value and nil exit; a Unit call is sequenced before its normal result. Generated
Exit workers and callable members return `(T, *ExitRequest)`. A concrete Unit
worker returns only `*ExitRequest`; generic members retain two results even when
instantiated at Unit. Runtime helpers and native adapters retain `Outcome[T]`,
with explicit conversions at those boundaries. A polymorphic Exit
lower-bound call can target a polymorphic Direct contract because mode selection
chooses the available Exit member.

## Service effects

Host services are small domain effects declared in their own modules, such as
`Console.Console`, `File.FileSystem`, `Net.Network`, and `Process.Environment`,
beside the older `Time.Clock.Clock`, `Random.Random`, and `Http.Client.Http`.
Their operations are public so custom handlers name them directly; there are
no separate driver modules. One effect per service keeps rows short and lets
a custom handler answer only its domain: a single IO effect would make every
handler broad, and one effect per operation would make rows long. Net keeps
listeners, connections, and pools in one effect because their operations act
on one another's resources.

Operations sit at primitive boundaries: bytes, lines, handles, and whole-file
calls. Formatting, buffering, text decoding, HTTP framing, and traversal stay
ordinary Fango above them, so `print` is a helper rather than an operation. A
convenience with a native fast path gets an operation of its own, as whole-file
reads and writes do, instead of bypassing the effect. `IO` marks only code that
reaches the host: the system runners and the
[direct boundaries](../reference/services.md#direct-native-boundaries). An `IO`
annotation never makes a call interceptable, and actions private to a sidecar
are visible to no handler.

Each service has an explicit system runner, `(() ->{S | e} a) ->{IO | e} a`,
declared as its default handler, which [entry assembly](pipeline.md#entry-assembly)
installs for effects in `main`'s row. Nothing reaches the host implicitly
anywhere else, so a missing handler is an unhandled-effect diagnostic rather
than a silent system call. Simulated runners discharge the service without
`IO`, return typed observations of their own, such as a transcript or an image
snapshot, rather than a generic world, and are never defaults. Every runner
removes only its own effect and keeps the residual row, so a test replaces one
service and keeps the rest real.

Handlers stay complete: a handler that overrides one operation forwards the
others by calling the outer operation explicitly. Resources are domain-specific
handles with [provider identity](ownership.md#file-provider-identity); standard
streams are Console operations, and generic code takes a Source/Sink or
Reader/Writer. Each API keeps its existing `Result` or `Fail` boundary, and
Console never raises: its runner owns stream failures. A program's exit status
is `main`'s result, and `Process.exit` stays a hard native that skips cleanup.

Simulated runners model observable semantics rather than the host's
implementation, and their source comments state what they leave out, such as
permissions, symlinks, TLS, and timing. Runners shared by tasks keep their
state in [atomic state](#atomic-state), so each operation is consistent; none
simulates scheduling, since tasks are real goroutines.

## Standard streams

The program's standard streams are the `Console` effect, so printing is an
ordinary resumptive operation rather than a native call: a function that
prints receives Console evidence and, like any code over a resumptive effect,
has Direct and Exit members. The operations carry bytes; `print`'s display
and encoding stay in Fango above them. `Console.runSystem` answers them with
private native calls directly on the active session host, and turns a stream
error into the host panic that ends the program, so no Console call raises
and the diagnostic never becomes program output. Programs get that runner as
`main`'s [default handler](pipeline.md#entry-assembly), and the REPL applies
defaults to its inputs the same way. `Console.runTest` keeps its remaining
input and captured output in a private [atomic state](#atomic-state) runner,
so tasks sharing it consume each input line once and capture every write
whole.

A callee row that lists a label and also has a tail, such as a runner's
`{Out | e}`, forwards that label's evidence in the residual row too. Rows
hold distinct applications, so where `e` also holds the label the call site
sees it once, and a callback typed with `e` finds it only through that row.

## File services

[File](../../stdlib/File.fango) defines the public `FileSystem` effect for
acquisition, streaming reads/writes and close, whole-file text reads/writes,
directory tests, byte sizes, and sorted directory listings. Helpers call these
operations, including the whole-file native fast paths. Its default `runSystem`
handles them with private IO natives and a fresh resource provider. Directory
acquisition and bracket cleanup remain private to the runner. Entry assembly
and the REPL select this default through the ordinary effect attribute.

`runMemoryWith` holds immutable path entries, per-open cursors, and its fault
configuration in a private [atomic state](#atomic-state) runner, and each
operation, whole-file calls included, is one pure update of it; `runMemory`
is the fault-free case.
Its returned image contains only entries, so a snapshot can seed another run
without carrying faults or live handles. Fixture construction may create parent
directories; writes require existing parents. Whole-file mock operations use
the same acquisition/read/write/close machinery as streaming, including fault
injection and partial writes. File's native text conversion preserves its
replacement-per-invalid-run policy in both providers.

Each runner removes only `FileSystem` and preserves the residual row. Console
and File runners compose independently in either order. `Runtime.IO` shares the
raw line/byte EOF conversions and read bound. Generic code takes a row-indexed
Source/Sink, Reader, or Writer; there is no shared IO handle.

Because each operation is one atomic update, tasks that inherit the memory
runner never lose a write or read the same bytes twice through one handle;
provider validity synchronization is separate from that state. Provider
identity, retirement, task inheritance, and checked payload projection are
specified in the [resource design](ownership.md#file-provider-identity).

## Network services

[Net](../../stdlib/Net.fango) defines the public `Network` effect for
listeners, accepts, connects, byte transport, deadlines, and pools. Scoped
helpers, sources, and sinks call these operations, and `runSystem`, the
default, handles them with private natives under a fresh provider. A custom
handler creates its own resources through `withProvider`, as File's do.

Cancellation crosses the operation boundary as a `Net.Cancel` argument to
accepts, reads, and writes: either uncancellable or a task lifetime taken from
`Async.contextToken`. It is an ordinary value, so `runSystem` needs no `Async`
and a synchronous caller never acquires one. The system clause passes the
lifetime to a native that closes the socket if the lifetime ends during the
call. The `Async` helpers then checkpoint, so a cancelled task ends cancelled
instead of raising the interrupted call's error; other failures stay Net
errors, and deadlines stay `TimedOut`. A custom handler observes the same value
with `isCancelled`. Answering it with an immediate error tests application
handling, not native interruption, which loopback tests cover.

`Http.Client.run` and `Http.Server.serve` perform `Network` rather than `IO`,
so entry assembly nests the Net default outside HTTP's and a custom Network
handler reaches the same HTTP framing as sockets do.
