# Roadmap: handler instances

The typing rule that addresses a *specific* handler activation through a value
is implemented. [Effects](reference/effects.md#binding-a-closure-to-a-handler-activation)
owns its behavior and diagnostics; [effect execution](design/effects.md#binding-a-closure-to-an-activation)
owns the binding mechanism and the optimizer invariant it forces. This document
now owns only what is left: the wrapper shape the [byte IO layer](roadmap-io.md)
needs. It replaces the "named effect instances" entry previously deferred in
the [effects roadmap](roadmap-effects.md#deferred-topics).

Unless a block says otherwise, Fango below is an acceptance specification
rather than a fixture that compiles today.

## The row-polymorphic wrapper shape

The fixtures so far bind closures whose rows are closed and concrete. The byte
IO readers are not: their operations expose the residual row of the source they
read, so the bound closures are transport-polymorphic and select a member per
context rather than fixing one.

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

The three instantiations exist as the `reader_over_*` fixtures — a record of
closures in a row-polymorphic handler's subject, driven at a same-module
effect, at `State`, and at `IO` — but with `Reading` still named in the
record's field rows. Nothing is bound there: every call re-resolves to the
innermost activation, which is indistinguishable while there is one. Dropping
the label from those fields is what is left, and it is what lets two readers
be driven at once.

A named record literal's field positions already carry the rule, so a fixed-row
wrapper such as `Cell { step = \_ -> tick() }` binds today. What a row-indexed
one meets instead is that the field constraint carries the handler stack only
when the argument it adapts is the lambda itself. A closure inside a
row-indexed constructor or record literal reaches its position through the
container's row argument, and the inclusion that argument produces names no
handler to bind to, so the label is an ordinary mismatch:

```
It performs:

    {Counter | e}

but only these effects are available here:

    {e2}
```

Two more things on the way are not the rule's:

- An inferred record literal, `{ step = \_ -> tick() }` with no constructor
  name, resolves through the deferred record obligations instead of a field
  constraint, and so never reaches the rule.
- Projecting a field whose arrow is closed and pure and calling it inside an
  effectful body is rejected outright, with or without a handler: `c.step()`
  where `step : () -> Int` infers the field as `() ->{IO} a` from the ambient
  row before the obligation resolves, and then disagrees with the declaration.
  This predates the rule. A row-indexed field escapes it, because the row
  argument absorbs the ambient effects instead.

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
