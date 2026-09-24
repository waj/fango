# Detached completion

`Completion` records a computation's typed result or outward abort without
unwinding its caller. It is independent of Stream and can be used inside a
[coroutine](library-coroutines.md).

[Reference index](../reference.md).

```fango
capture : (() ->{e} a) ->{e} Completion a e
replay : Completion a e ->{e} a
failure : Completion a e -> Maybe Failure.Failure
```

`Completion a e` is abstract. `capture action` runs `action()` immediately.
Handlers inside the action still handle their own aborts. An abort escaping
them runs the action's pending cleanup before capture returns a completion.
Resumptive effects continue through the action's ordinary evidence; capture
does not interpret them or prevent coroutine suspension.

The call to `capture` retains the complete row `e`, including abort labels.
Capturing a future result is not permission to erase the effects it may need
when observed. The same row indexes the completion and appears on `replay`.
Generic helpers preserve that relationship across module and class boundaries.

`replay completed` returns the saved successful value, or performs the saved
abort against the handlers active at replay. Each replay selects a fresh
handler target. The completion retains the nominal operation and its complete
argument tuple, including curried arguments, and every nested suppressed
cleanup report; it retains no handler target or continuation. Replaying does
not run the original action or its cleanup again.

`failure completed` returns `Nothing` for success and `Just snapshot` for an
abort. The snapshot uses the ordinary [Failure inspection
API](library-effects.md); inspecting it does not perform the abort. Replay
preserves opaque payloads even when public inspection cannot expose them.

```fango
import Runtime.Completion
import Fail

main() =
    print (Fail.attempt (\_ ->
        completed = Runtime.Completion.capture (\_ ->
            Fail.fail "saved"
            0)
        print "captured"
        Fail.attempt (\_ -> Runtime.Completion.replay completed)))
```

This prints `captured` followed by `Ok Err saved`: the inner handler installed
after capture catches replay. The original outer handler is not remembered.

Completion does not make captured resources shareable or extend their lifetime.
Successful values, primary failure payloads, and suppressed reports retain their
transitive capture obligations. Returning a completion that contains a resource
from its owning cleanup scope is a `RESOURCE ESCAPES` error. The same check
applies through wrappers and stored callbacks.

To include a coroutine's final cleanup in a completion, capture the entire
`Runtime.Coroutine.with` call. A capture inside the producer encloses only cleanup
inside that action. Source code has no owner-stop operation; closing an owner
abandons its unfinished computation and reports cleanup failures through the
coroutine lifecycle contract.

A retained local abort handler can still handle a cleanup failure during close.
Its clause runs synchronously; the interrupted producer does not continue or
publish its pending completion. If that clause fails, the original cleanup
failure and its nested suppressed reports remain attached to the new failure.
