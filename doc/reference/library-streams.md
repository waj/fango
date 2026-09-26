# Streams and cursors

Reusable Stream descriptions, transformations, consumers, and Iterator access.

[Reference index](../reference.md). Sources: [Stream](../../stdlib/Stream.fango), [Iterator](../../stdlib/Iterator.fango).

`Stream a e` is an abstract, reusable description of elements `a` whose
production may perform effects `e`. Import `Stream` explicitly:

```fango
import Stream

main() =
    source = Stream.fromList [1, 2, 3, 4]
    Stream.forEach print (Stream.take 2 (Stream.map (\x -> x * 2) source))
```

Construction is pure. Arguments still evaluate strictly, in source order;
producer and transformation callbacks run during traversal. Each traversal
starts production on its first pull, and reopening repeats producer effects.

```fango
generate : (() ->{Yield a | e} ()) -> Stream a e
yield : a ->{Yield a} ()
fromList : List a -> Stream a e
map : (a ->{e} b) -> Stream a e -> Stream b e
filter : (a ->{e} Bool) -> Stream a e -> Stream a e
take : Int -> Stream a e -> Stream a e
fold : (a -> b ->{e} b) -> b -> Stream a e ->{e} b
forEach : (a ->{e} ()) -> Stream a e ->{e} ()
toList : Stream a e ->{e} List a
zip : Stream a e -> Stream b e -> Stream (a, b) e
```

These names belong to `Stream`, including its ordinary, user-handleable `Yield`
effect. Stream interprets `yield` with the producer's scoped
[Coroutine pause callback](library-coroutines.md); a user handler can instead
interpret it like any other resumptive operation.
`fold` passes the element before the accumulator. `take n` starts no upstream
production when `n <= 0`. `zip` pulls left first and can consume one unmatched
left element when right ends. Stages retain bounded buffering; `toList`
intentionally retains every output element.
[Concurrent mapping](library-async-combinators.md#concurrent-stream-mapping)
is available through `Stream.Concurrent` under an Async runner.

Custom consumers use `Stream.withCursor` and `Iterator.next`:

```fango
withCursor
    : Stream a e
    -> (Iterator a e ->{Drive | e} result)
    ->{e} result

next : Iterator a e ->{Drive | e} Maybe a
fromCoroutine : Coroutine a () () e -> Iterator a e
```

Import `Runtime.Coroutine.Drive` for consumer annotations. `Iterator a e` is an ordinary
opaque resource wrapping `Coroutine a () () e`; `Iterator.fromCoroutine` adapts
an existing coroutine without changing its owner or lifetime. `next` advances
with Unit and maps `Suspended value` to `Just value`, and both `Finished ()` and
`Closed` to `Nothing`. Named consumers, aliases, sequential reads, helper
calls, and temporary ADTs or closures can use a cursor within its owner.
Returning it, returning a closure or ADT that retains it, or storing it in an
outer handler reports `RESOURCE ESCAPES`. Unrelated closures may be returned.
Overlapping or possibly overlapping advancement reports
`ITERATOR ADVANCEMENT CONFLICT`. Ordinary handling of Coroutine's `Drive` or
`Suspension` reports `COMPILER-OWNED EFFECT`.

Exhaustion keeps returning `Nothing`. A producer failure closes its cursor.
When a producer's residual effect has no handler at description construction,
each pull supplies its interpretation. A consumer can catch a failure around
one `Iterator.next`; subsequent reads then return `Nothing`. An interpretation
captured lexically when a callback is constructed keeps its original identity.
Scope exit closes unfinished production before returning or propagating
failure. Early stop drives upstream release to completion, including release
callbacks that yield; their yielded values are discarded after the consumer
has stopped. Distinct nested cursors have separate identities. Each yield routes
to its lexical owner, including across nested producer suspensions.

A pure traversal can run in a splice. Compile-time native restrictions and
the evaluation-step budget apply throughout production and consumption,
including loops that never yield. Failed expansions retain REPL rollback.

For example, a reusable file description opens its file during traversal:

```fango
emitLines file = case File.readLine file of
    Nothing -> ()
    Just line ->
        Stream.yield line.text
        emitLines file

lines path = Stream.generate (\_ -> File.withFile path emitLines)

prefix path =
    lines path
        |> Stream.filter (\line -> line /= "")
        |> Stream.take 20
        |> Stream.toList
```

Import `File` and handle the pipeline's `Fail IO.Error` effect at traversal.
File acquisition and release are synchronous. A byte-oriented producer is
[`Reader.chunks`](library-readers.md), which yields a buffered reader's
chunks as they are pulled. A custom stage can use
`withCursor` inside `generate`, reading several input elements and yielding
zero or more outputs. A parser can retain bounded lookahead in ordinary values.
Yielded values may retain resources owned by an enclosing scope, but cannot
retain producer-local resources that a later pull could release. This rule also
applies through aliases, ADTs, and closures.
