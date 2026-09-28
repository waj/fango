# Roadmap: general execution contracts

Deferred historical proposal. The synchronous redesign supersedes these
contracts; links to removed APIs point to their historical revision. Current
behavior is in [tasks](design/tasks.md) and [effects](design/effects.md).

The joint [C0](roadmap-coroutines.md#c0-control-and-ownership-contracts)/
[A0](roadmap-async.md#a0-library-representation-contract) gate selected general
contracts for typed execution. Implemented scoped behavior belongs in
[Coroutine](https://github.com/waj/fango/blob/2a8f313fec6897824549e0d5074d75d4c6be3441/doc/reference/library-coroutines.md), [Work](https://github.com/waj/fango/blob/2a8f313fec6897824549e0d5074d75d4c6be3441/doc/reference/library-work.md),
and [Completion](https://github.com/waj/fango/blob/2a8f313fec6897824549e0d5074d75d4c6be3441/doc/reference/library-completion.md); this topic owns their
remaining integration with native readiness and suspending cleanup.

## General capabilities required before delivery

Owner-sensitive control, detached typed completion/replay, and private owner
stop are described in [the Coroutine design](https://github.com/waj/fango/blob/2a8f313fec6897824549e0d5074d75d4c6be3441/doc/design/coroutines.md) and
[Completion design](https://github.com/waj/fango/blob/2a8f313fec6897824549e0d5074d75d4c6be3441/doc/design/completion.md). Plain advancement still propagates
outward aborts. The [Async task wrapper](https://github.com/waj/fango/blob/2a8f313fec6897824549e0d5074d75d4c6be3441/doc/design/async-cooperative.md) captures
its entire execution and synchronous cleanup before publishing completion;
initial Async results and all failure
payloads/reports must be transitively capture-free. General completion preserves
captures and does not make values shareable.

Shared service evidence with a separate implicit invocation argument is
implemented by [Service](https://github.com/waj/fango/blob/2a8f313fec6897824549e0d5074d75d4c6be3441/doc/reference/library-services.md). Its fixed protocol,
producer slot, nested-pull forwarding, capture rules, and independent Core
proofs are owned by [the design contract](https://github.com/waj/fango/blob/2a8f313fec6897824549e0d5074d75d4c6be3441/doc/design/shared-capabilities.md).
[Cell](https://github.com/waj/fango/blob/2a8f313fec6897824549e0d5074d75d4c6be3441/doc/reference/library-cells.md) supplies the scope-owned publication boundary.
Async A2 combines these facilities with its cooperative scheduling and failure policy.

## Scoped effects and work packages

The lexical package API and immediate/latent row obligations are implemented
in [Work](https://github.com/waj/fango/blob/2a8f313fec6897824549e0d5074d75d4c6be3441/doc/reference/library-work.md); [ownership design](design/ownership.md#scoped-work-budgets)
owns source-row proofs, membership, and checked evidence projections.

[C4](roadmap-coroutines.md#c4-scope-owned-dynamic-allocation) supplies dynamic
registration and scope-owned cleanup. A dynamic facet must retain the same
owner identity and hidden budget. Registration must install cleanup before
publishing a handle/package, preserve the original typed completion index,
and clear dead execution storage on completion/close. Heterogeneous
task cells and detached-failure injection preserve nominal operation
identity, typed payloads, and all suppressed reports without unchecked casts.
[Shared capabilities](https://github.com/waj/fango/blob/2a8f313fec6897824549e0d5074d75d4c6be3441/doc/design/shared-capabilities.md) define the implemented
transfer boundary.

## Validation boundary

The [source probes](../internal/infer/async_feasibility_test.go) and
[test-only models](../internal/feasibility/) remain prerequisite regression
checks. Production C1 tests add real source inference, module/codec proofs,
malformed Core/Machine, and interpreter/generated-Go execution. C2 must
revalidate A0's representation against the executable API before migrating
Stream. C4 and C6c validate dynamic registration and shared service adapters through
stored closures, dictionaries, and modules; Async retains these gates.
