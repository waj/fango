# fango roadmap

This document owns priorities, unresolved decisions, and possible future work.
Implemented architecture belongs in [the design](design.md), and functionality
available to users belongs in [the reference](reference.md). Completed work is
removed after durable results are promoted to those documents; Git history is
the archive.

## Standard library

Build the first standard library on the module system so it exercises the same
language and import rules as user code.

- Choose a small initial API from concrete programs rather than attempting
  broad coverage: foundational `List`, `Maybe`, and `Result` operations plus
  the text and IO utilities needed by those programs.
- Decide how bundled library modules are discovered and versioned, and whether
  any prelude names are implicit or every dependency is imported explicitly.
- Prefer fango implementations where they are adequate; add native primitives
  only where semantics or benchmark evidence justify them.
- Give library modules the same differential, diagnostic, and documentation
  coverage as user programs.

Open decisions include provider precedence when bundled and local modules have
the same name, the prelude policy, API stability expectations, and the boundary
between compiler primitives and ordinary fango modules. Package distribution,
dependency fetching, and configurable source roots remain deferred.

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
- Refine `--emit-go` discovery, output routing, and diagnostics.
- Add interactive editing and persistent history to the REPL.
- Expand introductory and task-oriented documentation without duplicating the
  normative reference.
- Make benchmark baselines easier to reproduce and less sensitive to machine
  load while retaining meaningful regression gates.

## General and aborting handlers

Resume this work when a concrete language feature needs early exit, non-tail
resumption, or escaping continuations. The runtime foundation exists, but the
compiler and interpreter integration should not grow ahead of a consumer.

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
- A Go FFI with explicit purity/effect boundaries and panic/error translation.
- Records and transparent aliases, including whether records are nominal or
  structural and whether aliases can abbreviate effect rows.
- Numeric semantics beyond the current `Int`/`Float` model: overflow, integer
  division, conversions, and possible arbitrary precision.
- Typeclasses versus a smaller set of built-in capability kinds; if adopted,
  define coherence and the boxing boundary for higher-kinded abstractions.
- A canonical formatter for the layout syntax and an LSP for editor support.
