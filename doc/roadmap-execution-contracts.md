# Roadmap: general execution contracts

The joint [C0](roadmap-coroutines.md#c0-control-and-ownership-contracts)/
[A0](roadmap-async.md#a0-library-representation-contract) gate selected general
contracts for typed execution. Implemented scoped behavior belongs in
[Coroutine](reference/library-coroutines.md), [Work](reference/library-work.md),
and [Completion](reference/library-completion.md); this topic owns their
remaining dynamic-allocation and shared-service integration.

## General capabilities required before delivery

Owner-sensitive control, detached typed completion/replay, and private owner
stop are described in [the Coroutine design](design/coroutines.md) and
[Completion design](design/completion.md). Plain advancement still propagates
outward aborts. A future Async task wrapper must capture its entire execution
and cleanup before publishing completion; initial Async results and all failure
payloads/reports must be transitively capture-free. General completion preserves
captures and does not make values shareable.

- **Shared service evidence with invocation authority**, owned by C6c for A1.
  A captured context cannot simply contain the parent's pause
  closure. Select an opt-in general evidence contract with two separate inputs:
  retained immutable scoped service data, and a non-retainable execution argument
  supplied by the caller. A service operation may submit a request through that
  argument only while its producer is active. Binding the service captures its
  lifetime/context identity, never the execution argument. This requires explicit
  evidence/callable adapter metadata, propagation through helpers, dictionaries
  and modules, and independent Core validation; ordinary captured handlers retain
  their current semantics. C1 must preserve the seam for the C6c implementation.
  No ambient current-task slot or global public suspend function is introduced.

The service contract fixes its request/reply protocol at the effect instance;
an adapter cannot accept an arbitrary coroutine's execution argument. A task
root installs a scoped slot containing its matching pause capability. Calls
thread this slot explicitly, including through a nested stream producer, so an
unfinished pull still suspends to the task. A nested pull does not overwrite the
slot with its differently typed local pause. An independently started child
installs a fresh slot. A captured service remembers its original context data,
but its operation worker receives this invocation slot, not a captured slot.
Reject invocation without a matching active producer, retention of the slot,
and transfer of an existing slot to independently scheduled work. Core lint
checks protocol equality, scope, and producer ancestry on the implicit argument
just as it checks an explicit callback. This is opt-in compiler contract support
for general scoped services; it does not change ordinary handler binding.

## Scoped effects and work packages

The lexical package API and immediate/latent row obligations are implemented
in [Work](reference/library-work.md); [ownership design](design/ownership.md#scoped-work-budgets)
owns source-row proofs, membership, and checked evidence projections.

[C4](roadmap-coroutines.md#c4-scope-owned-dynamic-allocation) supplies dynamic
registration and scope-owned cleanup. A dynamic facet must retain the same
owner identity and hidden budget. Registration must install cleanup before
publishing a handle/package, preserve the original typed completion index,
and clear dead execution storage on completion/close. Future heterogeneous
task cells and detached-failure injection must preserve nominal operation
identity, typed payloads, and all suppressed reports without unchecked casts.
[C6c](roadmap-coroutines.md#c6c-shared-and-transferable-capabilities) extends
transfer beyond the current conservative capability checks.

## Validation boundary

The [source probes](../internal/infer/async_feasibility_test.go) and
[test-only models](../internal/feasibility/) remain prerequisite regression
checks. Production C1 tests add real source inference, module/codec proofs,
malformed Core/Machine, and interpreter/generated-Go execution. C2 must
revalidate A0's representation against the executable API before migrating
Stream. C4 and C6c must validate dynamic registration and shared service
adapters through stored closures, dictionaries, and modules before A1.
