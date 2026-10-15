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

The explicit annotation on `Stream.foldStep` fixes the combining callback's
first arrow to be pure. Its flat callback can therefore run without constructing
an intermediate callable for each element; a compiled allocation test covers
empty, singleton, and long scalar streams. `Stream.takeStep` is annotated for a
different reason: inferred alone, its counter generalizes over `Num` and `Ord`,
so each step would compare and decrement through class dictionaries even though
`take` supplies an `Int`.

Workers with a qualifying parameter receive a plain Go function for the selected
mode. A known sequence of lambda bodies becomes one literal with all saturated
arguments; the loop invokes it once without intermediate callable records.
An unknown curried value is evaluated once at the original argument position
and wrapped in one adapter. That adapter executes its original intermediate
applications on every invocation, preserving their behavior and possible costs.
Forwarding an already selected callback passes the same Go function.

Nonrecursive local lambdas used only by saturated invocation or compatible
forwarding can be expanded at their call sites. Multiple arguments flatten only
when intervening bodies are pure lambda creation. Alias bindings preserve the
same contract, including every use of the original binding. Lambdas capturing evidence or residual rows
keep their definition-site closure. Escaping values and partial application retain
the structural Direct/Exit callable record described by the [backend](backend.md).

Closed callbacks with a fixed Direct body share a module-local Go function and
an Exit wrapper that supplies a nil exit. Both callable members name these
functions, including inside generic workers and enclosing transport families.
A lexical check permits internal bindings and patterns but rejects outer term
captures; every runtime type and call instantiation must be monomorphic, so
no enclosing type descriptor is captured. The argument and unused invocation
row remain ordinary parameters. Captured evidence, residual rows, runtime
effect parameters, or generic descriptors use the shared factories below.
Effect-polymorphic bodies retain separate transport paths. Keeping closed
callbacks outside generic workers avoids Go dictionary closure allocations
introduced by factory inlining.

A local callable bundle initialized with function literals can split into
individual function locals when all uses select members for direct calls or
function arguments. The bundle must have one declaration, no reassignment,
no address taken, and no whole-value or other member use. Closure creation stays
at the declaration, preserving capture cells and evaluation order. This exposes
the selected function to Go's devirtualizer after a known hidden package has
been eliminated; retaining a struct-field projection can otherwise obstruct
inlining even with zero allocations.

## Bounded effect bodies

The backend shares ordinary transport-polymorphic workers through one Exit
implementation and a checked Direct entry. Direct callers prove normal completion;
the entry projects the result with `RequireNormal`. Module exports retain both
signatures, so this is an owner-local choice with no downstream specialization.
Eligible workers have more than eight Core expression nodes, no function-typed
parameters, and no self-tail loop. Tiny forwarders, loops, and higher-order workers
retain separate bodies: their Direct specialization avoids repeated adapters,
and a bridge can be larger than a small operation forwarder.

General callbacks use module-local closure factories keyed by source lambda and
transport. A factory contains the actual closure body; constructing a callback
passes its captures to the factory. Invocation calls that closure directly.
Nested callback construction repeats factory calls, not nested copies of their
bodies, bounding each general callback to two bodies regardless of nesting depth.
Factories that capture callback records carry a Go inlining barrier: inlining
that construction into a generic cleanup worker can corrupt the captured
callback on Go 1.26 darwin/arm64. Invocation of the returned closure retains
normal Go optimization.
Selected multi-argument callbacks retain their flattened literal, and a task
launch constructs only its selected member, preserving Go's inlining opportunities.
Nested general callbacks still go through shared factories at these boundaries.

Factories capture terms, type descriptors, evidence, and residual rows explicitly.
Unused captures and unrelated enclosing type parameters are omitted. Captured
evidence uses its linked Exit pointer as a canonical family reference; each body
selects the appropriate view without specializing on combinations of captured
handler modes. Invocation-time evidence remains an argument. Recursive bindings
capture their typed cell and read its assigned value on invocation, preserving
recursive initialization. Handler activations installed inside a shared Exit
worker supply a checked Direct view where selected Direct callbacks need one;
other Exit activations keep their existing representation.

Factory names retain the containing Fango function for source traces. Compiler-only
worker bridges have a private line-table marker and are omitted from those traces.
Debug and optimization-disabled emission retain literal bodies. The
[comparison harness](verification.md#effect-body-sharing-comparison) retains the
original emitter and a factory-only control for performance checks.

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
not the handler operation or its state synchronization. When that evidence is
a Direct activation installed in the same function, an Exit member calls the
Direct slot and supplies the nil exit itself instead of entering the Exit view,
and a small clause of a fixed activation is expanded in place of the slot under
the same conditions as an expanded Perform. The
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
