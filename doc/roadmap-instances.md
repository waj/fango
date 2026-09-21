# Roadmap: handler instances

The typing rule that addresses a *specific* handler activation through a value
is implemented. [Effects](reference/effects.md#binding-a-closure-to-a-handler-activation)
owns its behavior and diagnostics; [effect execution](design/effects.md#binding-a-closure-to-an-activation)
owns the binding mechanism and the optimizer invariant it forces. This document
now owns only what is left: the wrapper shape the [byte IO layer](roadmap-io.md)
needs, and one backend defect the rule exposes. It replaces the "named effect
instances" entry previously deferred in the
[effects roadmap](roadmap-effects.md#deferred-topics).

Unless a block says otherwise, Fango below is an acceptance specification
rather than a fixture that compiles today.

## The row-polymorphic wrapper shape

The fixtures so far bind closures whose rows are closed and concrete, inside
wrappers that are themselves fixed Direct. The byte IO readers are not: their
operations expose the residual row of the source they read, so both the wrapper
and the bound closures are transport-polymorphic.

```fango
effect Reading
    buffered : () -> Bytes
    refill : () -> Bool
    skip : Int -> Int

type Reader e =
    { buffered : () ->{e} Bytes
    , refill : () ->{e} Bool
    , skip : Int ->{e} Int
    }

Reader.over : Source e -> (Reader e ->{e} a) ->{e} a
Reader.over source use =
    handle use { buffered = \_ -> buffered(), refill = \_ -> refill(), skip = \n -> skip n }
        with pending = Bytes.empty of
        buffered () -> resume pending with pending
        refill () -> ...
        skip n -> ...
```

The three lambdas perform `Reading`; the fields want `e`; the clauses perform
`e` through the source. This is the same split
[`Stream.withCursor`](reference/library-streams.md) already makes for
`Iterator a e`. It matters for more than documentation: the compiler selects
Direct, Exit, or Machine transport per arrow from its row, so a reader over
memory compiles to direct calls while a reader over a socket carries exits.

Wanted, in order: a record of bound closures at a same-module effect, at a
cross-module effect, and at `IO`, the last being where the
[codegen defect](roadmap-io.md#codegen-segfaults-on-a-row-polymorphic-handler-at-a-cross-module-effect)
lives.

A named record literal's field positions already carry the rule, so a fixed-row
wrapper such as `Cell { step = \_ -> tick() }` binds today. Two things on the
way are not the rule's:

- An inferred record literal, `{ step = \_ -> tick() }` with no constructor
  name, resolves through the deferred record obligations instead of a field
  constraint, and so never reaches the rule.
- Projecting a field whose arrow is pure and calling it inside an effectful
  body is rejected outright, with or without a handler: `c.step()` where
  `step : () -> Int` infers the field as `() ->{IO} a` from the ambient row
  before the obligation resolves, and then disagrees with the declaration.
  This predates the rule and blocks every record of bound closures.

## A bound closure's transport must be materializable

This is the committed blocker, found while building the rule. The Go backend
materializes a handler activation's record at the transport of the worker that
installed it: `handleExpr` resolves the activation's polymorphic control
against the enclosing definition's, and machine lowering hands the body worker
a Machine-mode record unconditionally. A bound closure whose own row fixes a
*lower* transport than that worker's therefore has no member to call.

```fango
effect Counter
    tick : () -> Int

type Cell = Cell (() -> Int)

-- The handled label in the callback's row is what makes this wrapper
-- transport-polymorphic; with `(Cell ->{IO} a)` the same program runs.
counter : Int -> (Cell ->{Counter, IO} a) ->{IO} a
counter start use =
    handle use (Cell (\_ -> tick())) with n = start of
        tick () -> resume n with n + 1
```

Two symptoms, depending on which variant the wrapper needs. The Machine
variant emits a `Direct`-typed member whose body calls the Machine record, and
Go rejects the generated package. The Exit variant compiles with the closure's
`Direct` member absent, so reaching it is a nil dereference at runtime. The
same shape with `Fail`-style abort in the callback row reproduces the second.

The `Reader e` shape above does not hit this: its bound closures are
transport-polymorphic, so they carry every member and select one per context.
What hits it is a bound closure whose row fixes Direct or Exit, which is the
plain case a reader of this document will write first, so it cannot stay
silently accepted.

The fix is to materialize the activation at its clauses' own transport rather
than the installing worker's, so a Direct handler stays Direct inside an Exit
or Machine worker and performs widen to the caller's protocol the way Direct
evidence passed to a wider callee already does. That is contained for the Exit
variant. For the Machine variant it is not: machine lowering turns the clauses
into frame workers and the state into a machine state token, which nothing
outside that machine can drive, so the handler would have to stay ordinary Go
while only its body is a machine region. Refusing the shape with a diagnostic
is the cheaper half and should land first.

## Related work, not required here

**Row labels keyed by their arguments.** A parameterized effect may appear in
a row only once, so `{Box Int, Box Bool}` is rejected. Keying label identity on
the effect together with its arguments would lift that. The hard part is
unification rather than the rule: `{Box a, Box Int}` has two distinct labels
only if `a` is not `Int`, so row unification acquires a disequality it cannot
generally decide, and the plausible restriction is to require repeated labels'
arguments to be rigid or ground where the row is formed. Nothing in the IO
layer needs it: varying types live on the bound record, and the effects stay
unparameterized.

## Open questions

- Whether the adaptation should stay implicit. A lambda that used to be a type
  error inside a handler subject now compiles and binds to that handler, which
  is the reading its author most plausibly intended, but an explicit marker on
  the lambda would remove the doubt at the cost of syntax.
- Whether an abort-only operation may be bound once a consumer exists, and if
  so whether by forcing the activation scoped or by another guard. Today it is
  refused, because an abort carries a runtime exit target for its exact
  activation and a bound abort could outlive it.
- Whether the rule should extend beyond the subject to closures written in the
  handler's clauses, which run outside the activation and today could bind
  only to an enclosing one.
- Whether rows should ever say *which* activation. Doing so means naming
  instances in types, with a fresh rigid name per handler; the index infects
  every type that holds one, and discharge inside a library wrapper rather
  than a syntactic `handle` needs rank-2, which Fango does not have. The
  current rule deliberately leaves rows exact about effects and silent about
  identity.
