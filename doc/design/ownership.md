# Resources and evidence

[Design index](../design.md). Public rules: [cleanup scopes](../reference/resources.md),
[effects](../reference/effects.md), and [tasks](../reference/library-tasks.md).

## Runtime resources

Bracket cleanup is deterministic for normal returns and language aborts. Native
resource operations validate the handle's current state, including use after
close. The compiler does not prove that a resource, wrapper, or closure cannot
outlive the scope that acquired it. Sidecars own resource validity checks.

The `resource` marker retains nominal native-storage restrictions; it is not a
lifetime proof. Sealed indexed native storage cannot be rewrapped at a different
type. Mutable storage is IO-marked and task transfer checking rejects it.

## Evidence summaries

Core retains structural capture summaries for lexical evidence and explicit
residual-row binders. Lint reconstructs result summaries and verifies evidence
availability and representation after elaboration transforms. These summaries
do not constitute a resource lifetime or retention analysis. There is no
abstract heap, owner-flow graph, coroutine advancement proof, or sharing fixed
point in the compiler.

Effect rows cannot lose an effect merely because a closure was created inside
its handler. Mutable library objects expose IO on their operations. Synchronous
state handlers remain ordinary lexical interpretations of effectful calls.
