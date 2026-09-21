# Roadmap: handler instances

This document owns the proposal to address a *specific* handler activation
through a value, rather than always reaching the innermost one. It replaces
the "named effect instances" entry previously deferred in the
[effects roadmap](roadmap-effects.md#deferred-topics).

It is a committed prerequisite of the [byte IO layer](roadmap-io.md), whose
readers and writers are handler activations and which must operate two byte
sources at once. It is proposed on its own merits as well: an activation that
can be named is the missing half of an effect system whose evidence already
distinguishes activations at runtime.

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

Core already distinguishes the two modes. A `Lambda` carries `EffectParams`
and `RowEffects` — evidence supplied at invocation — while a closure whose row
no longer names the effect uses captured definition-site evidence, which
`Scope.bracket`'s release closure already relies on. **Discharge is the switch
between them.** The runtime is equally ready:
[every activation has a ScopeID as well as a nominal identity](design/effects.md#handler-activations-and-state),
and nested handlers of the same effect are already distinct capabilities.

The whole gap is in the surface and the type layer.

## The feature

One sentence: **a way to construct operation closures that capture their
activation's evidence instead of taking it at the call site.**

### Reification

A handler may hand the computation it handles a value standing for itself.
The proposed spelling is a contextual keyword valid only in a handler's
subject, where the activation is installed but the body has not yet run:

```fango
Reader.over source use =
    handle use(instance) with pending = Bytes.empty of
        buffered () -> resume pending with pending
        refill () -> ...
        skip n -> ...
```

`instance` evaluates to a record whose fields are this effect's operations,
each bound to this activation. Calls are ordinary field projection:

```fango
first.refill()
second.refill()
```

Nothing new is added to call syntax or to name resolution. The generated
record is the natural reading of what reification means, and it is what the
failed library trick above was reaching for.

`with` is the precedent for a contextual keyword, so `instance` remains an
ordinary identifier everywhere else.

### The instance type

Each `effect E` implicitly declares an opaque type usable in type position as
`E`. This fills a slot that is already reserved rather than creating an
ambiguity: effects and types share one namespace today, so `type Foo` beside
`effect Foo` already reports `MULTIPLE DEFINITIONS` ("the type `Foo` collides
with an effect of the same name"), and an effect name in type position
currently reports `NAMING ERROR`. It is the same pun the language already
makes between a type and its constructor.

```fango
Reader.over : Source e -> (Reader ->{Reader | e} a) ->{e} a
Reader.readUpTo : Reader -> Int ->{Reader} Bytes
```

`Reader` in a row is the effect; `Reader` in type position is an activation of
it. This is settled: the reuse is decided, and a separately declared instance
type — an `effect` block naming its own — was considered and rejected for
appearing in every signature to buy a distinction the namespace already keeps.

### What the rows carry

The label keeps its present meaning — "an operation of this effect is
performed here" — and the *value* decides which activation. Operation arrows
therefore carry the label, not the handler's residual row:

```fango
refill : () ->{Reader} Bool
```

so the instance type is **not** effect-indexed, and this proposal does not
depend on the effect-indexed-record defect recorded in the
[IO roadmap](roadmap-io.md#step-1--three-compiler-defects). The handler
absorbs its own residual effects into the `handle` expression's row, exactly
as it does today.

Two activations therefore share one label. That is already how the library's
one instance-shaped API works:

```fango
withCursor : Stream a e -> (Iterator a e ->{Traversal | e} result) ->{e} result

zipCursors : Iterator a e -> Iterator b e ->{Traversal, Yield (a, b) | e} ()
```

`zipCursors` holds two live iterators under a single `Traversal`.

**A parameterized effect is not required and should be avoided.** A row admits
a parameterized effect only once — `{Box Int, Box Bool}` reports
`DUPLICATE EFFECT` — so an effect whose label carries a content type cannot
describe two activations at different arguments. Keeping the effect
unparameterized and putting the varying types on the instance value is the
same split `Traversal` and `Iterator a e` already use, and it removes the
row-label rule change from this proposal's critical path. That change is
recorded below as related work, not as a prerequisite.

### Scoping

An instance retains its handler's capability, so it is scoped and the existing
proofs apply unchanged: returning one, storing it in an outer handler, or
retaining it through an ADT or a closure reports `STATE RESULT ESCAPES` or
`RESOURCE ESCAPES`. Parameterized handlers are already scoped.

Stateless handlers are durable by default, and reifying one must force it
scoped, because a reified operation needs its activation to resume into. This
is the one behavioral change the proposal makes to an existing contract.

It is contained without making reification opt-in at the declaration, because
whether a handler reifies itself is visible in its own subject: a handler that
never mentions `instance` keeps the durability it has today, and an effect
nobody reifies is untouched. The change is per activation, not per effect.
This is also why first-class abort labels are a separate question rather than
a free consequence — `Fail` is the effect most likely to be reified and most
load-bearing if its durability moves.

### Discharge, and the imprecision it inherits

Discharge is nominal: a scope removes its label even when the body also
operates on an activation owned by an outer scope. A function can therefore be
typed pure while it mutates. Current behavior, using only today's `Stream`:

```fango
leak : Iterator Int e ->{e} Maybe Int
leak cursor =
    Stream.withCursor (Stream.fromList [9, 9, 9]) (\_ -> next cursor)
```

At a pure stream `e` is empty, so `leak : Iterator Int -> Maybe Int` — and
three calls answer `Just 1`, `Just 2`, `Just 3`.

No effect escapes unhandled: an instance exists only inside its own scope and
cannot leave it, so whenever an operation runs its activation is live. What is
inaccurate is only the purity claim, and it bears on compile-time evaluation,
where a splice may run a pure expression, and on any future pass that reorders
or shares calls. Splices look contained — a splice can only reach an instance
it created itself, deterministically and under the step budget. The optimizer
case is real:

> **Invariant.** A pass that reorders, hoists, or shares calls must treat an
> arrow whose parameters or captures include a resource- or instance-typed
> value as impure, whatever its row says.

That belongs in the design whether or not this proposal is built, because
`Iterator` already requires it.

### Rejected: instance names in types

Making rows exact would mean naming instances in types, with a fresh rigid
name per handler. Rejected on two grounds. The index infects consumers: a
`Reader` carrying an instance becomes `Reader r`, so every type holding one
gains the parameter. And discharge inside a library wrapper rather than a
syntactic `handle` needs rank-2, since a caller could otherwise instantiate
the name to an outer activation's and discharge the wrong one; Fango has
neither rank-2 nor existentials, and defers higher kinds.

## Milestones

### A. Reification

The contextual `instance` keyword, the generated instance type and its
resolution in type position, operations bound to captured evidence, and forced
scoping for reified stateless handlers.

Acceptance: two nested cells answer independently rather than both driving the
inner one; a reified operation invoked inside an unrelated handler of the same
effect still reaches its own activation; an instance escaping its scope through
a value, an ADT, a closure, or an outer handler is rejected with the existing
diagnostics; Core lint reconstructs the captured-evidence form; a module
declaring an effect and a type of one name is rejected.

Grammar changes require a TextMate update and representative tokenization of
files using the new form.

### B. Documentation

Promote the implemented contract into [effects](reference/effects.md) and
[design](design/effects.md), remove the superseded deferred entry, and state
the optimizer invariant in the design regardless of what else lands.

## Related work, not required here

**Row labels keyed by their arguments.** A parameterized effect may appear in
a row only once, so `{Box Int, Box Bool}` is rejected. Keying label identity on
the effect together with its arguments would lift that. The hard part is
unification rather than the rule: `{Box a, Box Int}` has two distinct labels
only if `a` is not `Int`, so row unification acquires a disequality it cannot
generally decide, and the plausible restriction is to require repeated labels'
arguments to be rigid or ground where the row is formed. Nothing in this
proposal or the IO layer needs it, because both keep their effects
unparameterized.

## Open questions

- Whether `instance` is the right keyword, and whether it should be restricted
  to the handler's subject or allowed wherever the activation is in scope.
- Whether an abort-only effect may be reified. It would give first-class
  failure labels, but it forces the stateless-durability change above and
  interacts with the abort protocol's unwind-to-exact-activation rule.
- Whether a reified instance may be stored in an ADT that outlives a nested
  scope but not its own. The escape rules permit it and the IO layer relies on
  exactly that, so it needs its own fixtures.
