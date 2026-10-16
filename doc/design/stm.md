# Software transactional memory

[Design index](../design.md). [STM](../../stdlib/STM.fango) owns the public
contract. Source and checks: [runtime](../../runtime/fangort/stm.go),
[sidecar](../../stdlib/STM.native.go),
[runtime tests](../../runtime/fangort/stm_test.go), and the
[transaction fixture](../../testdata/suite/STMTransactions.fango).

## Sealed effect

`STM` is an ordinary effect whose label the module exports without its
operations. A handler must name every operation of the effects it handles,
and an importer cannot name an unexported one, so `atomically` is the only
handler. No language feature marks the effect as sealed.

A transaction is typed `() ->{STM} a`, a closed row. Every effect it performs
other than `STM` must be discharged inside it, by a handler that replay
installs afresh, so rerunning an attempt is unobservable. Nested `atomically`
is excluded because it needs `Async`.

## Attempts

`atomically` handles one attempt at a time and reruns the transaction closure
to restart; no continuation is captured. Each attempt owns a fresh native log
that its handler clauses capture lexically, as `State.runAtomic` captures its
cell, so the handler keeps no `with` state. A read clause that observes a
conflict cannot abort through its own handler, so it performs the private
`Restart` abort effect, handled around the `STM` handler, and the attempt ends
as `Conflicted`. Nothing is locked while the transaction body runs, so an
attempt abandoned by an outer abort or cancellation needs no cleanup.

The log natives are private and typed pure, reached only through the handler.
Values cross under the
[representation-blind storage contract](../reference/native.md#indexed-native-storage):
the runtime stores and returns payload tokens without inspecting them. Writes
come from clauses, where an operation-local `a` is a rigid skolem at the
clause's uniform representation, and reads return through the request/reply
envelope, which adapts the payload back to the caller's representation; the
transaction fixture round-trips scalars, records, recursive ADTs, functions,
and Unit written and read at different instantiations.

## Runtime

The runtime follows TL2 with a global version clock. Every published value is
an immutable node carrying the version of the commit that wrote it.

- **Begin** records the clock as the attempt's read version.
- **Read** answers the attempt's newest buffered write, if any. Otherwise it
  loads the variable's node; a version above the read version is a conflict,
  which keeps every attempt on one consistent snapshot (opacity).
- **Write** buffers the value. **New** publishes the initial value at version
  0 at once, which is safe because nothing else reaches the variable before
  the attempt commits; a discarded attempt leaves it unreachable.
- **Commit** of an attempt without writes succeeds at once: its reads already
  proved the snapshot consistent. A writing commit takes the global commit
  mutex, checks that no variable it read has a version above its read
  version, publishes each variable's final write at the next version, and
  only then advances the clock. An attempt that begins later sees every new
  node; one that began earlier sees old nodes or rejects new ones as stale.
