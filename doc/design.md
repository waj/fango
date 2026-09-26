# Fango design

Implemented architecture and invariants. Start here, then read only the topics
relevant to the task. [Reference](reference.md) owns observable behavior;
[roadmap](roadmap.md) owns unfinished work. List topic headings with
`rg -n '^#{1,3} ' doc/design`.

## Goals and constraints

Fango is strict, statically typed, and purely functional, with nominal ADTs,
inference, and direct-style algebraic effects. The compiler uses one Go toolchain,
no compiler framework dependencies, local modules, and an experimental library
shipped beside it. Generated representations and calls aim to stay close to ordinary Go.
Laziness, self-hosting, a package manager, and a general optimizer are not implemented.

## Language semantics

[Pipeline and resolution](design/pipeline.md) — strict sequencing, source scope,
nominal identity, parser lowering, and the inference/elaboration boundary.
Exact language rules are in the [reference topics](reference.md).

## Functions and effects

[Effect execution](design/effects.md) — per-arrow timing, handler activation,
abort routing, state, cleanup, and Direct/Exit transport.
[Resources and evidence](design/ownership.md) — runtime resource validity and
structural evidence summaries.

## Compiler pipeline

[Pipeline and resolution](design/pipeline.md) — parsing, fixity, module graph,
Prelude, canonical names, and incremental loading.

## Type inference

[Inference](design/inference.md) — dependency groups, generalization, row inclusion,
instance environments, deriving, and deferred record obligations.

## Compile-time metaprogramming

[Metaprogramming](design/metaprogramming.md) — reflection visibility, hygiene,
completion order, stage-only values, evaluator integration, and rollback.

## Core and evidence invariants

[Core](design/core.md) — typed nodes, dictionaries, residual evidence rows,
adapters, specialization, ANF, and independent lint.
[Native tasks and explicit streams](design/tasks.md) — the checked spawn
boundary, goroutine runtime, IO references, and library traversal.

## Go backend and runtime

[Backend and native runtime](design/backend.md) — representations, List storage,
module-owned output, tail loops, build caching, scalar sidecars, and native workers.

## Formatting

[Formatter](design/formatter.md) — pre-fixity printing, source layout,
comment anchors, verbatim fallback, and round-trip verification.

## Interpreter and REPL

[Interpreter and REPL](design/repl.md) — Core values, display, persistent stores,
import increments, generations, and transactions.

## Testing and performance

[Verification](design/verification.md) — commands, fixture conventions,
differential tests, deterministic emission, and manual timing gates.

## Known limitations

Current restrictions belong beside their [reference contracts](reference.md),
especially [classes](reference/classes.md), [effects](reference/effects.md),
[native sidecars](reference/native.md), and [REPL](reference/repl.md).
Possible extensions and unresolved decisions belong only in the [roadmap](roadmap.md).
