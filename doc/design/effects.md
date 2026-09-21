# Effect execution and transport

Handler evidence, state, abort routing, cleanup, and callable execution modes.

[Design index](../design.md). Source and checks: [Control contracts](../../internal/core/control.go), [Evidence](../../internal/core/evidence.go), [Activation binding](../../internal/infer/bind.go), [Runtime outcomes](../../runtime/fangort/outcome.go), [Control tests](../../internal/codegen/control_test.go), [Cleanup tests](../../internal/core/synchronous_scope_test.go).

## Arrows and operation discipline

`TFun{Arg, Eff, Ret}` associates effects with each curried arrow. Syntactic
multi-parameter workers run only after their final parameter; a function
returning a lambda may instead perform on its outer arrow. Function values
execute only through application. Native value annotations retain their rows
although their scalar sidecar ABI has no hidden evidence parameter.

Rows contain distinct nominal labels and an optional tail. Operation-local
polymorphism is limited to an abort-only operation's caller-selected result
variable, absent from payloads. Partial operations are pure closures.

Each source effect is uniformly tail-resumptive or abort-only. Source checking
and Core lint separately prove that every normal resumptive-clause path ends
in exactly one owned tail resume. An abort terminal may discharge that normal
obligation. Resume ownership survives equation compilation, nested handlers,
and staging. Normal return transformations do not run on abort answers.
The [reference](../reference/effects.md) owns syntax and diagnostics.
Direct/Exit execution uses ordinary calls and tagged results, without goroutines,
channels, panic sentinels, or continuation objects.

## Handler activations and state

Every activation has a compiler ScopeID as well as its nominal effect identity.
Core evidence names the activation or an abstract capture variable supplied by
a caller. Nested handlers of the same effect are distinct capabilities. Scope
identities erase at runtime; they are not liveness flags.

A parameterized handler owns one mutable cell. Its initializer runs once before
evidence installation. Clauses see immutable snapshots; the body does not. A
stateful resume evaluates result then next state, commits only after both
succeed, and returns through the existing evidence call. Core retains the state
binder/type and update expressions for lint/capture checks. The interpreter
uses an activation cell; Go uses a captured local. No continuation is captured.

Stateless source handlers are durable by default; parameterized handlers are
scoped. Compiler-owned APIs may additionally mark evidence scoped, operation
results borrowed, or arguments retained. All use the same
[capture proof](ownership.md), without trusted runner-name exemptions.

## Binding a closure to an activation

Discharge is the switch between call-site and captured evidence, and the
handler instance rule is the way a program asks for it. Inference carries, on
every argument or field constraint that adapts a lambda written in a handler's
subject, the enclosing activations that subject belongs to. When such an
inclusion can hold only by losing a label one of them handles, the label is
replaced by what that handler's clauses perform and the inclusion is solved
again; the clause row is collected while the clause bodies are generated,
leaving out the `resume` call, whose row describes the perform site's
continuation rather than the clause. Those constraints are solved after the
ordinary bounds, because a subject is generated before the clauses whose row it
inherits. An abort-only label is refused instead: its runtime exit target
belongs to one activation, which a bound abort could outlive.

Nothing is recorded for elaboration. The lambda's row still names the label
while the position it flows into does not, which is exactly the case
`adaptFunctionValue` already answers by substituting the innermost lexical
activation's captures into the body and dropping the lambda's evidence
parameter — the same discharge that gives a `Scope.bracket` release closure its
definition-site evidence. The closure then carries that activation's record of
operation closures, so nested activations of one effect stay distinct without a
special case, and the existing capture proof sees it retaining that scope.

The Go backend materializes an activation's record at the transport of the
worker that installed it. A bound closure whose own row fixes a lower transport
than that worker's therefore has no member to call; the
[roadmap](../roadmap-instances.md) owns that defect and the fix it needs.

> **Invariant.** A pass that reorders, hoists, or shares calls must treat an
> arrow whose parameters or captures include a resource-typed value or a value
> bound to a handler activation as impure, whatever its row says.

Rows are exact about the effects such an arrow performs, not about the state
its activation owns, so a bound `() -> Int` is typed pure and still answers
differently on each call. Discharge leaves the same gap for cursors. Nothing
escapes unhandled — a scoped value exists only inside its own scope, so the
activation is live whenever an operation runs — but the purity claim is
inaccurate, and compile-time evaluation stays contained only because a splice
can reach only an activation it created itself.

## Abort and cleanup protocol

Abort evidence carries a fresh runtime target pointer to a non-zero-sized
object. Performing evaluates payloads left to right and returns an ExitRequest;
it does not execute the clause. The matching boundary restores definition-site
outer evidence and invokes the clause after unwinding. Foreign exits propagate;
exits from a clause or return transformation never re-enter that activation.
Canonical effect names keep generated envelopes independent of graph-local IDs.

Scope.bracket introduces a separate cleanup region. Its release closure retains
definition-site evidence. Direct execution is acquire/body/release with plain
results. Exit execution checks each Outcome: failed acquisition releases nothing;
a body failure remains primary and a failed release is appended through the
copying Suppress operation. Successful-body cleanup failure becomes primary.
Nested cleanup is inner-to-outer. Go defer is not used because ordering depends
on the body's language-level result, not on host function return.

Acquisition/release must be synchronous; actual callback obligations are checked
before row widening. Machine cleanup follows the same primary/secondary rules.
[Resource semantics](../reference/resources.md) owns the complete event table.

## Direct, Exit, and Machine

Execution transport is separate from effect rows and operation discipline.
Every Core arrow, callable, application, and evidence slot records a lower bound
and whether its mode is selected by an enclosing control context.

| Transport | Execution |
| --- | --- |
| Direct | Plain value/void result |
| Exit | Checked Outcome carrying a normal value or targeted exit |
| Machine | Typed frame driven by the private dispatcher |

Open rows and abstract custom evidence are transport-polymorphic because their
interpretations may exit or suspend. Contracts are per arrow and joined, not
one variant for each combination of callback modes. Each defining module emits
its available Direct/Exit/Machine workers independently of downstream consumers.

Function values carry typed callable members together in a Go record. Members
below the body's/captured evidence's minimum mode are absent; checked contracts
prevent selecting them. Construction runs no body. Pure curried arrows remain
Direct, and synchronous Machine members defer work until frame stepping.

ADTs and dictionaries share one value representation across transport families;
exported family type names are aliases. Pure factories run once and return
complete callback records without driving a Machine. Evidence records also
carry separate members and preserve explicit and captured lexical evidence.
Names use nominal declarations, not graph-local numbers. Opaque cursors retain
one owner representation.

A checked Direct call may select a polymorphic worker's Exit member in an Exit
context. RequireNormal projects it and treats an unexpected exit as a compiler
invariant failure. Calling Direct evidence from Exit code wraps a normal result;
a Unit call is sequenced before producing its Unit Outcome. A polymorphic Exit
lower-bound call can target a polymorphic Direct contract because mode selection
chooses the available Exit member.
