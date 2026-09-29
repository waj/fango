# Effect execution and transport

Handler evidence, state, abort routing, cleanup, and callable execution modes.

[Design index](../design.md). Source and checks: [Control contracts](../../internal/core/control.go), [Evidence](../../internal/core/evidence.go), [Runtime outcomes](../../runtime/fangort/outcome.go), [Control tests](../../internal/codegen/control_test.go), [Cleanup tests](../../internal/core/lint_test.go).

## Arrows and operation discipline

`TFun{Arg, Eff, Ret}` associates effects with each curried arrow. Syntactic
multi-parameter workers run only after their final parameter; a function
returning a lambda may instead perform on its outer arrow. Function values
execute only through application. Native value annotations retain their rows
although their scalar sidecar ABI has no hidden evidence parameter.

Rows contain distinct nominal labels and an optional tail; binding a row
variable can place an effect into a row that already carries it under other
arguments, and the solver reconciles every row a constraint mentions once
the group is solved, unifying the two argument lists. Operation-local
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
and Go backend use the same generic activation cell. Its lock protects each
snapshot and commit separately, including the return clause's snapshot; it is
never held while executing a clause. This provides safe publication of complete
values, not atomic read–modify–write operations. Handler implementations own
operation-level synchronization. No continuation is captured.

An installed activation's evidence carries the transport its own clauses need.
A Direct handler inside an Exit worker still uses Direct evidence; calls adapt
the plain result to the caller's protocol. Open-row callback adapters receive
evidence at invocation, including when constructed inside a matching handler.

Closure compatibility uses effect-row inclusion. Binding a closure to an
activation replaces its nominal label with a fresh local permission plus the
clause effects; it never erases mutable state access to an empty row. Source
escape checks retain that permission until the enclosing inference group is
solved. Optional handler permissions are not introduced into unrelated open
callbacks merely because the handler can supply them. Elaboration captures the
activation evidence when adapting a bound callable. [Resources and
evidence](ownership.md) describes the retained structural metadata.

A scoped runner may receive a callback whose result also mentions its fresh
permission. The runner can consume that result while the permission is active;
its own result, residual effects, and reachable outer bindings remain outside
the scope. Source inference checks those boundaries after solving the group,
and Core lint checks that a scoped call's outward signature has no fresh
permission.

## Abort and cleanup protocol

Abort evidence carries a fresh runtime target pointer to a non-zero-sized
object. Performing evaluates payloads left to right and returns an ExitRequest;
it does not execute the clause. The matching boundary restores definition-site
outer evidence and invokes the clause after unwinding. Foreign exits propagate;
exits from a clause or return transformation never re-enter that activation.
Canonical effect names keep generated envelopes independent of graph-local IDs.

Runtime.Scope.bracket introduces a separate cleanup region. Its release closure retains
definition-site evidence. Direct execution is acquire/body/release with plain
results. Exit execution checks each Outcome: failed acquisition releases nothing;
a body failure remains primary and a failed release is appended through the
copying Suppress operation. Successful-body cleanup failure becomes primary.
Nested cleanup is inner-to-outer. Go defer is not used because ordering depends
on the body's language-level result, not on host function return.

Acquisition, body, and release are synchronous calls and may perform effects.
[Resource semantics](../reference/resources.md) owns failure ordering.

## Direct and Exit

Execution transport is separate from effect rows and operation discipline.
Every Core arrow, callable, application, and evidence slot records a lower bound
and whether its mode is selected by an enclosing control context.

| Transport | Execution |
| --- | --- |
| Direct | Plain value/void result |
| Exit | Checked Outcome carrying a normal value or targeted exit |

Open rows and abstract custom evidence are transport-polymorphic because their
interpretations may exit. Contracts are per arrow and joined, not
one variant for each combination of callback modes. Each defining module emits
its available Direct/Exit workers independently of downstream consumers.

Function values carry typed callable members together in a Go record. Members
below the body's/captured evidence's minimum mode are absent; checked contracts
prevent selecting them. Construction runs no body. Pure curried arrows remain
Direct.

ADTs and dictionaries share one value representation across transport families;
exported family type names are aliases. Pure factories run once and return
complete callback records without executing their bodies. Evidence records also
carry separate members and preserve explicit and captured lexical evidence.
Names use nominal declarations, not graph-local numbers.

A checked Direct call may select a polymorphic worker's Exit member in an Exit
context. RequireNormal projects it and treats an unexpected exit as a compiler
invariant failure. Calling Direct evidence from Exit code wraps a normal result;
a Unit call is sequenced before producing its Unit Outcome. A polymorphic Exit
lower-bound call can target a polymorphic Direct contract because mode selection
chooses the available Exit member.
