# Roadmap: general execution contracts

The joint [C0](roadmap-coroutines.md#c0-control-and-ownership-contracts)/
[A0](roadmap-async.md#a0-library-representation-contract) gate selected general
contracts for typed execution. Implemented scoped behavior belongs in
[Coroutine](reference/library-coroutines.md), [Work](reference/library-work.md),
and [Completion](reference/library-completion.md); this topic owns their
remaining integration into Async.

## General capabilities required before delivery

Owner-sensitive control, detached typed completion/replay, and private owner
stop are described in [the Coroutine design](design/coroutines.md) and
[Completion design](design/completion.md). Plain advancement still propagates
outward aborts. A future Async task wrapper must capture its entire execution
and cleanup before publishing completion; initial Async results and all failure
payloads/reports must be transitively capture-free. General completion preserves
captures and does not make values shareable.

Shared service evidence with a separate implicit invocation argument is
implemented by [Service](reference/library-services.md). Its fixed protocol,
producer slot, nested-pull forwarding, capture rules, and independent Core
proofs are owned by [the design contract](design/shared-capabilities.md).
[Cell](reference/library-cells.md) supplies the scope-owned publication boundary.
Async A1 must combine these facilities with its scheduling and failure policy.

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
[Shared capabilities](design/shared-capabilities.md) define the implemented
transfer boundary.

## Validation boundary

The [source probes](../internal/infer/async_feasibility_test.go) and
[test-only models](../internal/feasibility/) remain prerequisite regression
checks. Production C1 tests add real source inference, module/codec proofs,
malformed Core/Machine, and interpreter/generated-Go execution. C2 must
revalidate A0's representation against the executable API before migrating
Stream. C4 and C6c validate dynamic registration and shared service adapters through
stored closures, dictionaries, and modules; A1 must keep these gates.
