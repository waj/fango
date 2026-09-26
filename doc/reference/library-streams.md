# Streams and iterators

[Reference index](../reference.md). Sources: [Stream](../../stdlib/Stream.fango),
[Iterator](../../stdlib/Iterator.fango).

A stream carries an initial state and an ordinary step function:

```fango
type Stream state a e =
    { initial : state
    , step : state ->{e} Maybe (a, state)
    }
```

`Stream.unfold initial step` constructs a stream. Each step returns `Nothing`
for exhaustion or `Just (element, nextState)`. `Stream.fromList` uses the
remaining list as its state. There is no `yield` or suspended producer stack.

`map` transforms elements, `filter` advances until a matching element is found,
`take` adds a remaining count to the state, and `zip` pairs the two states.
Construction does not execute the callbacks. `take n` with `n <= 0` never
steps its input. Zip steps left before right and stops when either ends; it
may consume one unmatched left element when the right stream ends first.

`fold combine initial stream` calls `combine element accumulator` in traversal
order. `forEach` discards results and `toList` preserves element order. Starting
a traversal again starts from the stored initial state. Effects in the step
function still occur on each traversal; a stream over a mutable Reader shares
that reader's state and does not rewind it.

`Iterator.start initial step` constructs an immutable iterator.
`Iterator.next iterator` returns `Maybe (a, Iterator state a e)` with effects
`e`. The caller must use the returned iterator to advance. Calling `next` again
on the same iterator repeats the same state transition, including its effects.
`Stream.withCursor stream use` passes a fresh iterator to `use` as a convenience.

Resources are acquired around the consuming traversal with ordinary cleanup
scopes. Stopping early does not trigger hidden producer cleanup. Recursive
producers can use callback traversal without constructing an iterator.
