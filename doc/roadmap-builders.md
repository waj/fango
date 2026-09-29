# Roadmap: builder blocks and generators

This is a proposal, not a description of current Fango syntax or APIs. It
explains a small source-level translation that would let a library offer
generator notation without restoring compiler-generated coroutine machines.
The [current Stream](reference/library-streams.md) has an explicit state and
step function; the [current handlers](reference/effects.md#resume-discipline)
cannot retain a resume for the next pull. The [synchronous foundation](design/effects.md)
remains the architecture for ordinary calls and effects.

The proposed `build` form is also useful to libraries other than generators.
It names a module whose ordinary functions supply sequencing and delay. It is
not a new algebraic effect handler: `yield` in this proposal builds a producer
value, and a call to an ordinary function cannot suspend its caller.

## Start with a producer value

Imagine a bundled `Generator` module with this private representation. These
declarations are illustrative; they are not implemented.

```fango
type Producer a e result
    = Done result
    | Emit a (Producer a e result)
    | Defer (() ->{e} Producer a e result)

type Step a e result
    = Finished result
    | Produced a (Producer a e result)
```

`Done` records a completed producer. `Emit` holds one element and the position
to use on the next pull. `Defer` holds work that must wait until a pull. Its
closure may perform effects `e`. The consumer advances the value with an
ordinary recursive function:

```fango
next : Producer a e result ->{e} Step a e result
next producer = case producer of
    Done result -> Finished result
    Emit value rest -> Produced value rest
    Defer work -> next (work())
```

For example, this value describes two elements without running `print` when
the value is constructed:

```fango
numbers = Emit 10 (Defer { _ ->
    print "between"
    Emit 20 (Done ())
})
```

The first `next numbers` finds `Emit 10` and returns its `Defer` tail. The
second `next` invokes that tail, prints `between`, and returns `20`. A third
pull finds `Done`. Nothing saves a Go or interpreter call stack: the remaining
work is an ordinary Fango value containing a closure.

The representation can be the state of today's stream:

```fango
toStream : Producer a e () -> Stream.Stream (Producer a e ()) a e
toStream producer = Stream.unfold producer step
```

Here `step` calls `next`, maps `Finished ()` to `Nothing`, and maps
`Produced value rest` to `Just (value, rest)`. `toStream` discards the
producer's final result because the present Stream step type has no place for
it. Direct users of `next` can observe that result.

## Why the tail must be delayed

Fango evaluates constructor arguments strictly. This attempted infinite
producer never finishes construction:

```fango
countFrom n = Emit n (countFrom (n + 1))
```

The recursive call must sit inside a closure:

```fango
countFrom n = Emit n (Defer { _ -> countFrom (n + 1) })
```

Constructing `countFrom 0` now creates one `Emit` and one `Defer`. Each pull
does finite work and returns another producer position. A consumer can take a
finite prefix without constructing the rest. Productivity is still the
producer's responsibility: a chain of `Defer` values that never reaches
`Emit` or `Done` makes one pull diverge.

## The proposed surface form

The compiler could write those closures for the programmer:

```fango
numbers : Generator.Producer Int IO ()
numbers =
    build Generator
        yield 10
        Generator.lift { _ -> print "between" }
        yield 20
        return ()
```

`build Generator` chooses a module at compile time. The statements mean:

| Source form | Builder call | Purpose |
| --- | --- | --- |
| `return value` | `Generator.pure value` | Complete with a result. |
| `yield value` | `Generator.yield value` | Supply one element. Available only if the module exports `yield`. |
| `action` | `Generator.bind action { _ -> rest }` | Sequence an action returning Unit. A final action is the block result. |
| `name = action` | `Generator.bind action { name -> rest }` | Use an action's result in following statements. |

The compiler wraps each block or remaining suffix in `Generator.delay` so
its ordinary expressions run when the builder chooses, not while the block is
constructed. In particular, it evaluates `yield value`'s argument on demand.
The proposed `Generator.lift` is a library function that turns an effectful
Unit callback or other effectful callback into a producer action; `lift` is
not part of the compiler's builder protocol. Requiring it explicitly keeps
ordinary Fango expressions distinct from producer actions.

The meaning of `name = action` is specific to a `build` block: it binds the
result *inside* the builder. In an [ordinary Fango block](reference/syntax.md#blocks-and-sequencing),
`name = expression` remains an eager local binding. A builder block does not
infer the choice from the expression's type. To name an ordinary computed
value within the proposed block, embed it explicitly:

```fango
build Generator
    doubled = Generator.pure (count * 2)
    yield doubled
    return ()
```

This extra `pure` keeps action binding and local evaluation unambiguous. In
the first version, only simple name bindings are proposed inside `build`;
ordinary local function declarations can stay outside it.

`if` and `case` could select among producer-valued branches inside the delayed
block. For an initial version, `yield` occurs only as a block statement, not
inside an operand, guard, or ordinary lambda. Bare actions and `=` bindings
accept producer actions returned by helper functions:

```fango
twice value =
    build Generator
        yield value
        yield value
        return ()

example =
    build Generator
        twice 7
        yield 9
        return ()
```

An ordinary helper that wishes to yield returns `Producer` and is composed in
the block. A plain function returning an ordinary value cannot call `yield`
and suspend the block through its call. This restriction keeps ordinary
calling conventions unchanged.

## Builder-module contract

The compiler recognizes the syntax but does not know a `Builder` type class.
It resolves the selected module's named top-level functions and type-checks
their applications through existing inference. Every builder must provide
`pure`, `bind`, and `delay`; a block using `yield` additionally requires
`yield`. There is no first-class builder record or higher-kinded parameter.
Fango's [current class restrictions](reference/classes.md) do not admit the
higher kinds or method-local polymorphism such a record-like interface would
typically need.

For `Generator`, the expected signatures are:

```fango
pure  : result -> Producer a e result
bind  : Producer a e x
        -> (x -> Producer a e result)
        -> Producer a e result
delay : (() ->{e} Producer a e result) -> Producer a e result
yield : a -> Producer a e ()
lift  : (() ->{e} x) -> Producer a e x
```

`bind` sequences a completed result into its continuation and carries that
continuation past each `Emit`; `delay` stores a thunk rather than calling it
immediately. These are the library's timing promises, not properties that the
types alone can prove. One possible implementation makes the contract more
concrete:

```fango
pure value = Done value
delay work = Defer work
yield value = Emit value (Done ())
lift work = Defer { _ -> Done (work()) }

bind producer use = Defer { _ -> case producer of
    Done value -> use value
    Emit value rest -> Emit value (bind rest use)
    Defer work -> bind (work()) use
}
```

Each call to `bind` constructs a `Defer`; it does not advance its input.
On a pull, `Done` passes its result to `use`, `Emit` returns an element while
retaining `use` for the tail, and `Defer` executes one pending action.
Repeated pulls of the same producer position may repeat its effects; a
consumer advances by using the returned tail. A builder should give `pure`
and `bind` their usual sequencing laws, but the compiler checks types rather
than proving those laws or `delay`'s timing.

`pure`, `bind`, and `delay` in another module may have
different concrete types, provided the lowered calls type-check and the module
defines coherent sequencing and timing. `yield` is optional and its meaning
belongs to that module. A parser builder, for example, could use the same
`return`, bare-action, and `=` forms without providing `yield`:

```fango
pair =
    build Parser
        left = Parser.integer
        right = Parser.integer
        return (left, right)
```

`Parser` and its operations are only an example of the generic syntax; no
parser builder is proposed for the first delivery. The module is selected by
name, rather than inferred from the block's expected result type, so name
resolution and error messages have an unambiguous target. The name must resolve
through an ordinary import; a builder is not a runtime value.

## Translation boundary

The compiler would lower a `build` block into ordinary calls and lambdas after
resolving its builder module and before type inference. In schematic notation,
where `B` is that module and `rest` is the remaining block:

```text
build B { return value }        => B.delay { _ -> B.pure value }
build B { yield value; rest }   => B.delay { _ ->
                                     B.bind (B.yield value) { _ -> lower(rest) } }
build B { action; rest }        => B.delay { _ ->
                                     B.bind action { _ -> lower(rest) } }
build B { name = action; rest } => B.delay { _ ->
                                     B.bind action { name -> lower(rest) } }
```

`lower(rest)` is itself delayed. A final bare action lowers to a delayed
`action`; a final `yield value` completes with Unit according to the builder's
`yield` result. Branches lower recursively, with conditions evaluated when
their enclosing `delay` runs. Generated lambdas retain source spans for
diagnostics and use hygienic binders. The lowering must preserve the existing
left-to-right evaluation order of value expressions and effects.
As in ordinary Fango blocks, a non-final bare action must return Unit; a
non-Unit result needs a `name = action` binding or an explicit discard action.
An `=` right-hand side must produce a builder action, not a plain value.

For a smaller complete translation:

```fango
build Generator
    yield 10
    return ()
```

becomes, schematically:

```fango
Generator.delay { _ ->
    Generator.bind (Generator.yield 10) { _ ->
        Generator.delay { _ -> Generator.pure () } }
}
```

Construction creates the outer delayed value. The first `next` invokes it,
and `bind` turns the `Emit 10` into a `Produced 10 rest`. The second `next`
uses `rest`; its `Done ()` reaches the continuation, which produces the final
`Done ()`. The longer example inserts `Generator.lift` in that delayed rest,
so `print "between"` happens on the second pull.

For the two-element example, the important shape is:

```text
delay { _ -> bind (yield 10) { _ ->
    delay { _ -> bind (lift { _ -> print "between" }) { _ ->
        delay { _ -> bind (yield 20) { _ ->
            delay { _ -> pure () } } } } }
```

The actual AST need not duplicate `delay` where a builder law makes one
redundant, but it must retain the demand boundary before the second element.
Inference sees ordinary applications and effectful closures. Elaboration,
Core lint, the interpreter, and Go emission continue through their existing
paths; no generator node or suspension mode enters Core.

## Infinite source in block syntax

With the same translation, recursion after a yield is delayed:

```fango
countFrom n =
    build Generator
        yield n
        countFrom (n + 1)

firstTen = Stream.toList (Stream.take 10 (Generator.toStream (countFrom 0)))
```

The lowered value has the essential shape `Emit n (Defer { _ -> countFrom
(n + 1) })`. Constructing the producer does not recurse. Each pull advances
only until its next `Emit`; `take 10` can stop after ten pulls. Traversing the
same initial value again reruns its effects, like the current Stream contract;
this proposal does not add memoization or an independently mutable cursor.

## Scope and cleanup limits

The producer holds closures, not a suspended stack. A resource acquired inside
one `Defer` action must be released before that action returns an `Emit`, unless
the resource is explicitly retained and checked by its own existing API.
Acquisition and cleanup for the whole traversal remain around the consumer,
as with [current streams](reference/library-streams.md). Stopping early does
not run an implicit producer finalizer. Handler evidence captured by a
deferred closure must still satisfy the current effect and scope rules.

This proposal does not make a direct-style `Yield` operation work through an
arbitrary ordinary call. That would require retaining the rest of that call's
execution, with different control, cleanup, and evidence contracts. Nor does
it restore owned coroutine execution.

## Delivery and verification

1. Prototype `Producer`, `next`, `bind`, `delay`, `yield`, `lift`, and the
   Stream bridge as library code using existing Fango syntax. Establish that
   construction is pure, pulls are bounded, and recursive tails are delayed.
2. Add the smallest `build Module` block grammar and an AST lowering pass.
   Keep generated calls resolved by module identity, preserve spans, and make
   missing builder operations ordinary, specific diagnostics. Support ordered
   statements and result binding first; add branch forms only when their
   translation and type errors are clear.
3. Cover finite and infinite producers, effects between elements, a producer
   helper composed as a bare action, taking zero elements, repeated traversal,
   early stop, and cross-module use in both the interpreter and generated Go.
   Add a second small builder fixture to prove that syntax is not keyed to
   `Generator` or `Stream` identities.

When implemented, move the durable syntax and diagnostics to the reference,
the lowering invariant to design, and library behavior to the Stream or a new
Generator reference topic. Update the VS Code grammar and tokenizer fixture
for new syntax, retain Core lint and differential checks, run `make
test-grammar`, `make test`, and `go vet ./benchmarks`. Benchmark only on an
otherwise idle machine if measurements are needed to judge this allocation
tradeoff.

## Decisions before implementation

- Choose the exact spelling of the module-directed block. The examples use
  `build`, `yield`, and `return` to explain the translation. The proposed
  action-binding and action-sequencing forms deliberately reuse `=` and bare
  expression statements inside that block.
- Decide whether `Producer` is public with constructors exposed or abstract
  with `next` and library combinators as its only interface. This does not
  change the source lowering.
- Decide whether an initial builder block permits `if` and `case` immediately
  or introduces them after the statement forms. Neither requires backend
  suspension, but both need explicit branch-result rules and diagnostics.
