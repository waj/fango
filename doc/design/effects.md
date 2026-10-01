# Effect execution and transport

Handler evidence, state, abort routing, cleanup, and callable execution modes.

[Design index](../design.md). Source and checks: [Control contracts](../../internal/core/control.go), [Evidence](../../internal/core/evidence.go), [Runtime outcomes](../../runtime/fangort/outcome.go), [Control tests](../../internal/codegen/control_test.go), [Cleanup tests](../../internal/core/lint_test.go).

## Arrows and operation discipline

`TFun{Arg, Eff, Ret}` associates effects with each curried arrow. Syntactic
multi-parameter workers run only after their final parameter; a function
returning a lambda may instead perform on its outer arrow. Function values
execute only through application. Native value annotations retain their rows
although their scalar sidecar ABI has no hidden evidence parameter.

Rows contain distinct applied effect labels and an optional tail. Two fully
resolved applications of one nominal effect may coexist. Occurrences whose
arguments still contain type variables overlap until inference resolves them;
the solver unifies their arguments and reconciles rows after the group is
solved. Core keys lexical evidence by the full application. Residual rows and
inherited handler evidence carry type descriptors so runtime projection makes
the same distinction across module boundaries. Source-defined
resumptive operations may quantify unconstrained variables independently at
each call, including variables inside nominal payloads and callbacks. A
handler clause checks those variables as rigid skolems; they may not escape
through the handler result, residual effects, state, or outer bindings.
Abort-only operations retain their caller-selected result-variable rule.
Partial operations are pure closures.

The interpreter passes the call's type descriptors into the clause frame. The
Go backend uses a checked request/reply envelope because an evidence record
cannot contain a Go function field polymorphic at each invocation. Request
values are adapted to the clause's uniform representation and replies back
to the caller's representation; nominal ADTs, Lists, and callback values are
adapted recursively. The reply descriptor is checked before reconstruction.

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
and Go backend use the same generic activation cell. While only the installing
invocation can reach the cell, snapshots and commits are plain accesses. A task
launch publishes every activation its row makes visible, transitively through
clause dependencies, before the task's goroutine starts; from then on the
cell's lock protects each snapshot and commit separately, including the return
clause's snapshot, and is never held while executing a clause. This provides
safe publication of complete values, not atomic read–modify–write operations.
Handler implementations own operation-level synchronization. No continuation
is captured.
The Go backend omits a clause's initial snapshot when its Core body never
mentions the state binder, as in an unconditional cell write. State commits
and their synchronization remain unchanged.

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
results. Exit execution checks each child result: failed acquisition releases nothing;
a body failure remains primary and a failed release is appended through the
copying Suppress operation. Successful-body cleanup failure becomes primary.
Nested cleanup is inner-to-outer. Go defer is not used because ordering depends
on the body's language-level result, not on host function return. A call of
the intrinsic whose three callbacks are literal lambdas over the call's own
lexical evidence is lowered to that sequence at the call site: the lambdas'
row effects bind to the evidence in scope, so no row, callback record, or
evidence lookup is constructed. A call that forwards an open row, passes a
callback value, or whose callback reads its row keeps the ordinary call.

Acquisition, body, and release are synchronous calls and may perform effects.
[Resource semantics](../reference/resources.md) owns failure ordering.

## Direct and Exit

Execution transport is separate from effect rows and operation discipline.
Every Core arrow, callable, application, and evidence slot records a lower bound
and whether its mode is selected by an enclosing control context.

| Transport | Execution |
| --- | --- |
| Direct | Plain value/void result |
| Exit | Normal value or targeted exit; split Go results internally, Outcome at runtime boundaries |

Open rows and abstract custom evidence are transport-polymorphic because their
interpretations may exit. Contracts are per arrow and joined, not
one variant for each combination of callback modes. Each defining module emits
its available Direct/Exit workers independently of downstream consumers.

General function values carry typed callable members together in a Go record.
[Invocation-only callback parameters](lowering.md#callback-contracts) can carry
just the selected member with all verified saturated arguments. Members
below the body's/captured evidence's minimum mode are absent; checked contracts
prevent selecting them. Construction runs no body. Pure curried arrows remain
Direct.

ADTs and dictionaries share one value representation across transport families;
exported family type names are aliases. Pure factories run once and return
complete callback records without executing their bodies. Each handler
activation constructs linked Direct/Exit evidence record pointers and its
immutable row binding once. Calls select the corresponding record instead of
rebuilding adapters. These views share the activation origin and preserve
explicit and captured lexical evidence. Child-task rebuilding still creates
the family against rebased dependencies while sharing the inherited activation's
state. When operation closures have no captured evidence or residual rows,
the activation records its immutable family directly and tasks reuse it.
Explicit child overrides take precedence over this reuse.
Names use nominal declarations, not graph-local numbers.

A checked Direct call may select a polymorphic worker's Exit member in an Exit
context. RequireNormal projects it and treats an unexpected exit as a compiler
invariant failure. Calling Direct evidence from Exit code supplies a normal
value and nil exit; a Unit call is sequenced before its normal result. Generated
Exit workers and callable members return `(T, *ExitRequest)`. A concrete Unit
worker returns only `*ExitRequest`; generic members retain two results even when
instantiated at Unit. Runtime helpers and native adapters retain `Outcome[T]`,
with explicit conversions at those boundaries. A polymorphic Exit
lower-bound call can target a polymorphic Direct contract because mode selection
chooses the available Exit member.
