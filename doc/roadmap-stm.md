# Roadmap: shared state and transactional memory

This document owns the proposed shared-mutable-state layer: transactional
variables, atomic transactions, and the primitives they need. The [effects
roadmap](roadmap-effects.md#deferred-topics) defers shared mutable state to
here; [Async](roadmap-async.md) owns scheduling and executors, while the main
[roadmap](roadmap.md#shared-state-and-transactional-memory) summarizes priority.
Implemented contracts live in [effects](reference/effects.md), [native
sidecars](reference/native.md), and [capture contracts and resource
ownership](design/ownership.md). The APIs below are **proposed**, and nothing
here precedes [structured cooperative tasks](roadmap-async.md#a2-structured-contexts-and-failures).

## Why transactions rather than locks

The proposed task ownership model rejects child captures of parent-local
mutable state, and the implemented language has no shared cell to lock. Adding
one requires choosing a sharing protocol, and transactions fit the existing
rules where locks do not.

- The discipline a transaction needs — no un-replayable effect inside it — is an
  ordinary closed effect row, not a new judgment. A body typed `(Txn ->{Stm, Retry} a)`
  cannot perform `IO`, cannot await, and cannot reach an outer handler.
- `retry` is an abort-only operation in the implemented shape: one
  operation-local type variable as the whole result, absent from payloads. It
  unwinds to the exact `atomically` activation and bypasses `return`.
- Replay re-calls the action; it never clones a continuation. Tail-resumptive,
  single-shot handlers are sufficient, and the capture checker already proves an
  action thunk re-runnable.
- Locks do not compose across two variables; transactions do, which is the whole
  reason to pay for them.
- `retry` also supplies blocking queues, semaphores, and condition variables as
  library code, which is the only proposal that fills the gap left by keeping
  public channels and `select` out of scope.

## Proposed API

```fango
module Stm exposing (Stm, Retry, Txn, TVar, atomically, newTVar, read, write, modify, check, retry, orElse)

effect Retry
    abort retry : () -> value

newTVar    : a ->{IO | e} TVar a
read       : Txn -> TVar a ->{Stm} a
write      : Txn -> TVar a -> a ->{Stm} ()
modify     : Txn -> TVar a -> (a -> a) ->{Stm} ()
check      : Bool ->{Retry} ()
atomically : (Txn ->{Stm, Retry} a) ->{IO | e} a
orElse     : (Txn ->{Stm, Retry} a) -> (Txn ->{Stm, Retry} a) -> Txn ->{Stm, Retry} a
```

```fango
transfer : TVar Int -> TVar Int -> Int ->{IO | e} ()
transfer from to amount =
    atomically \txn ->
        balance = read txn from
        check (balance >= amount)
        write txn from (balance - amount)
        write txn to (read txn to + amount)
```

`Stm` is a marker effect: the accesses are native and perform no operation, but
naming the effect in their rows keeps them from being treated as pure functions
of their arguments, which they are not — a read must observe this transaction's
own earlier write. `atomically` discharges it. The closed callback row, not the
marker, is what excludes `IO`.

The `Txn` handle is what the handle-free spelling below would remove.

## What is ordinary Fango

The whole control layer, with no compiler or runtime knowledge of these names:

- `atomically` is a handler over `Retry` plus a loop: a `Retried` answer calls
  the action again with a fresh transaction, a `return` clause validates and
  commits.
- Rollback needs no new machinery either, but it is a cleanup scope rather than
  handler state, because the write set lives in the sidecar: `Scope.bracket`
  acquires the transaction and abandons an uncommitted one on every exit,
  including an abort aimed at an outer handler and cancellation. Anything the
  handler keeps in its own `with` state is discarded with the activation for
  free.
- `orElse` is a nested handler over `Retry` around a savepoint: the left
  branch runs, and a retry rolls the sidecar back to the savepoint and runs the
  right branch with the left branch's reads retained. Savepoint and rollback
  stay unexported.
- Exposing `Stm` as an abstract label keeps it a marker. Its operations are
  never performed and never exported, so no user module can handle it or
  forge membership in a transaction.
- `check`, `modify`, and derived structures — bounded queue, semaphore, barrier —
  are ordinary library functions over `read`, `write`, and `retry`.

## What must be native

Nothing in Fango expresses shared mutable memory; `State` is a per-activation
snapshot and cannot be shared. One sidecar owns the variable and its version,
the per-transaction read and write sets, and validate-and-commit under a
critical section, in the shape `File` and `Net` already use: an opaque
single-constructor wrapper over `Native.Any` with a `{-# resource #-}` contract
and an unexported constructor.

Two obligations follow from that boundary. The sidecar must be
representation-blind — it stores values it never inspects, because the
interpreter and the Go backend hand it different representations — which is
affordable because validation compares versions and never values. And commit
must be shielded from cancellation, the same shielding
[Async cancellation and cleanup](roadmap-async.md#cancellation-and-cleanup)
defines for release. The suspension-safe resource lifetime itself belongs to
[general coroutines](roadmap-coroutines.md#c5-suspending-acquisition-and-cleanup).

Blocking `retry` is not irreducible. Re-running the transaction after yielding
is correct, and parking on the read set until a conflicting commit wakes it is a
performance addition that reuses the readiness registration protocol rather than
introducing a second one.

## What today's rules block

- **Polymorphic values cannot cross the native boundary.** Parameters and
  results are the scalar set, `Bytes` in bundled sidecars, `Native.Any`, and a
  local single-boundary-value wrapper; a polymorphic variable is a `NATIVE ABI`
  error. `Native.Any`'s constructor is private to its module, so Fango code
  cannot box a value into one either. Scalar transaction payloads need no new
  value representation, but the public `TVar Int` shape still needs the wrapper
  extension below, and sharing needs the checked capability contract. A general
  `TVar a` additionally needs one new boundary kind: a Fango value crossing opaquely
  and returning at the same type — `Native.Any` with a phantom index. That
  extension also keeps the heterogeneous log in Go, where it is a plain `any`,
  so Fango needs no existential. Coordinate this representation contract with
  [typed opaque values](roadmap-coroutines.md#c6a-typed-opaque-values)
  rather than introducing a separate unchecked box for task completion.
- **Wrapper types may not take type parameters.** The boundary check accepts a
  one-constructor, one-boundary-value type declared with no parameters
  (`internal/modules/modules.go`), so `type TVar a = TVar Native.Any` is
  rejected today although its field is monomorphic. Admitting a phantom
  parameter requires a checked promise about its index when the sidecar cannot
  see it. C6a owns phantom-wrapper validation separately from arbitrary opaque
  payload round trips; the scalar stage needs that wrapper contract already.
- **The resource pragma scopes a capability to one owner.** A `TVar` exists to
  be shared with child tasks, so it requires
  [shared and transferable capabilities](roadmap-coroutines.md#c6c-shared-and-transferable-capabilities)
  before even the cooperative scalar stage. Executor-independent capture rules
  do not permit delaying this contract until parallel execution. Executors
  consume the shared native contract rather than providing a TVar-specific
  exception; concurrent runtime safety is the separate C6d gate.
- **The handle-free spelling needs deferred type-system work.** Writing the
  accesses as operations of an `Stm` effect —

  ```fango
  effect Stm
      read  : TVar a -> a
      write : TVar a -> a -> ()
  ```

  — is general operation-local polymorphism, the `fetch : Key a -> a` case, and
  storing a clause's skolem in a handler's state additionally needs existential
  payload scope. Both are deferred in the [effects
  roadmap](roadmap-effects.md#deferred-topics). What it buys is real: no handle
  threading, and a pure sequential interpretation of `Stm` for tests, so a
  concurrent structure can be checked deterministically and then run under the
  transactional interpretation unchanged. It is a type-system project, not a
  library one, and this proposal is the concrete consumer that list asks for.

## Delivery staging

Each stage is usable without the ones after it.

1. **Scalar variables and sharing.** `TVar Int`, `TVar Float`, `TVar String`, `TVar Bytes`
   with the full control layer, spinning `retry`, and a cooperative executor.
   Dependencies: Async A2, C6c's shared-capability contract, and C6a's
   phantom-wrapper validation. Scalar payloads use the existing ABI; do not
   describe that as eliminating the wrapper and sharing prerequisites.
   Acceptance: a transfer that preserves a total across
   contending tasks, and a bounded queue whose consumer blocks on `retry`.
2. **General variables.** C6a's typed opaque-value round trips extend the same
   wrapper to arbitrary `TVar a` payloads. Acceptance: a variable holding a record
   and a recursive ADT, with both backends agreeing.
3. **Parking.** C6b's scoped request/registration contract lets `retry` register
   on its read set and a conflicting commit wake it. Count live registrations,
   and test commit before/during/after publication and cancellation during wait.
4. **Parallel.** After C6d and the corresponding Async executor stage, run the
   same fixtures under `Executor.parallel` and `Executor.mixed`. Both use the
   sharing contract already required by stage 1; this stage validates concurrent
   native transaction access and publication rather than introducing permission
   to share a TVar for the first time.
5. **Handle-free spelling**, if operation-local polymorphism lands for its own
   reasons.

## Open decisions

- Whether `newTVar` requires async evidence — tying a variable's lifetime to the
  root context that created it — or is an ordinary `IO` allocation whose
  lifetime is the program's.
- Whether a transaction may fail as well as retry. Keeping `Fail` out of the
  body's row is the conservative rule, and a body that wants typed failure can
  hold `Fail.attempt` inside the transaction and return a `Result`.
- Whether commit conflicts are invisible or observable: a retry count would make
  contention measurable but makes the result depend on scheduling.
- Whether the marker effect should be public at all, or whether the closed
  callback row alone is the contract and the accesses declare only what the
  optimizer needs.

## Verification

Retain the [repository gates](../AGENTS.md) and
[verification contracts](design/verification.md), including the Core linter, the
interpreter/compiler differential suite, and `go vet`. Fixtures assert final
state and invariants, never interleavings: contention retries are
nondeterministic by construction, and the [Async executor contract](roadmap-async.md#goals-and-boundaries)
does not promise identical concurrent interleaving across executors. A transaction
that retries must show exactly one commit and no partial writes, and a cancelled
transaction must leave no live registration.
