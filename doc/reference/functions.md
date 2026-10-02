# Declarations, annotations, and functions

Bindings, calls, function equations, inference, and the self tail-call guarantee.

[Reference index](../reference.md).

Definitions and optional annotations are separate, adjacent declarations:

```fango
square : Float -> Float
square x = x * x

main = square 3.0
```

Arrows associate to the right. Type application uses spaces, such as
`Maybe Int`. User type constructors must always be fully applied.

## Application and lambdas

Functions are curried and application uses whitespace: `f x y`. An attached
empty `()` is a postfix Unit call and binds tighter than whitespace
application: `print foo()` means `print (foo ())`. This applies repeatedly and
after parentheses, as in `foo()()` and `(factory x)()`. Spaced `foo ()` remains
ordinary whitespace application, so `print foo ()` means `(print foo) ()`.
Only empty attached parentheses have this precedence; `print foo(1)` retains
the ordinary whitespace-application grouping `(print foo) 1`. Whitespace or a
comment before `()` makes it an ordinary application.

Partial application and functions as values are supported. Lambdas use
`{ x y -> expression }`. The braced form is an expression atom, including
when passed as an argument:

```fango
Runtime.Scope.bracket acquire release { resource ->
    use resource
}
```

The closing brace ends the body, including operators and semicolon-separated
statements. A list comma follows the complete lambda, so
`[test "one" { checkOne() }, test "two" { checkTwo() }]` contains two
calls. An indented lambda body may contain bindings and Unit statements. A
complete field list such as `{ x = 1 }` is a record; a sibling statement after
a binding, as in `{ x = 1; x }`, makes it a Unit lambda.
`{ expression }` is a function taking Unit; it is equivalent to
`{ () -> expression }`. The body must end in a result expression, so `{}` is
invalid. A lambda with explicit parameters needs at least one pattern.

`value |> function` and `function <| value` are ordinary strict calls to
operators declared in `Basics` and exposed by `Prelude`. They perform the
callback's effects on the final application. For example,
`values |> List.map double |> List.foldl (+) 0` chains leftward;
`print <| 1 + 2` applies `print` to the sum. Mixing `|>` and `<|` without
parentheses is an associativity conflict. In `apply { x -> x } |> finish`, the
pipe follows the call; in `apply { x -> x |> finish }`, it belongs to the
lambda body.

## Function equations

Function and lambda arguments are patterns. Constructor applications must be
parenthesized in an argument position (`map f (Cons x xs)`), while record and
list patterns delimit themselves. `_` discards an argument. A lambda has one
pattern row, so that row must be exhaustive.

Adjacent definitions with the same name and arity form source-ordered
equations for one function:

```fango
withDefault fallback Nothing = fallback
withDefault _ (Just value) = value
```

Blank lines and comments do not split a group; another declaration does. One
annotation immediately above the first equation applies to the group, so a row
carrying its own annotation starts a new definition instead of joining the one
above it. Rows in a group must agree on how many arguments they take, and the
group must be exhaustive and non-redundant. Top-level, local, operator,
instance-method, and deriver-method equations use the same rule. Only
definitions with arguments group: a repeated zero-argument value remains a
duplicate definition. Functions may have indented block bodies, and local
function bindings are supported.

A Unit function is canonically called as `f()`; `f ()` remains equivalent.
An attached definition `f() = body` retains the sole-argument Unit-function
spelling. Spaced Unit is an ordinary exhaustive pattern, so `f () x = body`
has two arguments. The compatible `f _ = body` form remains available.

## Visibility and recursion

Top-level functions with parameters are visible throughout their module,
including `f()` and operator functions. They may call or pass later functions
as values, and mutually recursive groups need no annotations:

```fango
even n = if n == 0 then True else odd (n - 1)
odd n = if n == 0 then False else even (n - 1)
```

Same-name equation rows must still be contiguous. Ordinary values, including
`f = { x -> x }`, and local block bindings remain visible only after their
declarations; native declaration rules are unchanged. Parameters and locals
cannot shadow any module function name, including a later declaration.
Dependencies are checked before callers without changing evaluation order.
A dependency cycle containing an ordinary value reports
`CYCLIC VALUE DEFINITION`; only function-only groups can recurse.

## Inference and annotations

Hindley-Milner inference generalizes each completed dependency group, keeping
recursive calls monomorphic while unrelated helpers remain polymorphic.
Polymorphic values and parameterized ADTs are supported. Numeric polymorphism uses ordinary class constraints, for example
`double : Num a => a -> a`. Variable names such as `number`, `equatable`, or
`printable` have no special meaning. Polymorphic recursion and non-regular
recursive ADTs are rejected. Local value bindings are monomorphic; local
functions and lambda bindings may generalize.

