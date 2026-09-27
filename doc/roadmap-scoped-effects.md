# Scoped readers and effect-polymorphic tasks

Remaining task API and type-system work following the
[synchronous foundation](roadmap-simplification.md). Scoped memory readers are
implemented; the task contracts below remain targets. They do not reactivate the coroutine roadmap.
Current behavior remains in [readers](reference/library-readers.md) and
[tasks](reference/library-tasks.md).

## Reader: the complete effect row

DONE

`Reader.withBytes` and complete reader rows are [reference
contracts](reference/library-readers.md#reader). The pure constructor supports
independent nested cursors and preserves additional consumer effects. Existing
IO-based constructors retain their escapable behavior.

## Fresh scopes and escape checking

DONE

The restricted [scoped callback contract](reference/functions.md#scoped-callbacks),
[source escape checks](design/inference.md#scoped-callback-rows), and
[erased local-state boundary](design/core.md#scoped-state-boundary) are implemented.
General first-class rank-two types and recursive scoped runners are outside this
contract; they are not prerequisites for the task API below. Executing scoped
local state during compilation remains limited by the existing sidecar-native
restriction; extending the stage native host is separate unfinished work.

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

1. DONE — Fresh callback rows, permission-aware composition, source escape
   checks, exported contracts, and Core signature validation are implemented;
   see [scoped callbacks](reference/functions.md#scoped-callbacks).
2. DONE — Pure memory readers compose across multiple instances and consumer
   effect rows in both backends; see [readers](reference/library-readers.md#reader).
   Existing IO-backed constructors and File/Net adapters retain their contracts.
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
