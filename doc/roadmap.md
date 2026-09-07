# fango roadmap

This document owns priorities, unresolved decisions, and possible future work.
Implemented architecture belongs in [the design](design.md), and functionality
available to users belongs in [the reference](reference.md). Completed work is
removed after durable results are promoted to those documents; Git history is
the archive.

## Standard library expansion

Continue selecting APIs from concrete programs rather than attempting broad
coverage.

- Add `Result`, and expand `List`, text, numeric, and IO operations only as
  subsequent examples require them.
- `String` APIs are byte-oriented (`length` counts bytes); Unicode-aware text
  semantics remain an open decision.
- Prefer fango implementations; use declared bundled natives only for semantics
  source code cannot express or when benchmark evidence demands it.
- Keep adding differential, diagnostic, documentation, and performance
  coverage with each library increment.

Unify bundled natives around the user-sidecar mechanism so a stdlib module's
native code lives in its own `<Module>.native.go` and runs in both backends,
instead of accreting in fangort behind templates and delegators. The proposal
under iteration is in [roadmap-natives.md](roadmap-natives.md).

Independent library versioning, package distribution, dependency fetching,
and configurable source roots remain deferred.

## REPL hardening

- Implement `:load` and `:reload` for complete source files.
- Reconcile values, custom types, constructors, and effects by generation so
  unchanged declarations retain identity while changed generative declarations
  cannot be confused with old values or closures.
- Decide dependency invalidation and whether removed declarations remain
  addressable by existing closures only.
- Connect Ctrl-C to evaluator cancellation without corrupting the session or
  consuming input intended for `readLine`.
- Add transcript coverage for load/reload, cross-generation errors,
  cancellation, handler interaction, and recovery after failures.

## Product polish

- Improve diagnostic specificity and source presentation, especially for row
  inclusion and handler restrictions.
- Add interactive editing and persistent history to the REPL.
- Expand introductory and task-oriented documentation without duplicating the
  normative reference.
- Make benchmark baselines easier to reproduce and less sensitive to machine
  load while retaining meaningful regression gates. Neither performance gate
  runs unattended today, because neither survives a loaded host: the
  runtime-ratio gate's `mapfilter` case swings between roughly 5x and 8x
  against its 4.5 ceiling on one machine depending on whether the rest of the
  suite is running alongside it, so calibrating against a same-host Go
  baseline is not on its own enough. Until that is fixed the gates stay
  manual, and compile latency additionally needs per-host baselines or a
  host-independent formulation.

## Tail calls beyond the self-call loop

Self tail calls of top-level workers compile to loops in both backends (see
the design and reference). Deliberately deferred, each awaiting a concrete
program that needs it:

- **Mutual recursion** (`f` → `g` → `f`): needs fused dispatch loops or a
  trampoline, changes the emitted shape of several defs at once, and
  cross-module workers live in different Go packages that cannot share a
  loop.
- **Monomorphic local recursive closures**: emitted as declare-then-assign Go
  closures with indirect curried calls — a different transform over captured
  mutable locals with no worker ABI to anchor it. Workaround exists: annotate
  so the local generalizes and lambda lifting hoists it, or write it
  top-level.
- **Capture-excluded workers**: definitions rejected only because a closure
  captures a mutated parameter could be re-enabled by copying mutated params
  into per-iteration locals inside the loop; not worth the extra output shape
  until a real program is excluded.
- A diagnostic (or LSP hint) when a loop-shaped function narrowly misses
  eligibility — e.g. via the capture exclusion — is open tooling territory.

## General and aborting handlers

Resume this work when a concrete language feature needs early exit, non-tail
resumption, or escaping continuations. The runtime foundation exists, but the
compiler and interpreter integration should not grow ahead of a consumer.
A second concrete consumer is parameterized handler state (state threaded
through `resume`): it would let `Random.runSeeded` be a pure-fango state
handler instead of advancing a native PRNG cell.

- Permit aborting operation clauses and non-tail continuation use with precise
  one-shot and liveness checks, and choose the fango surface spelling for
  explicit continuation abandonment.
- Support operation-local and result polymorphism through inference,
  elaboration, generated Go, and the interpreter.
- Preserve deterministic evidence passing and lexical restoration across
  nested handlers, closures, translations, and return clauses.
- Define and test cancellation/liveness behavior. Add goroutine-leak tests and
  prove every completion, abort, error, and cancellation path releases any
  continuation runtime resources.
- Keep the current direct, allocation-light tail-resumptive path where it
  remains valid; use benchmark evidence before changing its representation.

Open decisions include the motivating first use case, the surface spelling and
semantics of continuation abandonment, and diagnostics for invalid liveness
transitions.

## Longer-term candidates

These are directions, not commitments or an ordering after the work above.

- Structured concurrency built on effects: nursery scope, futures,
  cancellation, channels, and select semantics.
- Extend the deliberately narrow Go sidecar FFI only from concrete needs:
  richer safe boundary types, explicit effectful imports, interpreter strategy,
  and panic/error translation are all still open.
- Transparent aliases, including whether aliases can abbreviate effect rows.
- Extend nominal records to inline record payloads on variant constructors
  when an example needs named fields on one alternative; the surface syntax,
  construction, matching, and field visibility remain open together.
- Numeric semantics beyond the current `Int`/`Float` model: overflow, integer
  division, conversions, and possible arbitrary precision.
- Extend type classes only from concrete needs: superclasses, method-local
  polymorphism, higher kinds, and default methods remain deferred. Mutually
  recursive deriving groups and richer precedence-aware display are also open.
- Broaden the bounded scalar worker specialization only when benchmarks justify
  it; multiple numeric parameters, effectful workers, and custom dictionaries
  currently retain the generic evidence-passing path.
- A canonical formatter for the layout syntax and an LSP for editor support.
