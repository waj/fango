# Roadmap: software transactional memory

A proposed `STM` library modeled on Haskell's `Control.Concurrent.STM`:
transactional variables, composable atomic transactions, blocking `retry`,
alternatives, and the derived `MVar` and `Queue` structures. Completed
stages are marked DONE; their contract lives in the
[STM module](../stdlib/STM.fango) and the [STM design](design/stm.md). The [main roadmap](roadmap.md) is the navigation entry point;
implemented contracts that this proposal builds on live in
[effects](reference/effects.md), [effect execution](design/effects.md),
[native tasks](design/tasks.md), and [native sidecars](reference/native.md).
Stage IDs and titles follow the [repository milestone rules](../AGENTS.md).
Implementation details below are a starting point and may change as stages
land; promote the settled contract into design/reference and the module's own
source comments.

## Motivation

[`State.runAtomic`](../stdlib/State.fango) makes one cell's updates atomic,
and [`Async.Channel`](../stdlib/Async.fango) transfers values between tasks.
Neither composes: two atomic cells cannot be updated together, and a task
cannot wait for "a value on either channel, or a flag". Transactions compose
both ways. Several variables change atomically, and `retry` with `orElse`
turns any condition over shared state into a blocking wait. Queues,
semaphores, and timeouts then become ordinary library code.

The language already has what the design needs:

- **Operation-local polymorphism.** `readVar : Var a -> a` lets one
  unparameterized `STM` effect serve variables of every type.
- **Mixed effects.** One effect holds resumptive reads and writes beside the
  aborting `retry`.
- **Closed rows.** A transaction typed `() ->{STM} a` cannot perform `IO`,
  `Async`, or any effect handled outside it, so replaying it is unobservable.
- **Replay by re-invocation.** Restarting calls the transaction closure again;
  no continuation is captured.
- **Uninheritable abort effects.** A transaction never reaches another task,
  so its log needs no synchronization.
- **Export lists.** The module exports the `STM` label but not its
  operations, so no other module can handle it. A handler must name every
  operation, and unexported operations cannot be named. STM is sealed without
  a language feature.

## Surface

### Variables and transactions

```fango
module STM exposing (STM, Var, atomically, attempt, check, exchange, modify, new, orElse, read, retry, write)

-- A shared mutable variable holding an `a`. Its identity, not its value,
-- distinguishes it; it may cross task boundaries freely.
{-# resource #-}
type Var a = Var Runtime.Native.Any

-- Running inside a transaction. Only `atomically` handles it.
effect STM
    -- (operations unexported; see Implementation)

atomically : (() ->{STM} a) ->{Async} a
new        : a ->{STM} Var a
read       : Var a ->{STM} a
write      : Var a -> a ->{STM} ()
modify     : Var a -> (a -> a) ->{STM} ()
exchange   : Var a -> a ->{STM} a
retry      : () ->{STM} a
check      : Bool ->{STM} ()
orElse     : (() ->{STM} a) -> (() ->{STM} a) ->{STM} a
attempt    : (() ->{STM, Fail error} a) ->{STM} Result error a
```

| Fango | Haskell | Meaning |
| --- | --- | --- |
| `atomically` | `atomically` | Runs a transaction until it commits. |
| `new` | `newTVar` | A fresh variable; outside a transaction, `atomically { STM.new x }`. |
| `read`, `write` | `readTVar`, `writeTVar` | Reads see the transaction's own earlier writes. |
| `modify`, `exchange` | `modifyTVar'`, `swapTVar` | Named after `State.modify` and `State.exchange`. |
| `retry` | `retry` | Abandons the attempt and waits until a variable it read changes. |
| `check` | `check` | `retry` unless the condition holds. |
| `orElse` | `orElse` | Runs the second branch if the first retries, discarding the first branch's writes. |
| `attempt` | `catchSTM` | Runs a failing action; an `Err` discards the action's writes. |

### Examples

A transfer that blocks until the source can cover it:

```fango
import Async
import STM exposing (STM)

transfer : STM.Var Int -> STM.Var Int -> Int ->{STM} ()
transfer from to amount =
    balance = STM.read from
    STM.check (balance >= amount)
    STM.write from (balance - amount)
    STM.modify to { n -> n + amount }

main() =
    Async.run {
        alice = STM.atomically { STM.new 100 }
        bob = STM.atomically { STM.new 0 }
        payment = Async.spawn { STM.atomically { transfer alice bob 150 } }
        STM.atomically { STM.modify alice { n -> n + 50 } }
        Async.await payment
        print (show (STM.atomically { (STM.read alice, STM.read bob) }))
    }
```

