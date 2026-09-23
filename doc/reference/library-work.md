# Scoped work packages

`Work` hides a coroutine's residual effect row while retaining its request,
reply, and result types. It provides lexical ownership for uniform library
queues; it does not create or schedule coroutines.

```fango
run : (Owner e ->{Coroutine.Drive | e} a) ->{Coroutine.Drive | e} a
facet : Owner e -> Facet
pack : Facet -> Coroutine request reply result e ->{e} Work request reply result
advance : Owner e -> Work request reply result -> reply ->{Coroutine.Drive | e} Step request result
close : Owner e -> Work request reply result ->{Coroutine.Drive | e} ()
```

`Owner`, `Facet`, and `Work` are abstract resource types. `run` supplies a fresh
owner for its callback. `facet` hides its effect budget without forgetting its
identity. `pack` retains an already existing coroutine; packing does not start
it. The coroutine must outlive the work owner. A facet or package cannot escape
its owner, including inside a closure or another data structure.

`advance` and `close` require the exact owner used by `pack`. Equal effect rows
do not make two owners interchangeable. Opening is rejected when flow from
different owners leaves the selected identity ambiguous. The operations use
the coroutine's original exclusive driver authority, reply/result checks, terminal states, and cleanup
behavior. `run` does not discharge `Coroutine.Drive`; the enclosing coroutine
boundary does. Its lexical scope adds no dynamic coroutine registry.

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
another coroutine's execution authority or mutable handler evidence.

Dynamic allocation, scope-owned registration, and shared service evidence are
later [coroutine stages](../roadmap-coroutines.md#implementation-stages).
