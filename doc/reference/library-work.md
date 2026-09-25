# Scoped work packages

`Work` hides a coroutine's residual effect row while retaining its request,
reply, and result types. It provides lexical ownership for uniform library
queues. Dynamic registration allocates work in a Coroutine scope; scheduling
remains the caller's responsibility.

```fango
run : (Owner e ->{Runtime.Coroutine.Drive | e} a) ->{Runtime.Coroutine.Drive | e} a
facet : Owner e -> Facet
pack : Facet -> Coroutine request reply result e ->{e} Work request reply result
advance : Owner e -> Work request reply result -> reply ->{Runtime.Coroutine.Drive | e} Step request result
close : Owner e -> Work request reply result ->{Runtime.Coroutine.Drive | e} ()
stop : Owner e -> Work request reply result ->{Runtime.Coroutine.Drive | e} Step request result
stopCompletion : Owner e -> Work request reply result -> Runtime.Completion.Completion () e
```

`Owner`, `Facet`, and `Work` are abstract resource types. `run` supplies a fresh
owner for its callback. `facet` hides its effect budget without forgetting its
identity. `pack` retains an already existing coroutine; packing does not start
it. The coroutine must outlive the work owner. A facet or package cannot escape
its owner, including inside a closure or another data structure.

`advance`, `close`, `stop`, and `stopCompletion` require the exact owner used by
`pack`. Equal effect rows
do not make two owners interchangeable. Opening is rejected when flow from
different owners leaves the selected identity ambiguous. The operations use
the coroutine's original exclusive driver authority, reply/result checks, terminal states, and cleanup
behavior. `run` does not discharge `Runtime.Coroutine.Drive`; the enclosing coroutine
boundary does. Its lexical scope adds no dynamic coroutine registry.

`stop` begins an executable abandonment drain. Each suspended cleanup request
is returned as `Suspended`; answer it with `advance` until the Work returns
`Closed`. After that terminal step, `stopCompletion` returns the captured Unit
completion of the drain, including any typed cleanup failure. The completion
can be inspected or replayed using current failure evidence. Calling
`stopCompletion` before terminal stop is an error.

Each package charges its residual effects immediately and retains the same
obligation on its owner. Handling an effect around `pack` can handle the
immediate charge but cannot remove the stored obligation. Handle an expected
failure inside the producer if it should not contribute to the owner's budget.
Unused packages retain these obligations as well. Opening a package incurs the
owner's budget and uses the current execution's evidence for that budget;
definition-site evidence remains bound to the original handler activation.

The checker reports `WORK OWNER MISMATCH` for a foreign owner,
`WORK EFFECT BUDGET` for an insufficient budget, and `RESOURCE ESCAPES` for
lifetime violations. `WORK CAPABILITY TRANSFER` rejects producers retaining
another coroutine's execution authority, mutable handler evidence, or a native
resource without a nominal `shared-resource` contract.

## Dynamic registration

```fango
owner : Runtime.Coroutine.Scope e -> Owner e
register : Runtime.Coroutine.Facet
    -> ((request ->{Runtime.Coroutine.Suspension} reply)
        -> reply ->{Runtime.Coroutine.Suspension | e} result)
    ->{e} Work request reply result
```

`owner` gives the execution-budget view of an existing Coroutine scope; repeated
calls select the same owner. `register` uses its `Runtime.Coroutine.facet` to allocate
and package a new lazy coroutine in that scope. It preserves the three protocol
types while hiding the child's residual row. Different child rows may share a
scope when each fits its budget. No producer application runs during registration.
Use `advance`, `stop`, and `close` with that scope's `owner`.

Registration retains both its immediate effect charge and its deferred budget
obligation, just like `pack`. An intervening handler or an unused result cannot
remove the latter from the scope's outward row. Registration also checks producer
captures against the destination scope and retains the existing
`WORK CAPABILITY TRANSFER` restriction. A nullary effect may return
`Runtime.Coroutine.Facet` to select the destination without exposing its row parameter;
this does not grant shared mutable service evidence or transfer authority.

Scope exit and live-entry removal follow
[dynamic ownership](library-coroutines.md#dynamic-ownership). `Runtime.Work.run` still
provides only a lexical packaging budget, and `pack` never changes a coroutine's
execution owner. [Shared native resources](native.md#shared-native-resources),
[write-once cells](library-cells.md), and [service contexts](library-services.md)
provide checked sharing without transferring a producer’s pause or driver.
