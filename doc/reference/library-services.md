# Shared service contexts

`Service` separates a retained service context from the execution authority of
its caller. It supports cooperative coroutines and nested pulls.

```fango
{-# service #-}
effect Dispatch
    submit : Int ->{Runtime.Service.Invocation Int ()} ()
```

Every operation of a `service` effect declares the same fixed
`Runtime.Service.Invocation request reply` protocol. Its types may depend on the effect's
parameters, but not on operation-local variables. Native and aborting operations
are excluded. Invalid declarations report `SERVICE PROTOCOL`.

A service handler uses ordinary immutable lexical context. It cannot declare
mutable handler state (`SERVICE STATE`). Native state retained by its context
must satisfy the [shared-resource contract](native.md#shared-native-resources)
when the context crosses into independently registered work. Ordinary handlers
keep their existing binding rules.

Calling a service operation supplies a compiler-generated invocation argument
separately from its retained context. There is no additional source parameter.
Inside the clause, `Runtime.Service.invoke request` sends through that argument and
returns its typed reply. A clause finishes with ordinary tail `resume`.

```fango
type Bound = Bound (Int ->{Runtime.Service.Invocation Int ()} ())

bind offset = handle Bound (\value -> submit value) of
    submit value -> resume (Runtime.Service.invoke (value + offset))
```

The stored callable retains this handler's `offset`. Each later call receives
its own caller's invocation authority, including through helpers, ADTs, and
class dictionaries.

## Installing invocation authority

```fango
Runtime.Service.run : (request ->{Runtime.Coroutine.Suspension} reply)
    -> (() ->{Runtime.Service.Invocation request reply | e} a)
    ->{Runtime.Coroutine.Suspension | e} a
```

A producer calls `Runtime.Service.run pause action` with its active pause capability.
The matching slot is scoped to `action`. The compiler threads it through calls;
a nested `Stream` pull forwards this slot even when that stream's own pause has
a different request type. An independently registered child installs a fresh
slot with its own pause. Binding a service retains its context, never this slot.

An ordinary handler cannot install `Invocation`. An arbitrary function cannot
stand in for an active producer's pause (`INVOCATION AUTHORITY`). Missing or
mismatched protocols produce effect/type errors. Scoped capture checks reject
retaining execution authority past its owner; work transfer rejects carrying
an existing slot or another producer's pause into an independent child. A
returned function whose row still names `Invocation` requires a new slot from
its next caller; it does not capture the old one.

This API introduces no ambient current-task state and does not permit concurrent
Fango execution. See [the implementation contract](../design/shared-capabilities.md).
