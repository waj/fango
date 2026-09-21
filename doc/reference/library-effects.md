# Failure, state, and randomness

Fail and Failure reports, State, and Random runners.

[Reference index](../reference.md). Sources: [Fail](../../stdlib/Fail.fango), [Failure](../../stdlib/Failure.fango), [State](../../stdlib/State.fango), [Random](../../stdlib/Random.fango).

## Fail and Failure

`Fail` is the conventional abort-only failure effect, so programs no longer
declare their own:

```fango
module Fail exposing (Fail, Report(..), attempt, attemptReport, fail, fromResult)

effect Fail error
    abort fail : error -> value

attempt : (() ->{Fail error | e} value) ->{e} Result error value
type Report error = { primary : error, suppressed : List Failure.Failure }
attemptReport : (() ->{Fail error | e} value) ->{e} Result (Report error) value
fromResult : Result error value ->{Fail error} value
```

`fail error` never returns: it unwinds to the nearest enclosing `attempt`,
which answers `Err error`; a normal completion answers `Ok value`. `fromResult`
unwraps an `Ok` and raises an `Err`, so a `Result`-returning call can join a
failing computation with `fromResult (File.read path)`. Nested attempts handle
only the failures raised inside them, and an abort raised by an outer `attempt`'s
clause propagates outward as usual. The effect is not in the prelude: import
`Fail exposing (Fail, attempt, fail)` to use it, and a module that declares its
own `Fail` effect is unaffected.

`attemptReport` also captures secondary cleanup failures. Its `Err` contains
the original typed error in `primary` and detached snapshots in `suppressed`.
`attempt` keeps its existing result type. Snapshots support these pure operations
from the separately imported `Failure` module:

```fango
effectName : Failure -> String
operationName : Failure -> String
argumentCount : Failure -> Int
suppressed : Failure -> List Failure
argument : Int -> Failure -> Maybe a
```

Names identify the effect and operation declarations. `argument` uses a
zero-based index and the requested nominal type, including all type arguments.
For example, `payload : Maybe IO.Error` followed by
`payload = Failure.argument 0 failure` inspects an IO error. Wrong types,
negative or out-of-range indices, and types containing functions or resources
produce `Nothing`. Ordinary immutable private ADTs are inspectable without
exposing their constructors. Redefining a REPL type gives it a new identity;
values of the old type do not match it.

Snapshots expose no handlers or resumptions. They retain opaque payloads, so
capture checks also reject a report whose secondary payload could outlive its
resource. Nested secondary failures retain their own `suppressed` lists.

## State

`State` provides a parameterized state effect and its standard runner:

```fango
module State exposing (State(..), StateResult(..), run)

effect State s
    get : () -> s
    put : s -> ()

type StateResult s a = { value : a, state : s }

run : s -> (() ->{State s | e} a) ->{e} StateResult s a
```

`State.run initial action` evaluates `initial` once, runs `action` with a
private cell, and returns both the action value and final state. `get()` reads
the current state and `put next` replaces it. Cells belong to handler
activations, so nested runs—including two runs of the same `State s`—are
independent.

## Random

`Random` declares a randomness effect and two ready-made handlers:

```fango
module Random exposing (Random(..), int, runSeeded, runSystem)

effect Random
    int : Int -> Int -> Int
```

`int lo hi` requests a draw in `[lo, hi]` (reversed bounds are swapped). It
has no default handler; a program chooses an interpretation by wrapping the
effectful computation in one of

```fango
runSeeded : Int -> (() ->{Random | e} a) ->{e} a
runSystem : (() ->{Random | e} a) ->{IO | e} a
```

`runSeeded seed action` answers every draw from a deterministic 31-bit
linear congruential generator (glibc constants; modulo-biased and not
cryptographic) started at `seed`: the same seed always yields the same
draws, in both the interpreter and compiled programs. `runSystem action`
seeds the same generator from system entropy, so every run draws a fresh
sequence. `examples/guess.fango` performs `Random.int` opaquely and picks
the interpretation with one line in `main`.

The seed is handler-local state. Nested seeded or system runs do not disturb
an outer sequence, and independent runs share no PRNG cell. Deterministic
`runSeeded` computations are allowed during compile-time staging; `runSystem`
is rejected with `COMPILE-TIME EFFECT` because it requires IO.
