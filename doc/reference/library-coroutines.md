# Scoped coroutines

`Coroutine` provides owned, resumable computation with typed requests, replies,
and final results. Import it qualified; `Step` has public constructors and
derived `Eq` and `Show` instances.

```fango
type Step request result = Suspended request | Finished result | Closed

with : ((request ->{Suspension} reply) -> reply ->{Suspension | e} result)
    -> (Coroutine request reply result e ->{Drive | e} a) ->{e} a
advance : Coroutine request reply result e -> reply ->{Drive | e} Step request result
close : Coroutine request reply result e ->{Drive | e} ()
```

`Coroutine` is an abstract resource type. `Suspension` and `Drive` are nullary
control labels with no exported operations. The checker tracks the particular
owner behind each pause or advance: a boundary removes only its own control,
even when foreign control has the same printed label.

## Exchange and lifecycle

```fango
Coroutine.with (\pause initial ->
    answer = pause ("hello " ++ initial)
    answer ++ "!") (\work ->
    first = Coroutine.advance work "Ada"
    second = Coroutine.advance work "received"
    (first, second))
```

This returns `(Suspended "hello Ada", Finished "received!")`. The first
advance supplies the producer's initial input; each later advance answers its
pending pause. Request, reply, and final result types may all differ.

`with` invokes its driver immediately but defers both producer applications
until the first advance. Constructing arguments is still strict, including
arguments to terminal advances and partial applications.

| Operation | Behavior |
| --- | --- |
| Advance an unstarted owner | Apply the producer to its pause callback and initial input |
| Advance a suspended owner | Return the reply from its pending pause and continue |
| Producer pauses locally | Return `Suspended request`, retaining execution and cleanup |
| Producer returns | Complete cleanup, then return `Finished result` exactly once |
| Advance a terminal owner | Return `Closed` |
| Close before starting | Discard the producer without invoking it |
| Close suspended production | Abandon execution and run pending cleanup |
| Close a terminal owner | Return Unit without retrying cleanup |
| Leave `with` | Close unfinished production before returning the driver answer |

An outward failure terminates production before reaching the outer handler.
Catching it around an advance leaves later advances returning `Closed`. A
cleanup failure prevents `Finished`; a failed close still leaves the owner
closed. Driver failures remain primary, with close failures suppressed under
the usual [cleanup precedence](resources.md#cleanup-failures).

## Ownership and effects

Aliases share the same execution. Sequential use is legal; overlapping advance
or close is rejected with `ITERATOR ADVANCEMENT CONFLICT`. An unfinished advance
keeps its exclusive access across suspension to an enclosing coroutine.

The pause callback belongs to the active producer and its synchronous
descendants. A nested producer may call an enclosing active producer's pause;
the inner advance remains pending until that foreign suspension resumes. The
driver cannot invoke pause independently or receive it in a request/result.
The checker preserves these obligations through helpers, closures, ADTs,
dictionaries, recursive calls, and module contracts.

Handles and pause authority cannot escape their owner. Requests and final
results may borrow enclosing resources but cannot retain producer-local
resources. Every input/reply is conservatively retained until the receiving
owner closes, even when unused or passed to a terminal advance. Its captures
must therefore outlive that owner. Violations report `RESOURCE ESCAPES`;
closing a value does not erase its static lifetime.
Recursive helpers preserve obligations; when recursive allocation makes owner
identity or lifetime ordering uncertain, the checker conservatively rejects it.

Definition-site handlers retain their activation. Uncaptured residual effects
use the current advance's evidence, allowing successive advances to supply
different interpretations. Cleanup uses definition-site evidence. Foreign
control remains in the residual row; a synchronous annotation cannot hide it
and reports an effect mismatch. Core independently rejects falsely synchronous
execution contracts.

[Work](library-work.md) packages existing coroutines behind a scoped effect
budget. [Completion](library-completion.md) captures typed outcomes for later
replay. Closing a coroutine uses a private stop outcome: user abort handlers
cannot catch the stop or resume abandoned production. Cleanup handlers can
still handle cleanup failures.

## Limits

Acquisition and release must remain synchronous. There is no dynamic allocation
scope, public scheduler, public cancellation operation, or concurrent execution API.
The ordinary [cooperative scheduler fixture](../../testdata/run/coroutine_scheduler.fango)
demonstrates lexical coroutines, a FIFO ready queue, voluntary yield, fake waits,
typed completion, and abandonment while a task is suspended inside a Stream pull.
Stream and Iterator use the same Coroutine protocol. Staging and the REPL
use the same ownership checks and retain their existing native-call policy.
Later work is listed in the [coroutine roadmap](../roadmap-coroutines.md#implementation-stages).
