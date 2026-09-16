# Effectful function types and handlers

Per-arrow effects, row compatibility, handlers, aborts, and resume discipline.

[Reference index](../reference.md).

Effects appear only on function arrows. `A ->{IO} B` applies an `A` argument,
performs `IO`, and returns a `B`. `{Console, Fail String | e}` is a row with two
known labels and an open tail; `{e}` is the compact open-tail spelling. Pure
arrows omit a row, so `A ->{} B` is rejected as redundant. The spaced
`A -> {IO} B` spelling is equivalent to `A ->{IO} B`.

Every curried arrow owns its execution effects. `A ->{IO} (B -> C)` performs
when applied to the `A`; `A -> (B ->{IO} C)` performs only when the returned
function is applied to the `B`.

There is no implicit execution. Expected types, annotations, bare mentions,
bindings, conditionals, and higher-order arguments never apply a function.
Unit functions must be called explicitly:

```fango
say : () ->{IO} ()
say() = print "hello"

main() =
    action = say
    action()
    input = readLine()
    print input
```

Function values are first-class and may be stored in ADTs. Partial operation
application remains a pure function; an operation performs only when
saturated.

## Row inclusion and callback compatibility

Rows may be shared across higher-order arrows. For example:

```fango
map : (a ->{e} b) -> List a ->{e} List b
```

A pure callback instantiates `e` to empty; an effectful callback propagates its
row to the traversal call.

A body may call arrows that carry the bare tail alongside arrows that add
labels to it, in either order:

```fango
using : (() ->{e} a) -> (a ->{Fail String | e} ()) ->{Fail String | e} ()
using acquire use =
    resource = acquire()
    use resource
```

Calling `acquire`, whose row is the bare tail `{e}`, does not stop the
surrounding row from gaining `Fail String` from the later call, and swapping
two such statements never changes whether a definition is accepted. The
annotated row may also carry effects a callee does not perform, so a
`{IO, Fail String | e}` body may call a `{Fail String | e}` argument and
`print` besides.

A shared row variable describes the permitted combined effects. Each callback
may perform fewer effects, whether it is named or written inline:

```fango
pair : (() ->{e} Int) -> (() ->{e} Int) ->{e} Int

pair emit boom      -- e includes IO and Fail String
pair boom emit      -- the same combined row
pair (\_ -> emit()) boom
```

Argument order does not determine the permitted row. Partial applications and
ordinary wrappers use the same rule as saturated calls, including `Scope`.
Passing a callback never executes it, and widening one use does not change its
binding or other uses.

The rule also applies to stored callbacks and covariant effect-indexed values.
For example, `type Test e = Test (() ->{e} ())` permits pure and IO tests in
one list, in either order. Open row parameters can also widen: a consumer of
an effect-indexed source may add its own
effects to the shared row. The enclosing annotation must permit those effects.
The compiler derives variance from fields, including
recursive types and imported abstract types. Function inputs reverse the
direction: a function accepting only pure callbacks cannot stand in for one
that must accept IO callbacks. Parameters used in both directions, effect-label
arguments, and class constraints remain invariant. Widening never removes an
effect or relaxes capture and resource restrictions.

## Exact definition annotations

Definition annotations remain exact about the known effects on their arrows.
A pure body annotated `() ->{IO} Int` is rejected, at top level or in a local
definition. This differs deliberately from argument compatibility: a named pure
function can be passed to a parameter permitting IO without claiming that its
own definition performs IO. Annotation type variables and residual row tails
remain rigid.

An effect outside the permitted row reports `EFFECT MISMATCH`, naming the
performed and available effects.

## Effects and handlers

Effects declare operations using an indented signature block:

```fango
effect Ask
    ask : () -> String

query _ = if ask () == "yes" then 40 else 0

main =
    print
        (handle query () of
            ask () -> resume "yes"
            return n -> n + 2)
```

The compiler adds the declaring effect to each operation's type. Functions may
annotate closed or open effect rows. An operation with a Unit argument is
called explicitly with `()`.

