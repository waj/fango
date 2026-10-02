# Structured Go lowering

[Design index](../design.md). Source and checks: [Lowering](../../internal/lower),
[Callback analysis](../../internal/core/callback_abi.go),
[Callback emission](../../internal/codegen/saturated.go),
[Exit ABI](../../internal/codegen/outcome_abi.go),
[Backend checks](../../cmd/fango/lowering_test.go).

## Statement boundaries

Typed Core remains the semantic representation consumed by the interpreter and
Core linter. The Go backend builds an owner-scoped statement representation:
bindings, evaluations, branches, matches, returns, loops, and continues. Match
nodes retain Core decision trees; each leaf has the same tail lowering. Nested
effect regions retain their checked Core payloads and use their scoped emitters.

Lowering ignores ordinary dependency bodies, reconstructs the owner's ABI against
installed dependency summaries, and rejects stale contracts. Its verifier checks
typed operands, worker result types, decision-tree leaves, terminating control
flow, and continues within an eligible self-tail loop. A deterministic dump
records transport, loop eligibility, callback arity/modes, and statements without
graph-local IDs. This representation does not modify Core. A pure call returning a
controlled callback needs that callback's representation family, but does not
execute its latent effects. ABI summaries distinguish this case from invoking
a controlled arrow; the factory still returns normally in an Exit context.

Go emission preserves lexical evidence and row scopes. Eligible immediately
invoked literals are expanded into statement blocks. Arguments are evaluated
once, in source order, before parameter bindings. Typed join temporaries and
labels preserve result conversion and captured local values. Calls with defer,
recover, named results, or variadic arguments keep their function boundary.

## Callback contracts

Each module summarizes function-typed parameters by saturated arity and selected
transport in Direct and Exit worker contexts. Recursive forwarding reaches a
fixed point using only owned bodies and dependency ABI summaries. The summary
is serialized and participates in module/cache compatibility.

A parameter qualifies when every use invokes its complete arity or forwards to
another parameter with a compatible contract. Returning, storing, partially
applying, capturing in a lambda or handler operation, or selecting mixed modes
keeps the general callable ABI. Unused parameters also keep that ABI.

All nonfinal application stages must be fixed Direct, with no evidence or row
arguments. Later source arguments must already be atoms: even a pure intermediate
application can compute or diverge, so flattening must preserve evaluation order.
Evidence and the residual row belong to the final stage and precede the source
arguments in the flat Go function signature.

Workers with a qualifying parameter receive a plain Go function for the selected
mode. A known sequence of lambda bodies becomes one literal with all saturated
arguments; the loop invokes it once without intermediate callable records.
An unknown curried value is evaluated once at the original argument position
and wrapped in one adapter. That adapter executes its original intermediate
applications on every invocation, preserving their behavior and possible costs.
Forwarding an already selected callback passes the same Go function.

Nonrecursive unary local lambdas used only by invocation or compatible forwarding
can be expanded at their call sites. Lambdas capturing evidence or residual rows
keep their definition-site closure. Escaping values and partial application retain
the structural Direct/Exit callable record described by the [backend](backend.md).

## Exit results

Generated Exit workers, operation slots, and callback members return a normal
value and an `*ExitRequest` separately. Nil exit denotes normal completion.
Concrete Unit workers return only the exit pointer; generic and structural
callback signatures retain `(T, *ExitRequest)`, including when T is Unit, so Go
instantiations agree across modules.

Typed call metadata connects emitted calls to their result representation. A
final Go AST pass changes generated signatures, separates local normal/exit
storage, and rewrites propagation and returns. It does not infer conventions
from function names. Runtime helpers, native adapters, task invocation, and
polymorphic conversion boundaries still consume or return `Outcome[T]`; explicit
adapters reconstruct or unpack it at those boundaries.

Small handler operations with no free evidence or rows can be expanded when the
callee is the exact installed lexical evidence value. The family remains available
for residual-row lookup and task rebuilding. Invocation evidence, deferred lookup,
shadowed handlers, child overrides, and polymorphic operations retain dispatch.
State operations retain their existing snapshot/store synchronization.

When a local callable's Direct and Exit members do nothing except forward to
the same effect operation, bound callbacks call that operation slot on their
captured evidence directly. The Go AST rewrite requires stable local binding,
fixed arity, and arguments whose discarded evaluation cannot have effects or
panic; otherwise it keeps the callable. This removes a forwarding invocation,
not the handler operation or its state synchronization. The
optimization-disabled backend keeps the original callable path.

## Verification

The optimization-disabled backend retains the general callable and Outcome
representations for compiled differential comparison. Runnable fixtures also
compare with the interpreter. Structural tests assert flat fold callback
signatures and absence of closure construction in their loops. Allocation tests
require zero allocations for known fold and unary callback loops, with inputs
built outside the measured region. These assertions do not establish runtime
speedup; [performance comparisons](verification.md#go-lowering-comparison) measure
that separately.
