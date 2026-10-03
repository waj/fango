# Effectful function types and handlers

Per-arrow effects, row compatibility, handlers, aborts, and resume discipline.

[Reference index](../reference.md).

Effects appear on function arrows and, as a type argument, at a
[row-kinded parameter](functions.md#row-kinded-parameters).
`A ->{IO} B` applies an `A` argument,
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

A row holds one label per applied effect. Fully resolved applications such as
`{Put Bool, Put String}` may appear together and select separate handlers:

```fango
effect Put a
    put : a -> ()

both : () ->{Put Bool, Put String} ()
both() =
    put True
    put "answer"
```

The same application cannot appear twice. When an argument still contains a
type variable, inference treats occurrences of the same nominal effect as
potentially overlapping and unifies their arguments. An incompatible overlap,
such as `{Put a, Put Bool}` for an unknown `a`, reports `EFFECT MISMATCH`.

The order of labels never chooses an application. An application whose
argument is still unknown, met by a row holding several applications of its
effect, waits until the rest of the declaration determines the argument.
Operation calls use their argument and result types; a handler or a
polymorphic function such as `Fail.attempt` uses whatever fixes its effect
argument, typically a pattern on the payload or a helper whose callback
parameter names one application:

```fango
printStrings : (() ->{Put String, IO | e} a) ->{IO | e} a
printStrings action = handle action() on
    put text -> print text; resume ()
```

`printStrings { both() }` handles `Put String` and leaves `Put Bool` to an
outer handler. If nothing determines the argument, an operation call reports
`AMBIGUOUS EFFECT APPLICATION` and any other use reports `AMBIGUOUS EFFECT`;
nesting two `handle` expressions whose clauses ignore their payload types is
rejected this way. A function's result annotation is checked after its body,
so it does not determine the argument. No additional source syntax is
required.

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
pair { emit() } boom
```

Argument order does not determine the permitted row. Partial applications and
ordinary wrappers use the same rule as saturated calls, including `Scope`.
Passing a callback never executes it, and widening one use does not change its
binding or other uses.

The rule also applies to stored callbacks and covariant effect-indexed values.
For example, `type Test e = Test (() ->{e} ())` permits pure and IO tests in
one list, in either order. Reading one back widens the same way: a record
field declared `() ->{Cell | e} Int` may be projected and called in a body
that performs more than that arrow names. Open row parameters can also widen:
a consumer of an effect-indexed source may add its own effects to the shared
row. The enclosing annotation must permit those effects.
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
performed and available effects. A report prints an arrow's row whenever the
row variable ending it appears anywhere else in the same type, an
effect-indexed value's row argument included, so two types that differ only in
such a tail are never shown as one:

```
The annotation says:

    Iterator Int e -> Maybe Int

but the body requires:

    Iterator Int e ->{e} Maybe Int
```

A row variable an arrow alone carries says only that the caller chooses, and
stays elided.

## Effects and handlers

Effects declare operations using an indented signature block:

```fango
effect Ask
    ask : () -> String

query _ = if ask () == "yes" then 40 else 0

main =
    print
        (handle query () on
            ask () -> resume "yes"
            return n -> n + 2)
```

Later operation signatures may outdent from the first while staying indented
under the declaration; the formatter aligns them. The compiler adds the
declaring effect to each operation's type. Functions may
annotate closed or open effect rows. An operation with a Unit argument is
called explicitly with `()`.

The handled subject follows `handle` the way a body follows `=`: an inline
expression or `;`-separated block on the `handle` line, or an indented
statement block below it. After a block, `on` (and a
[state clause](#stateful-handlers)) may return to the `handle` keyword's
column, which ends the block, so an inner `case` or handler never takes the
outer clauses:

```fango
loop() =
    handle
        exchanged = readRequest input
        case exchanged of
            Nothing -> ()
            Just request -> reply request; loop()
    on
        invalid error -> sendError error
```

`on` is reserved.

A source-defined resumptive operation may use a type variable local to that
operation. Each call instantiates it independently, while one handler clause
must work for every instantiation:

```fango
effect Echo
    echo : a -> a

answer = handle (if echo True then echo 42 else 0) on
    echo x -> resume x
```

The variable may also occur inside payload or callback types, such as
`fetch : Key a -> a`. It is rigid inside the clause: a clause cannot assume
that every `a` is `Int` or let `a` escape its handler. Operation-local class
constraints and native operation signatures are not supported.

## Abort-only effects

An abort-only effect marks every operation with `abort`:

```fango
effect Fail error
    abort fail : error -> value
```

All operations in one effect must use the same discipline; mixing marked and
unmarked operations is rejected. An abort operation may introduce exactly one
operation-local type variable as its whole result, as above. That variable may
not occur in a payload parameter. Abort operations cannot be `native`. A saturated
abort never returns normally, while partial application remains a pure function
value.

## Handler clauses and abort routing

A handler handles one or more effect applications and must contain a clause
group for every operation of each. Adjacent repetitions of an operation form
one source-ordered, exhaustive, non-redundant pattern group; a noncontiguous
repeat is a duplicate-clause error unless each group carries a
[signature](#operation-signatures) naming a different application. An
optional `return` group, written once, matches the handled computation's
normal result under the same rules. Clauses accept full argument patterns and
may vary in indentation like `case` branches; the formatter aligns them.
`resume value` continues from a resumptive operation. An abort clause instead
returns the handler answer directly and has no resume binding:

```fango
attempt action =
    handle action() on
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

Handler clauses execute outside their own activation, and outside every
application it handles. They may use an enclosing handler, including another
handler of the same effect; a sibling application of the same handler is
never reachable from a clause. A function annotation does not need to expose
effects discharged by those enclosing handlers; unhandled effects in clauses
must still be permitted by the annotation.

### Several applications in one handler

One handler may cover several applications, of one effect or of different
effects. Signed groups name their applications; a group without a signature
belongs to its effect's inferred application, or completes the effect's only
signed one. Once an effect has several applications in a handler, every one
of its clauses needs a signature saying which it serves, or the clause is a
`MISSING OPERATION SIGNATURE`. Two groups for the same operation and
application are a `DUPLICATE HANDLER CLAUSE`:

```fango
load action =
    handle action() on
        fail : ParseError -> a
        fail (ParseError line) -> Err ("bad syntax at line " ++ show line)

        fail : IoError -> a
        fail (IoError reason) -> Err ("cannot read: " ++ reason)

        return config -> Ok config
```

Each application is covered independently: every operation of its effect
needs a group, exhaustive and non-redundant. Each abort unwinds to the clause
of its own application; the `return` group is shared. Abort-only and
resumptive applications may share one handler, each keeping its own
discipline: resumptive clauses end in `resume`, abort clauses return the
common answer and bypass `return`. Inside the subject every handled
application is in scope, so an operation whose type does not say which
application it means is an `AMBIGUOUS EFFECT`; a typed helper names the one
intended.

## Operation signatures

A clause group may be headed by the operation's signature, written as the
effect declaration writes it and specialized to the handled application:

```fango
attemptText action =
    handle action() on
        fail : String -> a
        fail message -> Err ("failed: " ++ message)
        return value -> Ok value
```

The signature describes the handled operation, not the clause below it: its
result is the operation's result, while the clause still returns the handler
answer. It may fix only the declaring effect's parameters, and must fix every
one to a closed type; here it selects `Fail String` where the clause alone
would leave the error type open. The operation's own type variables stay
variables and may take any name, since a type variable in a signature is
always fresh. Instantiating one, or writing a shape the declaration does not
have, is an `OPERATION SIGNATURE MISMATCH`; leaving a parameter open is an
`INCOMPLETE OPERATION SIGNATURE`.

The effect row is optional. When written, it belongs to the innermost arrow
and is exactly the declaring effect's application, which is how a parameter
absent from the operation's inputs and result is selected:

```fango
simulated action =
    handle action() on
        tick : () ->{Clock Simulation} Int
        tick () -> resume 42
```

That row identifies the handled application; it does not describe the
effects the clause performs. A signature must be followed directly by a
clause for the same operation, has no class context, and `return` takes
none. A qualified operation may carry a signature. The formatter sets each
signed group after the first apart with one blank line.

## Stateful handlers

A parameterized handler inserts `with snapshot = initial` between its subject
and `on`:

```fango
handle action() with current = initial on
    get () -> resume current with current
    put next -> resume () with next
    return value -> StateResult { value = value, state = current }
```

`with` is contextual and remains an ordinary lowercase name elsewhere, apart
from the [`with` block item](syntax.md#with-items). The
initial state is evaluated once before entering the handled body. `current` is
an immutable snapshot visible in operation and `return` clauses, but not in the
handled body. Every operation path must use `resume value with nextState`;
ordinary handlers continue to use `resume value`. The value and next-state
expressions evaluate left to right exactly once, and the state is committed
only after both finish successfully. The `return` clause sees the final state.

The cell belongs to the whole handler. When one handler covers
[several applications](#several-applications-in-one-handler), every clause
sees snapshots of the same cell and every commit updates it, so `State Int`
and an `Emit` effect can share one context. An abort clause beside them sees
the snapshot taken when its abort unwinds and commits nothing:

```fango
collect action =
    handle action()
    with context = Context { count = 0, messages = [] } on
        get : () -> Int
        get () -> resume context.count with context

        put : Int -> ()
        put n -> resume () with { context | count = n }

        emit message ->
            resume () with { context | messages = [message | context.messages] }

        return value -> Collected { value = value, count = context.count, messages = context.messages }
```

State snapshots and commits are individually synchronized to publish complete
values. The clause runs between them without an operation-wide lock: concurrent
operations can read the same snapshot, and a later commit can overwrite an
earlier update. A clause that resumes with its own snapshot binder, as in
`resume current with current`, commits nothing and so never overwrites another
task's update. Handlers are responsible for operation-level serialization.
The snapshot is taken before the clause starts, so a lock acquired inside the
clause cannot protect that implicit read. For atomic updates, keep state in an
explicit reference and protect its read and write together, or serialize calls
to the handler.

Children inherit resumptive handler activations and their state cells; see the
[Async contract](library-async.md#handler-inheritance-and-aborts).

## Resume discipline

Resumptive handlers are deliberately restricted: every normally completing
operation-clause path must end in exactly one tail call to `resume`. A
saturated abort-only call is an exceptional terminal, so a path such as
`if valid then resume answer else fail error` is legal. Non-tail or escaping
continuations, operation-local class constraints and native operations, mixed-discipline effects,
and handlers for builtin `IO` are rejected. Effects other than the handled
label remain in the surrounding row. A resume in an operand or before another
expression is a `NON-TAIL RESUME`; a normal clause path without a resume is a
`MISSING RESUME`; and a bare, partially applied, stored, or lambda-captured
resume is a `RESUME ESCAPES` error. Diagnostics point to the offending
expression and name the owning operation clause's location. Nested operation
clauses bind their own resume, while nested handled bodies and return groups
retain the surrounding resume binding.

## Closures and handler effects

A closure's effect row describes the effects performed when it is called.
Creating it inside a handler does not make an effectful arrow pure. A closure
passed to a callback or record field inside a resumptive handler's subject may
bind to that activation when the expected row omits the handled label. Calling
it then requires a fresh local permission and the effects of the handler's
clauses. The permission cannot leave the handler through its result, residual
row, or outer storage. Abort-only effects cannot be bound this way.

A scoped runner can pass such a closure to its consumer through a row-indexed
record. The [scoped binding fixture](../../testdata/run/handler_scoped_binding.fango)
shows a counter retaining its original activation beneath another Counter
handler. Attempting to pass the stateful operation as `() -> Int` is rejected
with `HANDLER BINDING EFFECTS`; the local permission is never erased to claim
purity. Ordinary closures that retain the nominal effect in their row still
receive an interpretation at invocation.

The scoped [Reader and Writer constructors](library-readers.md) discharge their
private buffer permissions while preserving source, sink, and consumer effects.

Handlers remain synchronous: a resumptive clause finishes with its owning tail
`resume`, and an abort clause abandons the subject. Handlers do not capture a
resumable stack and cannot implement general coroutine suspension.

Resources that outlive their acquiring scope are checked by native operations
at runtime; see [cleanup scopes](resources.md).
