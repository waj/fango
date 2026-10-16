# Software transactional memory

[Design index](../design.md). [STM](../../stdlib/STM.fango) owns the public
contract. Source and checks: [runtime](../../runtime/fangort/stm.go),
[sidecar](../../stdlib/STM.native.go),
[runtime tests](../../runtime/fangort/stm_test.go), and the
[transaction](../../testdata/suite/STMTransactions.fango),
[retry](../../testdata/suite/STMRetry.fango),
[alternatives](../../testdata/suite/STMAlternatives.fango),
[MVar](../../testdata/suite/STMMVar.fango), and
[queue](../../testdata/suite/STMQueue.fango) fixtures, and the
[job pipeline example](../../examples/jobs.fango).

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
as `Conflicted`. `retry` is the abort operation `abandon`, which ends the
attempt as `Blocked`; `atomically` then waits on the attempt's log and reruns
the closure. Because `STM` has an abort operation, every arrow performing it
uses Exit transport. Nothing is locked while the transaction body runs, so an
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

`atomically` takes the current lifetime's context token when it starts,
which needs no checkpoint, so a transaction that never retries runs to commit
even during cancellation cleanup. Only the wait observes cancellation: when it
reports cancellation, `atomically` calls `Async.checkpoint`, which stops the
task.

## Alternatives and failure

`orElse` and `attempt` are ordinary Fango over two more private operations,
`savepoint` and `rollback`. `orElse` takes a savepoint and handles its first
branch with a forwarding handler: clauses run outside their own activation,
so each forwards its operation to the enclosing `STM` handler, and only
`abandon` is caught, rolling back to the savepoint and running the second
branch. A `retry` in the second branch propagates out of that abort clause to
the enclosing handler; a stale-read restart passes through as the private
`Restart` effect. Each `orElse` adds one forwarding hop for operations in its
first branch. `attempt` needs no handler of its own: it takes a savepoint,
runs `Fail.attempt`, and rolls back on `Err`.

A savepoint is the length of the log's append-only write list; rollback
truncates it and rebuilds the index of each variable's newest write. Reads are
never discarded, so they still validate the commit and widen a retry's wait
to every branch that ran.

## Derived structures

[`STM.MVar`](../../stdlib/STM/MVar.fango) and
[`STM.Queue`](../../stdlib/STM/Queue.fango) are ordinary Fango over the public
`STM` API, with no natives and no access to the sealed operations, so they
are also examples of what users can build. An `MVar` is a `Var (Maybe a)`
whose blocking operations `retry`.

A `Queue` is the two-list functional queue split across variables: producers
prepend to `back`, consumers take from `front`, and a consumer touches `back`
only to move it, reversed, into an empty `front`. A bounded queue splits its
free-slot count after `TBQueue`: producers decrement `pushRoom` and
consumers increment `popRoom`, and only a producer that finds `pushRoom`
exhausted reads `popRoom`, moves it over, and retries if it is zero. The
`try` operations are `orElse` over the blocking ones.

Measured once with an instrumented runtime, one producer and one consumer
moving 20,000 values each through a queue kept non-empty conflicted on about
1.7% of 40,000 commits unbounded and 7% bounded at capacity 1,000, against
78% for the same traffic on a single `Var (List a)`. The bounded rate is the
producer collecting freed slots; neither figure is asserted.

The [job pipeline example](../../examples/jobs.fango) composes both with a
worker pool, a counting semaphore, and a timeout. Its semaphore is a `Var Int`
and `check`, and its timeout a `Var Bool` that a timer task in the caller's
scope sets, chosen against a result slot with `orElse`; both stay program
code rather than library API.

## Runtime

The runtime follows TL2 with a global version clock and per-variable locks.
Every published value is an immutable node carrying the version of the commit
that wrote it, and every variable has a creation-order number and a lock held
only by a commit publishing it.

- **Begin** records the clock as the attempt's read version.
- **Read** answers the attempt's newest buffered write, if any. Otherwise a
  locked variable is a conflict; an unlocked one answers its node, unless the
  node's version is above the read version, which is also a conflict. Every
  attempt therefore reads one consistent snapshot (opacity).
- **Write** buffers the value. **New** publishes the initial value at version
  0 at once, which is safe because nothing else reaches the variable before
  the attempt commits; a discarded attempt leaves it unreachable.
- **Commit** of an attempt without writes succeeds at once: its reads already
  proved the snapshot consistent. A writing commit locks the variables it
  writes in creation order, so two commits never wait on each other in a
  cycle, and takes the next clock version only after locking. Unless that
  version directly follows its read version, it then checks that no variable
  it read is locked by another commit or has a version above its read
  version. It publishes each variable's final write at the new version and
  unlocks. An attempt that begins after the version was taken finds every
  written variable locked or already published; one that began earlier
  rejects the new nodes as stale. Commits with disjoint write sets proceed in
  parallel; the clock increment is the one shared step.
- **Wait.** A blocked attempt registers one waiter, with a one-slot channel,
  on every variable it read, then checks those variables again: a version
  above its read version means a commit raced the registration, and it
  returns at once. Otherwise it selects on the channel and the scope's
  cancellation, as `AsyncSleep` does. A writing commit, after unlocking,
  signals and removes the waiters of each variable it wrote
  under that variable's own mutex. The registration and the commit's waiter
  scan are ordered by that mutex, and publication precedes the scan, so a
  waiter either is found by the scan or sees the newer version on its check.
  A waiter withdraws from every variable on return, whether woken or
  cancelled. An attempt that read nothing waits for cancellation alone.

## Verification

Fixtures assert final state and invariants, never interleavings or retry
counts, which depend on scheduling. Waiting fixtures synchronize through
variables, such as a flag a task sets before it blocks, and never rely on
timing for their outcome.
Runtime unit tests cover commit validation, publication order, rollback,
and waiter registration directly, under the race detector; one counts the
registrations a cancelled waiter leaves.

The [runtime benchmarks](../../runtime/fangort/stm_test.go) measure commit
scaling. With one global commit mutex, attempts each writing their own
variable committed at a flat 215–240 ns per commit from one to eight CPUs,
while read-only attempts scaled from 14 to 1.9 ns. Per-variable locks
brought the writing commits to 165 ns on one CPU and about 105–120 ns on four
and eight; past that the shared clock dominates.