If the transfer runs before the deposit, it finds 100 and retries until the
deposit's commit wakes it; either way the final read prints `(0, 150)`.

Choosing between sources, without a select primitive:

```fango
nextJob : Queue Job -> Queue Job ->{STM} Job
nextJob urgent normal = STM.orElse { Queue.pop urgent } { Queue.pop normal }
```

All-or-nothing failure:

```fango
withdraw : STM.Var Int -> Int ->{STM, Fail String} ()
withdraw account amount =
    STM.modify account { n -> n - amount }
    if STM.read account < 0 then Fail.fail "overdrawn" else ()

payBoth : STM.Var Int -> STM.Var Int ->{Async} Result String ()
payBoth first second =
    STM.atomically { STM.attempt { withdraw first 10; withdraw second 20 } }
```

An `Err` from the second withdrawal also undoes the first. The transaction
still commits, with no writes, so `payBoth` returns the `Err` rather than
retrying; `Fail.fromResult` raises it in the caller.

## Semantics

**What a transaction may perform.** The callback row is closed: `{STM}`
exactly. Any effect the transaction uses must therefore be handled inside it,
in one of two ways:

1. Discharged locally, such as `Fail.attempt`, `Runtime.Local.run`, a local
   `State.run`, or a `Parser`. Replay installs fresh handlers, so the outcome
   is the same.
