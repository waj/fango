# Compile-time metaprogramming

Expansion, reflection visibility, stage execution, and rollback invariants.

[Design index](../design.md). Source and checks: [Stage hook](../../internal/staging/staging.go), [Templates](../../internal/meta), [Reflection natives](../../internal/natives/meta.go), [Stage lint tests](../../internal/core/stage_lint_test.go), [REPL tests](../../internal/repl/repl_test.go).

## Expansion and hygiene

Splices expand during inference so both backends see identical generated code.
Quotes use the quoting module's resolved AST with original spans plus ordered
hole expressions; internal/meta owns templates and opaque Code values. Scalar
lifting builds AST fragments through the same expansion path. Generator spans
remain available for diagnostics.

Resolution canonicalizes module references before inference. Generated names
therefore use the quoting module's scope and neither capture nor are captured
at the splice site. Quote depth distinguishes holes from splices; stage depth
distinguishes compile time from runtime. Each has one level. Locals belong to
their introduced stage; top-level definitions are stage-polymorphic. Source
rules and builders live in [the reference](../reference/metaprogramming.md).

## Reflection and dependency boundaries

Meta.TypeRepr carries nominal identity and structural type, including arguments
and arrow effects. Equality never uses display text. Reflection retains the
schema visibility at the typeOf site and carries it into arguments/fields;
opaque imports remain opaque. A narrow checker-table interface supplies schema
facts; there is no ambient name reification or instance-existence query.

Pure reflection/code-building natives return scalars and opaque handles. Meta's
Fango code assembles schema records and its own Items list, keeping natives below
eval in the package graph and avoiding Meta -> List -> Derive -> Meta cycles.
The loader adds Meta for quote/splice/typeOf and Derive for deriving. Meta depends
only on Basics; Derive supplies ordinary Fango Eq/Ord/Show generators.

## Compile-time-only values

Quote and typeOf are explicit compile-time Core operations. Definitions whose
types mention Code are not emitted, and runtime definitions may contain no such
expression. Compile-time-only types, dictionaries, instances, and helpers are
excluded transitively; Derive emits nothing, while Meta's ordinary Items/folds
may remain. Runtime Core lint rejects quotes, reflected constants, and typeOf.
This type-directed boundary matters because codegen emits every Prog.Def and
has no dead-code elimination; consumers must not change a dependency's output.

## Evaluator and completion order

internal/staging connects the checker's hook to elaboration/evaluation without
an inference-to-evaluator package cycle. Batch and REPL install the same hook.
It elaborates completed dependency groups on demand, installing all members
before capture analysis/evaluation. Checked is an append-only completion log,
not a source prefix. The shared batch session also snapshots each completed
module's declarative stage Core for its installable module object; this includes
compile-time-only definitions and ordinary functions a later splice may call.

Operands may execute only completed groups whose declarations and transitive
dependencies precede the splice. Source checks and the elaborated closure,
including dictionary calls, enforce that restriction. Quotes may describe later
functions; expanded references enter final dependency analysis. Declaration
cutoffs preserve concrete evidence between staging and final elaboration; operands
use instances visible at the splice site.

Stage-specific semantic lint admits checked quotes/reflected values and lowers
the exact operand plus reachable completed definitions through the same Machine IR.
A deriving dictionary still being expanded is not executable. The stage environment
retains elaborated definitions for capture substitution and lowering, incrementally
installs imported intrinsics, and rebuilds after rollback. A cached stage section
may omit an unused intrinsic; installation records only definitions it contains,
so the evaluator materializes any newly needed intrinsic before a later splice.

Cached stage installation appends validated templates and completed Core groups
directly, without replaying `DeclInfo` AST through elaboration. Group metadata
retains stable instance cutoffs and dictionary availability. Installed Core is
the evaluator's rollback base: a later failed splice discards speculative
definitions but not dependency objects. `TypeRepr.Schema` is deliberately not
serialized; installation reconnects it to the current checker while preserving
the semantic difference between unrestricted nil visibility and a non-nil empty
visibility set. Native stage-safety metadata and the evaluator's ordinary step
budget follow the same paths after installation.

## Reproducibility and rollback

An operand must have an empty residual effect row; locally handled failures/state
are allowed. Native metadata separately marks stage safety: seeded Random is safe,
system entropy and user sidecars are not. One evaluation-step budget covers Core,
tail loops, and producer machines, even non-yielding loops. These boundaries keep
emission reproducible.

Checker checkpoints restore the completion log, capture summaries, and declaration
environment and invalidate corresponding evaluator state. Failed splices/derivers
leave no prompt declaration or derived type behind.

Json.Encode exercises ordinary reflection/quotes without declaration generation.
Only escaping, finite-float formatting, and leading-string validation cross its
native boundary. Deterministic schema-order/tagged output is specified in
[JSON](../reference/library-json.md); decoding remains application Fango code.
