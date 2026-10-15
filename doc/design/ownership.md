# Resources and evidence

[Design index](../design.md). Public rules: [cleanup scopes](../reference/resources.md),
[effects](../reference/effects.md), and [tasks](../../stdlib/Async.fango).

## Runtime resources

Bracket cleanup is deterministic for normal returns and language aborts. Native
resource operations validate the handle's current state, including use after
close. The compiler does not prove that a resource, wrapper, or closure cannot
outlive the scope that acquired it. Sidecars own resource validity checks. File
scopes own File.Handle lifetimes; Console operations borrow the active host,
which owns synchronization and shared input buffering. Buffered native file
operations validate direction and state, serialize reads and writes, and allow
close to interrupt a blocked read.

The `resource` marker retains nominal native-storage restrictions; it is not a
lifetime proof. Sealed indexed native storage cannot be rewrapped at a different
type. Shared handlers and native resources own their synchronization.

## File provider identity

File keeps handler activation, resource provider, and cleanup scope separate.
Handler dispatch uses effect identity rather than inspecting handle values, so
File operations never intercept Console calls. Each system or virtual provider
is a fresh native token compared by identity, never an integer. A system handle
wraps a native object recording its owning provider, path, and open state. A
virtual handle is a private Fango value holding its provider, path, and the
handler's backend key; it carries no native state and cannot be constructed
outside File.

The system runner privately creates real providers. Its natives validate owner,
provider activity, and open state before projecting the buffered native file,
and virtual providers cannot open one. A virtual provider only proves
ownership: `handleKey` answers the key of a handle of that provider while it is
active. Whether that key is open is the handler's state, so handlers allocate
keys monotonically; the memory runner drops a cursor on close and treats an
owned key without one as closed. A returned File.Handle carries no live fixture
state, and provider retirement makes every handle of that provider unusable.

Provider retirement uses bracket cleanup on normal return and language abort.
Virtual retirement marks the token inactive; system retirement also closes
remaining raw files in reverse acquisition order. Scoped closes retain the
usual failure precedence and suppressed failures. Raw acquisitions have an
explicit close obligation; teardown invalidates leftovers, and the system
runner faults on its first leaked-resource close error. Already closed native
files are removed from the provider's active-resource list.

Nested providers are closed interpretations. They reject a foreign handle with
a typed error rather than forwarding it or falling back to the OS. Explicit
forwarders retain the outer FileSystem effect. Closing is idempotent within an
active owning provider, including after an injected close failure has already
invalidated the resource.

Inherited handlers share the same provider token. A task drained inside the
provider can use it; a task released after provider exit receives a retired
provider error. Every operation checks provider activity, including whole-file
and metadata calls. An operation admitted before retirement may finish; the
provider check does not serialize whole Fango handler clauses. Provider
teardown is not a task scheduler or a drain scope; Async still owns task
lifetime. Memory handler state remains subject to the existing non-atomic
snapshot/commit rule. Native provider locks protect only provider and
system-handle validity, and are not held during blocking file reads or writes.

Net follows the same model with its own provider type. System listeners,
connections, and pools are provider-owned native objects validated before
projection; virtual ones are Fango values naming their provider and the
handler's key. A pool belongs to one provider, so its leases and idle
connections never cross runners, and each checkout is a fresh lease that
retains its pool: releasing a stale lease cannot affect a later checkout.
System Net retirement closes the remaining sockets and ignores their close
failures. File and Net share the provider core in `fangort` but keep distinct
provider types, so neither domain accepts the other's provider or resources.

## Evidence summaries

Core retains structural capture summaries for lexical evidence and explicit
residual-row binders. Lint reconstructs result summaries and verifies evidence
availability and representation after elaboration transforms. These summaries
do not constitute a resource lifetime or retention analysis. There is no
abstract heap, owner-flow graph, coroutine advancement proof, or sharing fixed
point in the compiler.

Effect rows cannot lose an effect merely because a closure was created inside
its handler. Mutable resource operations expose their service or scoped
permission in their effect rows. Synchronous state handlers remain ordinary
lexical interpretations of effectful calls.
