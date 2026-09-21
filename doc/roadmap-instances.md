# Roadmap: handler instances

This document owns the proposal to address a *specific* handler activation
through a value, rather than always reaching the innermost one. It replaces
the "named effect instances" entry previously deferred in the
[effects roadmap](roadmap-effects.md#deferred-topics).

It is a committed prerequisite of the [byte IO layer](roadmap-io.md), whose
readers and writers are values bound to handler activations and which must
operate two byte sources at once. It is proposed on its own merits as well: an
activation that can be named is the missing half of an effect system whose
evidence already distinguishes activations.

Everything below is proposed. Fango blocks are acceptance specifications
rather than fixtures that compile today, except where labelled as current
behavior.

## The gap, as the language behaves today

An operation resolves to the innermost handler **at the moment it is
performed**, whenever the effect is still named in the performing closure's
row. The same closure gives different answers in different places:

```fango
effect Ask
    ask : () -> String

outer : ((() ->{Ask} String) ->{Ask, IO} a) ->{IO} a
outer use =
    handle use (\_ -> ask()) of
        ask () -> resume "OUTER"

inner : (() ->{Ask, IO} b) ->{IO} b
inner action =
    handle action() of
        ask () -> resume "INNER"

main() =
    outer (\probe ->
        print (probe())
        inner (\_ -> print (probe())))
```

prints `OUTER` then `INNER`. This is current behavior, not a defect, and it is
why an activation cannot be reified by the obvious library trick of handing
out a record of its operations: those operations' rows still name the effect,
so they re-resolve at every call. Two cells built that way both drive the
inner one.

The other half of the trick is rejected outright. Writing the record's fields
at arrows that do *not* name the effect is an `EFFECT MISMATCH` today, even in
the subject of the very handler that would answer them:

```fango
effect Counter
    tick : () -> Int

type Cell = Cell (() -> Int)

counter : Int -> (Cell ->{Counter, IO} a) ->{IO} a
counter start use =
    handle use (Cell (\_ -> tick())) with n = start of
        tick () -> resume n with n + 1
```

The rejection comes from inference. Elaboration already knows what to do with
such a closure: when a lambda is adapted to a row that no longer names one of
its effects, the elaborator substitutes the innermost lexical activation's
captures into the body, so the closure carries that activation's evidence
instead of taking evidence at each call. `Scope.bracket` relies on this, which
is why a release closure runs against the handlers live where it was written
rather than whatever is installed at the exit point. The runtime is equally
ready: an installed activation is a record of operation closures over its own
state cell and its own definition-site outer evidence, and nested activations
of one effect are separate records. **Discharge is the switch** between
call-site and captured evidence; what is missing is a rule that lets a program
ask for it.

## The rule

One sentence: **inside the subject of a `handle` expression, a closure whose
row names the handled effect may be adapted to an arrow that does not; the
adaptation binds every performance of that effect in the closure to the
activation this `handle` installs, and the closure's remaining row must cover
what the handler's clauses perform.**

With the rule, the `counter` example above compiles, and two nested counters
answer independently:

```fango
main() =
    counter 0 (\a ->
        counter 100 (\b ->
            case (a, b) of
                (Cell ta, Cell tb) ->
                    print (ta())
                    print (tb())
                    print (ta())))
```

prints `0`, `100`, `1`. `ta` was bound in the outer subject and reaches the
outer activation from inside the inner one, because binding replaces the
by-name lookup with a captured reference to one activation's record.

Nothing else changes. A bare `tick()` in the subject keeps resolving to the
innermost handler at the moment it is performed, so every existing program
means what it meant. There is no new keyword, no new type former, and no
change to name resolution or the grammar.

### The residual row

The adapted closure's remaining row is what its clauses will perform when it
is called, so the rule is not "drop the label" but "replace the label with the
handler's residual row, then unify with what is wanted". A closure bound in a
handler whose clauses perform `Fail` cannot be adapted to a pure arrow; the
diagnostic should say that the handler's clauses perform an effect the arrow
does not allow. In the common wrapper shape the two rows are one variable:

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
`Iterator a e`, whose `next` exposes exactly the cursor's residual row, and it
matters for more than documentation: the compiler selects Direct, Exit, or
Machine transport per arrow from its row, so a reader over memory compiles to
direct calls while a reader over a socket carries exits. An instance type
whose operations hid the residual row would force every call through it to be
transport-polymorphic or would drop an exit.

The instance type is therefore an ordinary user-declared type. Nothing is
generated, and the effect name keeps its single meaning as a row label.

### Nested activations of the same effect

The rule needs no special case for them, because the mechanism it exposes
already distinguishes activations. Each installation of a `handle`, including
two dynamic installations of the same expression, builds its own record of
operation closures. A parameterized activation's record closes over its own
state cell, and its clauses run against the outer evidence captured when it
was installed. A bound closure therefore reaches its own activation's clause,
snapshot, cell, and source, whichever handler of the effect is innermost when
it is called. The proxy in the [IO roadmap](roadmap-io.md#step-5--sockets-http-and-a-concurrent-server)
is two `Reader.over` activations nested, with the outer reader driven from the
inner scope.

### Scoping

A bound closure retains its activation's capability, so the existing proofs
apply unchanged. Parameterized handlers are scoped: returning the record,
storing it in an outer handler, or retaining it through an ADT or a closure
reports `STATE RESULT ESCAPES` or `RESOURCE ESCAPES`, while passing it inward
to the handled computation is an inner owner retaining an outer resource and
is permitted. That permission is what lets the outer reader flow through the
inner scope above, and it needs its own fixtures because it is the first
library use of it.

Stateless resumptive handlers stay durable. A bound closure escaping one is as
safe as any escaping closure today, since a clause is a call against captured
outer evidence and no continuation exists to resume into; if those outer
handlers are themselves scoped, the closure carries their scope identities and
the existing check rejects the escape. No contract changes.

Abort-only handlers are the exception. An abort carries a runtime exit target
for its exact activation, and a bound abort escaping a completed handler would
fire at a target nothing awaits. Today this cannot happen because a closure
performing an abort keeps the label in its row and re-resolves at each call.
The rule must therefore refuse to bind an abort-only operation, or mark an
activation scoped when its subject binds one. Refusing is proposed first:
nothing in the IO layer binds an abort, and first-class failure labels deserve
their own consumer.

### What the rows do not say

The rule makes rows exact about the effects a bound closure performs. It does
not make them exact about the handler's own state. A `Reader e` over a memory
source has `e` empty, so its `refill : () -> Bool` is typed pure and yet two
calls answer differently. Discharge already leaves the same gap for cursors.
Current behavior, using only today's `Stream`:

```fango
leak : Iterator Int e ->{e} Maybe Int
leak cursor =
    Stream.withCursor (Stream.fromList [9, 9, 9]) (\_ -> Iterator.next cursor)
```

At a pure stream `e` is empty, so `leak : Iterator Int -> Maybe Int`, and
three calls answer `Just 1`, `Just 2`, `Just 3`.

No effect escapes unhandled: a scoped value exists only inside its own scope,
so whenever an operation runs its activation is live. What is inaccurate is
only the purity claim, and it bears on compile-time evaluation, where a splice
may run a pure expression, and on any future pass that reorders or shares
calls. Splices look contained: a splice can only reach an activation it
created itself, deterministically and under the step budget. The optimizer
case is real:

> **Invariant.** A pass that reorders, hoists, or shares calls must treat an
> arrow whose parameters or captures include a resource-typed value or a
> value bound to a handler activation as impure, whatever its row says.

That belongs in the design whether or not this proposal is built, because
`Iterator` already requires it.

## Rejected alternatives

**A reification keyword and an implicit instance type.** An earlier form of
this proposal added a contextual keyword in the handler's subject that
evaluated to a generated record of the activation's operations, and gave each
`effect E` an implicit opaque type `E` for that record. It was rejected once
the rule above was seen to need only inference changes. The keyword and the
implicit type are two new primitives where the language already has the
mechanism; the implicit type puns the effect name with a type; and its
operations carried only the effect label, hiding the residual row the
transport selection depends on. It also forced reified stateless handlers to
be scoped on a justification the execution model does not support.

**Instance names in types.** Making rows exact about *which* activation would
mean naming instances in types, with a fresh rigid name per handler. Rejected
on two grounds. The index infects consumers: a `Reader` carrying an instance
becomes `Reader r`, so every type holding one gains the parameter. And
discharge inside a library wrapper rather than a syntactic `handle` needs
rank-2, since a caller could otherwise instantiate the name to an outer
activation's and discharge the wrong one; Fango has neither rank-2 nor
existentials, and defers higher kinds.

## Milestones

### A. The rule

Inference accepts the adaptation in a handler's subject and records it;
elaboration binds the closure to the activation through the existing capture
substitution; Core lint accepts and reconstructs the bound form; both backends
agree, the interpreter included, which resolves deferred operations through
explicitly passed rows and must honor captured evidence here as it does for
`Scope.bracket`.

Acceptance, on fixtures with no stdlib dependency first: two nested cells
answer independently rather than both driving the inner one; a bound operation
invoked inside an unrelated handler of the same effect still reaches its own
activation; a bound closure adapted to a row that omits an effect the clauses
perform is rejected, naming the clause effect; a record escaping a
parameterized handler through a value, an ADT, a closure, or an outer handler
is rejected with the existing diagnostics; the outer record flowing through an
inner scope of the same effect is accepted; binding an abort-only operation is
rejected; bare performs in a subject that also binds are unchanged. Then the
row-polymorphic wrapper shape at a same-module effect, a cross-module effect,
and `IO`, which is where the [codegen defect](roadmap-io.md#codegen-segfaults-on-a-row-polymorphic-handler-at-a-cross-module-effect)
lives.

### B. Documentation

Promote the implemented rule into [effects](reference/effects.md), state the
binding mechanism and the optimizer invariant in [design](design/effects.md),
and remove the superseded deferred entry from the effects roadmap.

## Related work, not required here

**Row labels keyed by their arguments.** A parameterized effect may appear in
a row only once, so `{Box Int, Box Bool}` is rejected. Keying label identity on
the effect together with its arguments would lift that. The hard part is
unification rather than the rule: `{Box a, Box Int}` has two distinct labels
only if `a` is not `Int`, so row unification acquires a disequality it cannot
generally decide, and the plausible restriction is to require repeated labels'
arguments to be rigid or ground where the row is formed. Nothing in this
proposal or the IO layer needs it: varying types live on the bound record, and
the effects stay unparameterized.

## Open questions

- Whether the adaptation should stay implicit. A lambda that used to be a type
  error inside a handler subject now compiles and binds to that handler, which
  is the reading its author most plausibly intended, but an explicit marker on
  the lambda would remove the doubt at the cost of syntax.
- Whether an abort-only operation may be bound once a consumer exists, and if
  so whether by forcing the activation scoped or by another guard.
- Whether the rule should extend beyond the subject to closures written in the
  handler's clauses, which run outside the activation and today could bind
  only to an enclosing one.