## Abort-only effects

An abort-only effect marks every operation with `abort`:

```fango
effect Fail error
    abort fail : error -> value
```

All operations in one effect must use the same discipline; mixing marked and
unmarked operations is rejected. An abort operation may introduce exactly one
operation-local type variable as its whole result, as above. That variable may
not occur in a payload parameter. This is the only supported form of
operation-local polymorphism. Abort operations cannot be `native`. A saturated
abort never returns normally, while partial application remains a pure function
value.

## Handler clauses and abort routing

A handler handles one effect and must contain a clause group for every
operation of that effect. Adjacent repetitions of an operation form one
source-ordered, exhaustive, non-redundant pattern group; a noncontiguous repeat
is a duplicate-clause error. An optional adjacent `return` group matches the
handled computation's normal result under the same rules. All clauses align
like `case` branches and accept full argument patterns. `resume value`
continues from a resumptive operation. An abort clause instead returns the
handler answer directly and has no resume binding:

```fango
attempt action =
    handle action() of
        fail error -> Err error
        return value -> Ok value
```

Using `resume` there is a `RESUME IN ABORT CLAUSE` error. An abort evaluates
all payload arguments left to right, unwinds to the exact handler activation,
and only then runs its clause with the surrounding outer evidence. Recursive
and nested handlers of the same effect remain distinct. The abort answer
bypasses the handler's `return` clause; normal completion runs `return` once.
An abort raised by an abort clause or return clause propagates outward rather
than re-entering that activation.

## Stateful handlers

A parameterized handler inserts `with snapshot = initial` between its subject
and `of`:

```fango
handle action() with current = initial of
    get () -> resume current with current
    put next -> resume () with next
    return value -> StateResult { value = value, state = current }
```

`with` is contextual and remains an ordinary lowercase name elsewhere. The
initial state is evaluated once before entering the handled body. `current` is
an immutable snapshot visible in operation and `return` clauses, but not in the
handled body. Every operation path must use `resume value with nextState`;
ordinary handlers continue to use `resume value`. The value and next-state
expressions evaluate left to right exactly once, and the state is committed
only after both finish successfully. The `return` clause sees the final state.

## Resume discipline

Resumptive handlers are deliberately restricted: every normally completing
operation-clause path must end in exactly one tail call to `resume`. A
saturated abort-only call is an exceptional terminal, so a path such as
`if valid then resume answer else fail error` is legal. Non-tail or escaping
continuations, general operation-local polymorphism, mixed-discipline effects,
and handlers for builtin `IO` are rejected. Effects other than the handled
label remain in the surrounding row. A resume in an operand or before another
expression is a `NON-TAIL RESUME`; a normal clause path without a resume is a
`MISSING RESUME`; and a bare, partially applied, stored, or lambda-captured
resume is a `RESUME ESCAPES` error. Diagnostics point to the offending
expression and name the owning operation clause's location. Nested operation
clauses bind their own resume, while nested handled bodies and return groups
retain the surrounding resume binding.

## Handler lifetimes

Ordinary stateless user-declared effects have durable evidence: returning a pure closure
that captures an immutable Reader-style handler remains legal. There is no
scope annotation in source syntax. Parameterized handlers are scoped: a result
that can retain their local capability is rejected with `STATE RESULT ESCAPES`
or `RESOURCE ESCAPES`. Inferred contracts distinguish functions that capture
local evidence from functions that are independent of it. A partial operation
that receives fresh evidence on its next application does not by itself retain
the preceding handler. This does not shorten the lifetime of existing stateless
handler values.

Nested handlers of the same effect remain distinct. Stored callbacks use their
nearest lexical binding and do not retain unrelated handler evidence.

A reusable handler wrapper may annotate that residual flow with an open row
tail. The handled label disappears from the callback's row while every other
effect the callback performs passes through the wrapper's own row:

```fango
run : (() ->{Ask | e} a) ->{e} a
run action =
    handle action() of
        ask () -> resume "yes"
```