Local helpers may call their enclosing function or a member of its mutually
recursive group, including when the callee's effects are inferred later.
Helpers may carry explicit effect annotations; these must match the effects
their bodies perform, just as for top-level functions.

## Row-kinded parameters

An ADT parameter is inferred as row-kinded when it is used as an open effect-row
tail. This supports effect-indexed declarations without explicit kind syntax:

```fango
type Foo eff = Foo (() ->{IO | eff} ())
```

The row parameter may be used in effect rows, but not as an ordinary value type.
When an ADT parameter is known to be row-kinded,
an effect name is accepted as a singleton row argument, so `Foo IO` means
`Foo {IO}`; parameterized effects use the corresponding application, such as
`Foo (State Int)`. A row itself is also accepted there, written exactly as it
is on an arrow — `Foo {}` for the pure row, `Foo {IO, Fail String}`,
`Foo {IO | e}` — which is the only spelling for a row of more than one label.
A row filling a parameter of ordinary kind is a `KIND MISMATCH`, because a row
has row kind and no other reading. Row-kinded parameters are source-level metadata and are
erased from Core and generated Go representations; the declaration remains
available to inference and reflection.

## Scoped callbacks

A declaration pragma binds a row universally within the final callback:

```fango
{-# scoped s #-}
withText : String -> (Reader s ->{s} a) ->{e} a
withText text use = Reader.withBytes (Bytes.fromString text) use
```

At each call, `s` contains a fresh local permission plus the runner's residual
row `e`. The callback may use both. The runner discharges its fresh permission;
all other effects remain in `e`. Nested calls have distinct permissions and can
use multiple readers together through ordinary row inclusion.

The annotated declaration must bind its final callback to a name. That callback
has one argument and the annotated scoped row as its arrow's tail. The binder
may occur in the callback argument and result, but not in earlier runner
parameters, the runner result, or the runner's own arrow effects.

Effects listed before the tail are granted to the callback by the runner, which
must handle them:

```fango
{-# scoped s #-}
guarded : String -> (Reader s ->{Fail String | s} a) ->{e} Result String a
guarded text use = Reader.withBytes (Bytes.fromString text) { reader -> Fail.attempt { use reader } }
```

At each call the callback may perform `Fail String` besides its scope's
permission and the residual row; the runner's caller never sees it. Granted
effects' type arguments may not mention the binder. A callback
may therefore return a value that uses a scoped reader or writer later in the
same runner. The runner must consume that value before its scope ends. Within
the implementation the callback can be instantiated at different rows
extending the runner's residual row.

Runners must be called by name with all parameters supplied. Partial application,
first-class aliases, recursion, mutual recursion, class-constrained runner
annotations, and scoped native sidecars are rejected with `SCOPED CALLBACK`.
Ordinary consumers and parsing helpers remain ordinary first-class functions.

A `SCOPE ESCAPE` rejects a local permission in a returned value, a latent
callback, enclosing mutable storage, or residual effects. Wrapping the value in
an ADT does not hide its permission. An older scope's reader can pass through an
inner scope, since only the inner permission is discharged there. This is a
permission check, not a resource close or an implicit IO handler.

## Tail-call guarantee

Recursion is the language's loop, and self tail calls are guaranteed to run
in constant stack in both backends. A recursive call is optimized when all of
the following hold; programs may rely on it, and arbitrarily deep tail
recursion of this shape never overflows:

- the call invokes the *same* named function it appears in, directly and
  with all its arguments; named local functions count, including monomorphic
  ones, because they are hoisted to workers with captured values as leading
  arguments;
- the call is in tail position: the returned expression of the body, of an
  `if` branch, of a `case` branch, or the final expression of a block,
  including through any nesting of those — but not inside a lambda body, not
  under a `handle` expression, and not as an operand or argument of anything
  else;
- the function calls itself at its own type (polymorphic recursion at a
  different instantiation is not optimized);
- no lambda or handler clause on a path to another self tail call captures a
  parameter that the recursion changes; a closure returned from a terminal
  branch is safe because that branch ends the loop. Parameters passed through
  unchanged (such as a callback threaded through a driver loop) are always
  safe to capture.

Mutually recursive functions (`f` calls `g` calls `f`) are *not* optimized and
consume stack proportional to depth. A tail call that never terminates,
such as `f x = f x`, spins instead of eventually overflowing.
