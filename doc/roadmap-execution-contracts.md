# Roadmap: general execution contracts

This topic owns the unimplemented general language prerequisites selected by the
joint [C0](roadmap-coroutines.md#c0-control-and-ownership-contracts)/
[A0](roadmap-async.md#a0-library-representation-contract) feasibility gate.
It is an implementation contract, not implemented design or a new milestone.
Owner-sensitive control remains in the [coroutine discharge contract](roadmap-coroutines.md#discharge-proof-gate),
and dynamic allocation remains in [C4](roadmap-coroutines.md#c4-scope-owned-dynamic-allocation).

## General capabilities required before delivery

- **Scoped effects and checked work packages**, specified
  [below](#scoped-effects-and-work-packages). C1 must preserve a registered
  computation's row and evidence behind an abstract owner capability, and
  reconstruct its contribution at the owning execution boundary. C4 supplies
  the dynamic registration facet. This permits a nullary scheduling effect;
  row-kinded effect parameters and general operation-local polymorphism are
  not prerequisites. An unindexed package is not a pure or unrestricted value.
- **Detached typed completion**, required before C2's A0 revalidation. A general
  owned execution boundary must intercept outward aborts after local handlers
  and cleanup, without unwinding a driver/parent stack. Its private completion
  package is indexed by result type and residual row. For each abort operation
  in that row it preserves nominal operation identity, typed argument tuple,
  inspectability descriptors and nested suppressed reports. It excludes runtime
  exit targets. Internal handlers still catch their own aborts normally.
  Resumptive effects continue through captured or per-advance evidence.
- **Checked replay of completion** takes the row's current evidence and creates
  a fresh exit at the observing execution. It preserves suppressed reports;
  replay cannot call a saved parent/child target. A row-polymorphic helper gets
  a module-owned typed completion/evidence adapter, not an unchecked cast or an
  operation-local polymorphic effect. Both evaluator and emitted Go need checked
  payload projections. Existing `Failure` snapshots alone cannot implement it.
  C1's implementation must cover multiple abort labels, curried argument tuples,
  local handlers and module/dictionary adapters, not just `Fail error`.
- **Private owner-stop outcome** for abandonment/checkpoints, distinct from
  ordinary abort operations and normal completion. Only an execution's current
  driver may initiate it. User handlers cannot catch it as `Fail`; cleanup
  still runs and its typed failures remain reportable. This general C1 outcome
  is used by A2's cancellation policy and later C5/C7; no public cancellation
  operation or new source syntax is selected.
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

The typed-completion boundary has a private mode in addition to the existing
propagate-exit behavior: plain `Coroutine.advance` retains the lifecycle table's
outward exit contract. A library task wrapper uses the general completion
boundary inside its producer, publishes completion, and normally finishes Unit.
The wrapper must enclose coroutine cleanup so a release failure cannot bypass
publication. Successful values and **all** published failure payloads/snapshots
must be transitively capture-free in the initial Async API; a resource hidden in
a suppressed report fails the same check. General completion itself preserves
captures and does not make arbitrary results shareable.

## Scoped effects and work packages

The general facility separates a scoped registration capability from its hidden
effect budget. Write `budget(s) = b` and `need(s, e)` in proof notation: scope
`s` must remain able to execute/drain work whose residual row is `e`. These are
compiler contracts, not new source syntax or public API names. The actual
scope may still have ordinary type `Scope b`; a checked registration facet hides
`b` without forgetting its identity or obligations. Library `Context` can retain
that facet and monomorphic scheduling operations can return it.

**Inference rules:**

- Registering a child with residual row `e` both charges the ordinary spawning
  arrow with `e` and retains `need(s, e)` on its selected owner. This applies
  without an await and through helpers, stored closures and dictionary fields.
- A handler around registration removes only immediate effects. It cannot
  discharge the registered child's future effects. A handler *inside* the child
  is accounted for when computing that child's residual row. In particular,
  handling Fail around spawn cannot make a later child failure target that
  expired handler. Catch an expected failure inside the child to return Result.
- At an owning scope boundary, infer the least row containing the subject's
  residual effects and all `need(s, e)` obligations. Check it against the
  boundary's annotated/exported row. A caller may widen its budget but cannot
  erase a child effect. A nested boundary consumes only its own obligations;
  captured outer owners retain theirs until the outer boundary. Two live scopes
  with equal budgets are still distinct owners.
- Export symbolic owner/row constraints in callable and capture contracts.
  Instantiate them together; join branches and recursive summaries monotonically
  to a fixed point. Do not infer empty from an unknown owner/row, finish inference
  before deferred callback obligations are known, or substitute the current
  context for captured evidence. Ordinary rows keep their existing nominal-label
  identity rule: incompatible parameters of one label still conflict.

Thus `run`'s public `e` includes latent child obligations, even when a handler
in its body masks the immediate charge of spawn. Exact annotations on the
runner's result cannot hide these effects. A reusable helper can carry an
abstract owner's latent obligation in its exported contract, just as it carries
capture obligations; its own handler does not claim to catch that deferred
failure. Diagnostics must name the registered effect and destination owner when
that obligation exceeds an enclosing annotation.

**Package introduction and elimination:**

1. At introduction, recover the selected owner's hidden budget `b`; prove the
   child's `e` is included in it. Retain the concrete typed coroutine, request/
   reply/result protocol, and evidence projection from `b` to `e`. Preserve
   definition-site interpretations separately from per-advance evidence, and
   account for their actual effects and captures. Reject parent-local mutable
   evidence, short-lived resources and captured producer authority using C6c.
2. Also retain a checked typed injection of detached failures from `e` to `b`,
   preserving nominal operation/parameter identities, payload types, descriptors
   and the entire suppressed tree. A task's completion cell remains indexed by
   its original `a` and `e`; packaging never casts the cell to the owner's row.
3. Hide the row in an opaque scoped work package. Its ordinary library wrapper
   can be a uniform `Job`; only the compiler-supported general operations can
   construct/open the package. This is a restricted existential row package,
   not general source existential syntax, a polymorphic effect operation, or
   `Native.Any` plus an integer lookup. Retain owner membership, lifetime and
   exclusive execution obligations through every wrapper and serialized value
   contract. Register cleanup before publishing a handle/package.
4. Opening a package requires its own owner's driver authority and evidence
   for that owner's budget. Its call acquires `drive(s)` and incurs that budget's
   effects; an unindexed package cannot be used to implement a falsely pure
   driver. A pending generic obligation remains in the callable contract until
   substitution proves it. The package is never a freely callable erased thunk.
   Clear frames and adapters on completion/close as in C4.

These rules require new general introduction/elimination and proof metadata in
inference, Core, lint, module codecs, Machine and both backends. The source-facing
declaration spelling of those general intrinsics remains an implementation
review decision; no extra public Async names are introduced at this gate.
The runtime may use private erased registers only with validated projections on
both edges. Core lint must reconstruct row obligations and adapter types from
executable operations, reject missing/stale proofs, and preserve them through
ANF, lifting and specialization. This is not implemented by today's capture
summary or by just deleting an effect parameter.

The [scoped-row model](../internal/feasibility/scoped_rows_test.go) checks ignored
children, local versus child handlers, nested/captured owner routing, symbolic
module round trips, absent/stale obligations, unknown rows and conflicting
effect parameters. Its typed packing prototype uses an explicit budget skolem
`B`, separate child rows `E`, and generated-style projection/injection functions
to construct an unindexed queue. Int/String tasks retain different typed cells;
failure observation and owner drain use fresh evidence and preserve cleanup
reports. Equal-budget foreign owners and effects outside the budget are rejected.
The explicit `B` argument models what the new compiler operation must recover
from a facet; the model does **not** show ordinary Fango can already hide it.

## Validation boundary

The [source probes](../internal/infer/async_feasibility_test.go) check nullary
scheduling arrow shapes and reject an annotation that drops unawaited IO; they
defer actions in closures and do not implement spawning. The
[test-only models](../internal/feasibility/) establish the selected representation's
limited feasibility. They do not validate arbitrary open-row inference, recursive
constraint solving or the production module format. C1 must add real inference,
malformed-Core, module codec,
interpreter and generated-Go fixtures for the new contracts. C6c must validate
shared service adapters and their hidden invocation capability through stored
closures and dictionaries before A1. Neither model authorizes a source program
to use an unimplemented facility.