2. Interpreted onto STM by a handler whose clauses perform STM; see
   [effect bridges](#effect-bridges).

Effects handled outside, including `IO`, `Async`, `Console`, `Random`, the
clock, and an outer `State`, are type errors. Even read-only services are
excluded because a replay would observe a different answer; read them before
`atomically` and pass the values in. Side effects that depend on the
transaction's outcome are returned as data and performed after commit.
Nested `atomically` is rejected for the same reason: it needs `Async`.

**Isolation.** Every attempt observes one consistent snapshot. A read that
would see a value committed after the attempt began restarts the attempt
immediately (opacity), so a transaction never runs on inconsistent values. That
matters because a transaction cannot be interrupted: cancellation is cooperative,
and `Async.checkpoint` is outside its row.

**Commit.** A transaction that only read commits without locking. A writing
transaction validates its reads and publishes its writes as one step; a failed
validation restarts it. Conflict restarts are invisible to the program.

**`retry`.** The attempt's writes are discarded, and the task waits until
another transaction commits a write to one of the variables the attempt read,
then reruns from the start. A `retry` with no reads waits until cancellation.

**`orElse`.** If the first branch retries, its writes are rolled back and the
second branch runs. The first branch's reads are kept, so if both branches retry,
the wait covers the variables either branch read. A failure or conflict in the
first branch is not caught.

**`attempt`.** An `Err` rolls back the action's writes and is returned as a
value; reads are kept for validation. Without `attempt`, `Fail` cannot appear
in a transaction's row.

**Cancellation.** `atomically` responds to cancellation only while waiting
after a `retry`. A transaction that never retries runs to commit even in a
cancelled task, so cleanup code can release a lock or return a permit with
`atomically`. A cancelled waiter leaves no registration behind.

**Variables.** A `Var` lives until it is unreachable and may be shared by any
number of tasks. Creation inside a transaction that later restarts or retries
leaves an unreachable variable. Variables have no equality or `Show`.

## Derived structures

Both modules are ordinary Fango over the public `STM` API, which keeps them
honest examples of what users can build.

### MVar

`STM.MVar` is Haskell's `TMVar`: a variable that is either full or empty.

```fango
module STM.MVar exposing (MVar, empty, isEmpty, new, put, read, swap, take, tryPut, tryRead, tryTake)

type MVar a = MVar (STM.Var (Maybe a))

new     : a ->{STM} MVar a
empty   : () ->{STM} MVar a
take    : MVar a ->{STM} a         -- retries while empty, leaves it empty
put     : MVar a -> a ->{STM} ()   -- retries while full
read    : MVar a ->{STM} a         -- retries while empty, leaves it full
swap    : MVar a -> a ->{STM} a    -- retries while empty
tryTake : MVar a ->{STM} Maybe a
tryPut  : MVar a -> a ->{STM} Bool
tryRead : MVar a ->{STM} Maybe a
isEmpty : MVar a ->{STM} Bool
```

```fango
take (MVar var) =
    case STM.read var of
        Just value ->
            STM.write var Nothing
            value
        Nothing -> STM.retry ()
```

A one-shot result slot, a mutex (`MVar ()`), and a handoff all follow.

### Queue

`STM.Queue` covers Haskell's `TQueue` and `TBQueue`: a FIFO queue, unbounded or
bounded.

```fango
module STM.Queue exposing (Queue, bounded, drain, isEmpty, peek, pop, push, tryPeek, tryPop, tryPush, unbounded)

unbounded : () ->{STM} Queue a
bounded   : Int ->{STM} Queue a       -- a capacity below 1 is treated as 1
push      : Queue a -> a ->{STM} ()   -- retries while full
tryPush   : Queue a -> a ->{STM} Bool
pop       : Queue a ->{STM} a         -- retries while empty
tryPop    : Queue a ->{STM} Maybe a
peek      : Queue a ->{STM} a         -- retries while empty
tryPeek   : Queue a ->{STM} Maybe a
drain     : Queue a ->{STM} List a    -- removes and returns everything, oldest first
isEmpty   : Queue a ->{STM} Bool
```

The representation is the two-list functional queue, split across variables
so that producers and consumers do not conflict while the front is non-empty:

```fango
type Queue a =
    {
      front : STM.Var (List a)   -- oldest first; consumers read here
    , back : STM.Var (List a)    -- newest first; producers prepend here
    , limit : Int                -- 0 for unbounded
    , pushRoom : STM.Var Int     -- free slots known to producers
    , popRoom : STM.Var Int      -- slots freed by consumers, not yet moved
    }

pop queue =
    case STM.read queue.front of
        [value | rest] ->
            STM.write queue.front rest
            freeSlot queue
            value
        [] ->
            case List.reverse (STM.read queue.back) of
                [] -> STM.retry ()
                [value | rest] ->
                    STM.write queue.back []
                    STM.write queue.front rest
                    freeSlot queue
                    value
```

For a bounded queue, a consumer increments `popRoom` and a producer decrements
`pushRoom`; only when `pushRoom` reaches zero does a producer read `popRoom`,
move it over, and retry if it is still zero. This is `TBQueue`'s split
counter, which keeps the common push and pop from touching the same variable.
An unbounded queue skips both counters.

Closing is deliberately absent: a closed flag is one more variable, and a
caller that needs one can pair a `Queue` with an `STM.Var Bool` and `orElse`.
See the open decisions.

## Extensibility

STM is sealed: no module can add an operation or handle the effect. Everything
else composes through the public API, and these cases show how far that goes
without a consumer yet.

### User-defined structures

A counting semaphore is a variable and a `check`:

```fango
type Semaphore = Semaphore (STM.Var Int)

acquire : Semaphore ->{STM} ()
acquire (Semaphore permits) =
    available = STM.read permits
    STM.check (available > 0)
    STM.write permits (available - 1)

release : Semaphore ->{STM} ()
release (Semaphore permits) = STM.modify permits { n -> n + 1 }

withPermit : Semaphore -> (() ->{Async | e} a) ->{Async | e} a
withPermit semaphore action =
    Runtime.Scope.bracket
        { STM.atomically { acquire semaphore } }
        { _ -> STM.atomically { release semaphore } }
        { _ -> action () }
```

The release runs during cancellation cleanup because it never retries.
Barriers, read–write locks, and countdown latches have the same shape.

Granularity is the main design choice for user structures. `STM.Var (Dict k v)`
makes every update conflict with every other; `Dict k (STM.Var v)` conflicts
only per key but needs a fixed key set or a second variable for membership.
The library does not choose for users.

### Choice across structures

`orElse` over a list is a select over any STM structures, mixing kinds:

```fango
popAny : List (Queue a) ->{STM} a
popAny queues =
    choose = List.foldr { queue rest -> { STM.orElse { Queue.pop queue } rest } } { STM.retry () } queues
    choose ()
```

### Timeouts

Haskell's `registerDelay` is a variable flipped by a timer. Here it is a
child task of the current scope:

```fango
delay : Int ->{Async} STM.Var Bool
delay milliseconds =
    flag = STM.atomically { STM.new False }
    ignore (Async.spawn { Async.sleep milliseconds; STM.atomically { STM.write flag True } })
    flag

popWithin : Int -> Queue a ->{Async} Maybe a
popWithin milliseconds queue =
    expired = delay milliseconds
    STM.atomically {
        STM.orElse { Just (Queue.pop queue) } { STM.check (STM.read expired); Nothing }
    }
```

The timer task belongs to the caller's scope and is cancelled when it ends.

### Effect bridges

A handler whose clauses perform STM interprets another effect inside a
transaction. Its writes are logged, so they roll back and replay with the
transaction:

```fango
runVar : STM.Var s -> (() ->{State s | e} a) ->{STM | e} a
runVar var action =
    handle action() on
        get () -> resume (STM.read var)
        put next -> resume (STM.write var next)
        update step ->
            (result, next) = step (STM.read var)
            STM.write var next
            resume result
```

Existing `State` code then runs atomically against shared state:
`STM.atomically { runVar counter { State.modify { n -> n + 1 } } }`. Domain
effects work the same way. An `Inventory` effect can have an STM interpretation
for production and a plain `State` one for deterministic tests, with callers
written once against `Inventory`.

### What cannot be extended

- **No new primitives.** Commit hooks, `unsafeIOToSTM`, and invariants are
  impossible outside the module. Post-commit work is returned as data.
- **No alternative interpretations of STM itself.** Tests run structures under
  the real `atomically`; a single-task test is deterministic.
- **No cross-mechanism waiting.** A transaction cannot wait on an
  `Async.Channel` or a task; the `delay` pattern bridges such sources by a task
  that writes a variable.

## Implementation

### Module layout

| File | Contents |
| --- | --- |
| `stdlib/STM.fango` | `Var`, the sealed effect, `atomically`, the public operations, and private native declarations |
| `stdlib/STM.native.go` | The sidecar wrapping the runtime below |
| `runtime/fangort/stm.go` | Variables, logs, the clock, commit, and waiting |
| `stdlib/STM/MVar.fango` | `MVar` over `STM.Var (Maybe a)` |
| `stdlib/STM/Queue.fango` | `Queue` over split list variables |

### The effect and its handler

```fango
effect STM
    readVar : Var a -> a
    writeVar : Var a -> a -> ()
    newVar : a -> Var a
    savepoint : () -> Int
    rollback : Int -> ()
    abort abandon : () -> value

-- How one attempt ended. Private.
type Attempt a = Finished a | Conflicted | Blocked

-- Raised by a read clause that sees a newer version. Private.
effect Restart
    abort restart : () -> value

retry () = abandon ()

atomically body =
    context = Async.contextToken ()
    attemptLoop context body

attemptLoop context body =
    log = beginLog ()
    case runAttempt log body of
        Finished value -> if commitLog log then value else attemptLoop context body
        Conflicted -> attemptLoop context body
        Blocked ->
            if awaitChange context log then attemptLoop context body
            else
                Async.checkpoint ()
                attemptLoop context body

runAttempt log body =
    handle
        handle body() on
            readVar var ->
                result = logRead log var
                if readFresh result then resume (readValue result) else restart ()
            writeVar var value -> resume (logWrite log var value)
            newVar value -> resume (logNew log value)
            savepoint () -> resume (logSavepoint log)
            rollback point -> resume (logRollback log point)
            abandon () -> Blocked
            return value -> Finished value
    on
        restart () -> Conflicted
```

A clause cannot abort through its own handler, so the stale-read restart goes
through the private `Restart` handler around it. The log is a mutable native
object captured lexically, as `State.runAtomic` captures its cell; no `with`
state is needed, and since nothing is locked while the body runs, an attempt
abandoned by an outer abort or cancellation needs no cleanup.

`orElse` installs a forwarding handler; its clauses run outside their own
activation, so the operations they perform reach the enclosing STM handler:

```fango
orElse first second =
    point = savepoint ()
    handle first() on
        readVar var -> resume (readVar var)
        writeVar var value -> resume (writeVar var value)
        newVar value -> resume (newVar value)
        savepoint () -> resume (savepoint ())
        rollback p -> resume (rollback p)
        abandon () ->
            rollback point
            second()
```

A `retry` in `second` propagates out of the abort clause to the enclosing
handler, as the semantics require. `attempt` needs no handler of its own:

```fango
attempt action =
    point = savepoint ()
    case Fail.attempt action of
        Err error ->
            rollback point
            Err error
        success -> success
```

### Natives

Private, typed as pure, and reached only through the handler, following
`State.runAtomic` and Async:

```fango
{-# resource #-}
type Log = Log Runtime.Native.Any

{-# resource #-}
type ReadResult a = ReadResult Runtime.Native.Any

beginLog : () -> Log
logRead : Log -> Var a -> ReadResult a
readFresh : ReadResult a -> Bool
readValue : ReadResult a -> a
logWrite : Log -> Var a -> a -> ()
logNew : Log -> a -> Var a
logSavepoint : Log -> Int
logRollback : Log -> Int -> ()
commitLog : Log -> Bool
awaitChange : Runtime.Native.Any -> Log -> Bool
```

Values cross under the [representation-blind storage
contract](reference/native.md#indexed-native-storage): the sidecar stores and
returns payload tokens without inspecting them.

### Runtime

The algorithm is TL2 with a global version clock. The first version serializes
writing commits under one mutex; reads and read-only commits take no lock.

```go
var stmClock atomic.Uint64   // version of the latest writing commit
var stmCommit sync.Mutex     // held by one writing commit at a time

type tvarNode struct {
	value   any
	version uint64
}

type TVar struct {
	current atomic.Pointer[tvarNode] // immutable published value
	mu      sync.Mutex               // guards waiters
	waiters []*stmWaiter
}

type stmLog struct {
	readVersion uint64          // stmClock when the attempt began
	reads       []*TVar         // deduplicated by a set in practice
	writes      []stmWrite      // append-only; savepoints are lengths
	latest      map[*TVar]int   // var → index of its newest write
}
```

- **Begin.** `readVersion = stmClock.Load()`.
- **Read.** Answer the newest buffered write if there is one. Otherwise load
  `current`. A version above `readVersion` is stale and answers a conflict;
  otherwise record the variable and answer its value.
- **Write and new.** Buffer `(var, value)`. A new variable starts with version
  0 and its initial value already published, which is safe because nothing
  else can reach it before commit.
- **Savepoint and rollback.** A savepoint is `len(writes)`. Rollback truncates
  and rebuilds `latest`; reads are kept.
- **Commit.** With no writes, succeed at once; the read-time checks already
  proved the snapshot consistent. Otherwise lock `stmCommit`, check that every
  read variable's version is at most `readVersion`, publish each final write
  as a new node with version `stmClock + 1`, then store that version into
  `stmClock`, unlock, and wake waiters on the written variables. Publishing
  nodes before advancing the clock means an attempt that begins later sees
  every node, and one that began earlier either sees old nodes or rejects the
  new ones as stale.
- **Wait.** Create a waiter with a one-slot channel and register it on every
  read variable. Then check the variables again: if any version is above
  `readVersion`, a commit raced the registration, so return immediately.
  Otherwise select on the channel and the context's cancellation, as `SleepIn`
  does. Commit signals and removes waiters under each variable's `mu`; a
  waiter withdraws itself from the rest on return, whether woken or cancelled.

### Representation and transport

- **Representation risk.** Variables are written from clauses, where an
  operation-local `a` is a rigid skolem at the clause's uniform
  representation, and their values are returned through the request/reply
  envelope, which adapts back to the caller's representation. STM1 must
  confirm that Int, Float, records, recursive ADTs, and functions round-trip
  identically in both backends, including values written at one instantiation
  and read at another call site.
- **Exit transport.** `abandon` makes STM an abort effect, so every arrow
  performing it uses Exit transport, and each `orElse` adds one forwarding
  hop for operations inside its first branch. This is accepted for the first
  version; see [Later](#later).
- **Interpreter.** The sidecar runs under the interpreter's native host
  unchanged. `atomically` needs `Async`, so STM is not available at
  compile time.

## Stages

### STM1 Variables and transactions

DONE. See the [STM module](../stdlib/STM.fango) and the
[STM design](design/stm.md).

`Var`, `atomically`, `new`, `read`, `write`, `modify`, and `exchange`, with
stale-read restarts and global-lock commits. No `retry` yet.

Acceptance: concurrent transfers among many variables preserve their total
in both backends; the representation round trips above; a transaction using
a local `Fail.attempt` and `Runtime.Local.run`; golden diagnostics for `IO`
and for nested `atomically` inside a transaction; documented examples in the
module comments.

### STM2 Blocking retry

DONE. See [`STM.retry`](../stdlib/STM.fango) and
[waiting](design/stm.md#runtime).

`retry`, `check`, and waiting on the read set.

Acceptance: a consumer blocked on an empty variable wakes on the producer's
commit; a commit racing registration is not lost; a cancelled waiter returns
promptly and leaves no registration (a runtime unit test counting waiters); a
transaction that never retries commits during cancellation cleanup.

### STM3 Alternatives and failure

`orElse`, `attempt`, and log savepoints.

Acceptance: nested `orElse` rolls back exactly its branch's writes; when both
branches retry, a write to either branch's reads wakes the waiter; `attempt`
undoes writes and keeps reads; a `Fail` without `attempt` is a type error.

### STM4 MVar

The `STM.MVar` module.

Acceptance: a one-shot result handoff and an `MVar ()` mutex protecting a
non-atomic sequence across tasks.

### STM5 Queue

The `STM.Queue` module, unbounded and bounded.

Acceptance: FIFO order under several producers and consumers; a bounded queue
blocks producers at capacity; `popAny` over several queues; producer/consumer
conflict counts on a non-empty queue stay low (measured, not asserted).

### STM6 Driving example

An example program that exercises the library end to end, for instance a job
pipeline with urgent and normal bounded queues, a worker pool, a result
`MVar`, and a timeout built from `delay`. It follows the [example-program
requirements](roadmap-examples.md#delivery-requirements) and may promote
`delay` or a semaphore into the library if the program needs them.

### STM7 Commit scalability

Replace the global commit mutex with per-variable versioned locks acquired in
a fixed order, so commits with disjoint write sets proceed in parallel. Gated
on a measurement showing commit contention.

### Dependencies

| Stage | Depends on |
| --- | --- |
| STM1 | Implemented effects, Async, and native storage |
| STM2 | STM1 |
| STM3 | STM2 (the combined wait set needs waiting) |
| STM4 | STM2 |
| STM5 | STM2; `popAny` and acceptance need STM3 |
| STM6 | STM3, STM4, STM5 |
| STM7 | STM1 and a measurement |

## Open decisions

- **Outside-transaction fast paths.** Whether to add Haskell's `newTVarIO` and
  `readTVarIO` as `newVar : a ->{Async} Var a` and `peek : Var a ->{Async} a`.
  A read-only `atomically` is already lock-free, so they wait for a
  measurement.
- **`retry` with no reads.** Waiting until cancellation matches the semantics
  above; a runtime diagnostic would catch the bug sooner, as GHC's
  "blocked indefinitely" does.
- **Fairness.** A long transaction can keep losing to short ones. GHC accepts
  this; a retry budget that falls back to running the attempt under the
  commit lock would bound it.
- **Closing queues.** Whether `Queue` gains `close` with `pop` answering
  `Maybe`, matching `Async.Channel`, or leaves it to callers.
- **Library helpers.** Whether `delay`, `Semaphore`, and `runVar` ship in the
  library or stay as documented patterns until STM6 or another consumer
  needs them.
- **Variable identity.** Whether `Var` gets an identity equality, which
  Haskell's `TVar` has and which keyed collections of variables need.

## Later

Abort operations currently put an Exit floor on every arrow performing their
effect. Because STM is sealed, the compiler could know its handlers. That
would allow treating reads and writes as a separate resumptive part so that
helpers that never retry stay Direct, and dispatching operations straight to
the log. Stale-read restarts also abort, so this pays off only together with
non-aborting reads, for example multi-version snapshots. Neither is part of
the stages above.

## Verification

Retain the [repository gates](../AGENTS.md) and [verification
contracts](design/verification.md): the Core linter, the interpreter/compiler
differential suite, `go vet`, and `fango doc --stdlib --strict`. Fixtures
assert final state and invariants, never interleavings or retry counts, which
depend on scheduling. Runtime unit tests in `runtime/fangort` cover commit,
publication order, and waiter registration directly, under the race detector.
