# Scoped readers and effect-polymorphic tasks

Unfinished API and type-system work following the
[synchronous foundation](roadmap-simplification.md). The remaining scope and task contracts below are
targets, not implemented APIs. They do not reactivate the coroutine roadmap.
Current behavior remains in [readers](reference/library-readers.md) and
[tasks](reference/library-tasks.md).

## Reader: the complete effect row

The complete reader effect row is now a [reference contract](reference/library-readers.md#reader).
A pure scoped memory constructor remains unfinished. Keep the reader's own
operation row separate from additional effects performed by its consumer.

Retain direct-style cursor advancement. A pure memory runner must create
local cursor state, run the consumer, and discharge only its own state effect.
Multiple readers must remain independently addressable, including when passed
to the same parser. Readers and callbacks retaining their cursor may only be
used within the owning runner.

The executable [memory cursor probe](../testdata/run/effect_api_memory_reader.fango)
uses the actual Reader parsing library with two cursors and a pure parser in
both backends. It uses two distinct nominal effects; it does not
implement a reusable generative reader constructor.

## Fresh scopes and escape checking

A scope runner must introduce a fresh rigid identity selected by the runner,
not by its caller. Its callback must work for that fresh identity, while the
runner's result and residual effects are independent of it. This needs a
locally checked quantification boundary; ordinary rank-one phantom parameters
are insufficient. The [type probes](../internal/infer/scoped_api_test.go)
demonstrate that a caller can return such a phantom token directly, inside a
record/ADT, or inside a callback.

The [solver prototype](design/inference.md#fresh-scope-solver-prototype)
allocates fresh nominal labels, composes two instances through ordinary row
inclusion, and checks non-escape after solving. It rejects returned readers,
latent callbacks, outer storage writes, and residual-effect escape at the
constraint level. It also preserves scope identity through row adaptation.
It does not yet quantify a source callback or emit scoped Core.

Before exposing the API, specify and validate:

- The smallest scope quantification facility that admits runner callbacks,
  without requiring general impredicative types.
- Abstract scope identity in exported callback contracts. The prototype uses
  distinct nominal labels for concrete instances; a reusable runner still needs
  quantification and substitution of that identity. Effect arguments alone do
  not distinguish instances in current rows.
- Local rejection of escape through returned readers, nested data, latent
  callback effects, outer mutable storage, and residual effect rows. Callback
  row widening or abstraction must not erase the identity.
- Correct routing for nested instances, including two instances of the same
  effect. Ordinary type unification must not merge distinct fresh scopes.
- Exported schemes, Core validation, staging, and module serialization carrying
  the same proof. No whole-program retention or lifetime analysis.

Scope identities should erase at runtime. Cursor state should use the existing
synchronous handler cell/captured-local representation, with no mutex or
scheduler. That representation exists today; erasure of the proposed quantified
API is still an acceptance gate, not established by the nominal-effect probe.

## Async orchestration and Task results

Application code should be able to spawn and await with a scheduling effect
alongside Database, Http, or other domain effects, without acquiring IO.
Use Async for that orchestration vocabulary and Task for typed results; exact
public declarations remain to be settled with the representation.

Keep named workers and explicit structurally transferable input/results for
the first implementation. Child workers install their own domain interpreters
from explicit configuration. They do not inherit the parent's handler evidence,
closures, mutable cells, or native handles. A production runner exposes IO at
the application boundary, where native execution and real domain interpreters
are selected.

Provide a pure deterministic runner first by executing submitted work eagerly
and serially, returning completed tasks. This supports business logic tests and
pure domain interpreters. It does not simulate interleavings, cancellation races,
timers, or parallel performance. Native execution retains structured scope
joining and cooperative cancellation from the current Task runtime.

The executable [domain task probe](../testdata/run/task_domain_setup.fango)
runs the same domain-only business function through pure serial and native task
interpretations. Different parent and child configuration verifies interpreter
placement. Job requests and results are monomorphic; this proves composition,
not a generic Async API. Its sentinel failure branches are fixture scaffolding,
not a proposed task error contract.

## Generic scheduling representation gate

Current resumptive effect operations cannot quantify a fresh result type per
operation. Effect header parameters also do not provide a supported row-kinded
budget. Neither a result-polymorphic spawn signature nor an arbitrary child
interpreter can simply be declared as an ordinary effect today.

Choose and prove one representation before publishing generic signatures:

- Checked operation-local polymorphism with an explicit request/result ABI; or
- A narrower checked work package, potentially using a Unit-returning runner
  and a typed result slot, if it can preserve result identity and child effect
  budgets without unsafe casts or generalized native closure transfer.

The latter is a candidate, not an accepted encoding. A package must preserve
the named worker restriction and explicit child interpreter setup. Go's
restrictions on generic function values require an executable cross-module
probe with heterogeneous task result types, not just a type-level sketch.

Prefer one general boundary over compiler recognition of each scheduling
combinator. Keep synchronous handlers, direct Go calls, and the existing native
task runtime; suspension frames and general coroutine machinery are not
prerequisites.

## Delivery gates

1. Validate fresh scope quantification and identity-aware rows with minimal
   compiler/Core probes. Reject all escape routes listed above, including
   callbacks hidden behind abstract wrappers.
2. Implement the scoped pure memory constructor with multiple-reader tests,
   reference contracts, and ordinary File/Net adapter coverage. Reader
   operations already expose their complete row; existing IO-backed
   constructors retain IO in their concrete reader row.
3. Validate heterogeneous task packaging and explicit child interpreter setup
   across modules in the evaluator and Go backend. Preserve worker transfer
   restrictions and reject implicit parent handler inheritance.
4. Deliver the generic orchestration API with pure eager and native runners.
   Specify task failure and cancellation behavior separately from application
   effects; test identical successful business results under both runners.

Preserve Core lint, differential/functional tests, editor grammar when syntax
changes, and vet. Inspect generated Go for ordinary state access and erased
scope identities. Measure performance only on an idle host after there is an
implementation to measure.
