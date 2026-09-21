# Roadmap: handler instances

The typing rule that addresses a *specific* handler activation through a value
is implemented. [Effects](reference/effects.md#binding-a-closure-to-a-handler-activation)
owns its behavior and diagnostics; [effect execution](design/effects.md#binding-a-closure-to-an-activation)
owns the binding mechanism and the optimizer invariant it forces. That includes
the row-indexed wrapper shape the [byte IO layer](roadmap-io.md) was waiting
on, so this document now owns only the questions the rule leaves open. It
replaces the "named effect instances" entry previously deferred in the
[effects roadmap](roadmap-effects.md#deferred-topics).

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
