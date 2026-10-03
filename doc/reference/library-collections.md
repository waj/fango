# Collections and result values

List, Range, Maybe, Tuple, Dict, and Result APIs.

[Reference index](../reference.md). Sources: [List](../../stdlib/List.fango), [Range](../../stdlib/Range.fango), [Maybe](../../stdlib/Maybe.fango), [Tuple](../../stdlib/Tuple.fango), [Dict](../../stdlib/Dict.fango), [Result](../../stdlib/Result.fango).

## List

`List` exposes the following algebraic type:

```fango
module List exposing (List(..), range, each, foldl, foldr, map, filter, length, reverse)

type List a = Nil | Cons a (List a) deriving (Eq, Ord)
```

```fango
range : (Num a, Ord a) => a -> a -> List a
each : (a ->{e} ()) -> List a ->{e} ()
foldl : (a -> b ->{e} b) -> b -> List a ->{e} b
foldr : (a -> b ->{e} b) -> b -> List a ->{e} b
map : (a ->{e} b) -> List a ->{e} List b
filter : (a ->{e} Bool) -> List a ->{e} List a
length : List a -> Int
reverse : List a -> List a
```

Its handwritten `Show a => Show (List a)` instance displays lists with bracket
syntax, using each element's `Show` instance: `[]`, `[1]`, and `[1, 2, 3]`.

The prelude imports the `List` type name, so an annotation may say `List a`
with no import. `Nil`, `Cons`, and the functions above still need one.

`range start end` produces ascending values by adding one, including `end`
when that value is reached, and returns `Nil` immediately when `start > end`.
It works at both `Int` and `Float`; callers must use finite bounds because the
ordinary recursive implementation is not guaranteed to terminate for `NaN`
or positive infinity. `each action values` applies `action` from left to right
and propagates its effects. `foldl combine initial values` visits values from
left to right, passing the current element first and the accumulator second to
`combine`; callback effects are propagated. `foldr` passes the same arguments
in the same order but visits values from right to left, so its combining
function receives the result of folding the rest of the list. `map fn values`
applies `fn` to every element, preserving order and length. `filter keep
values` retains the elements for which `keep` answers `True`, preserving their
order. Both call their callback once per element, from left to right.
`length` counts elements and `reverse` returns the same elements in the
opposite order.

Source code builds and matches lists with bracket syntax: `[a, b]` and
`[head | tail]` for [construction](types.md#lists) and for
[patterns](types.md#pattern-matching). `Cons` and `Nil` are the underlying
constructors; spelling them out is valid but discouraged. Construction and
head/tail access are constant time. Tails are
shared; values are immutable. `length` traverses the list. The
[backend design](../design/backend.md#list-representation) explains storage.

`map`, `filter`, and `foldr` use constant stack, with an intermediate list
and reversal.

## Range

`Range.each : (Num a, Ord a) => (a ->{e} ()) -> a -> a ->{e} ()` traverses an
inclusive ascending numeric range without constructing a `List`. For example,
`Range.each drawPoint 0 78` calls `drawPoint` with every value from `0` through
`78`. It does nothing when the start is greater than the end, works with both
`Int` and `Float`, and has the same finite-bound requirement as `List.range`.
The callback runs in ascending order and its effects are propagated.

## Maybe

`Maybe` exposes the optional-value type:

```fango
module Maybe exposing (Maybe(..), withDefault)

type Maybe a = Nothing | Just a deriving (Eq, Ord, Show)
```

`withDefault : a -> Maybe a -> a` returns the contained value or the
fallback. The prelude imports `Maybe(..)`, so the type and both constructors
need no import; `withDefault` does.

## Tuple

`Tuple` exposes the types behind tuple syntax:

```fango
module Tuple exposing (Pair(..), Triple(..), first, second, swap)

type Pair a b = Pair a b deriving (Eq, Ord)

type Triple a b c = Triple a b c deriving (Eq, Ord)
```

`(a, b)` and `(a, b, c)` are surface syntax for these, so a file that uses
tuples needs no import; naming `Tuple`, `Pair`, or `Triple` still does. Both
types have handwritten `Show` instances that display a tuple the way it is
written, `(1, one)`, rather than the derived structural form `Pair 1 one`,
and the type printer spells them `(Int, String)` for the same reason.
Derived `Eq` and `Ord` compare elements left to right, so `Ord` on a tuple
needs `Ord` on every element.

```fango
first : Pair a b -> a
second : Pair a b -> b
swap : Pair a b -> Pair b a
```

Use pattern matching for Triple fields.

## Dict

`Dict` exposes an ordered dictionary keyed by any `Ord` type:

```fango
module Dict exposing
    (Dict, empty, foldl, foldr, fromList, get, insert, isEmpty, keys, map,
     member, remove, singleton, size, toList, update, values)
```

`Dict` is exposed without its constructors, so the type is abstract: the
balance invariant belongs to the module. Its public types are:

```fango
empty : Dict k v
singleton : k -> v -> Dict k v
size : Dict k v -> Int
isEmpty : Dict k v -> Bool
get : Ord k => k -> Dict k v -> Maybe v
member : Ord k => k -> Dict k v -> Bool
insert : Ord k => k -> v -> Dict k v -> Dict k v
remove : Ord k => k -> Dict k v -> Dict k v
update : Ord k => k -> (Maybe v ->{e} Maybe v) -> Dict k v ->{e} Dict k v
keys : Dict k v -> List k
values : Dict k v -> List v
toList : Dict k v -> List (k, v)
fromList : Ord k => List (k, v) -> Dict k v
map : ((k, a) ->{e} b) -> Dict k a ->{e} Dict k b
foldl : ((k, v) -> b ->{e} b) -> b -> Dict k v ->{e} b
foldr : ((k, v) -> b ->{e} b) -> b -> Dict k v ->{e} b
```

Only the operations that navigate by key carry `Ord k`. `empty`, `singleton`,
`size`, `isEmpty`, `map`, and the traversals do not, because none of them
compares a key.

`keys`, `values`, `toList`, `foldl`, and `map` visit in ascending key order
and `foldr` in descending order. Callback effects are performed in that order,
so the order is part of the contract rather than an accident. Callbacks take
one `(k, v)` pair rather than a curried key and value, which also matches
`List.foldl`'s arity. `update`'s callback runs exactly once per call, at the
end of the search path; returning `Nothing` removes a present key and leaves
an absent one absent.

When a key is already present the stored key is kept and only the value
changes. That one rule covers `insert`, `update`, and `fromList` alike, and
it is observable only through an `Ord` instance that calls distinguishable
keys equivalent. `fromList`'s later entries win the value.

`show` displays `Dict [a = 1, b = 2]`, and the empty dictionary as `Dict []`.
Equality compares entries in key order rather than tree shape, so two
dictionaries built by inserting the same entries in different orders are
equal even though their trees differ.

`size` and `isEmpty` are constant time; `get`, `member`, `insert`,
`remove`, and `update` are logarithmic; `keys`, `values`, `toList`, the folds,
and `map` are linear; `fromList` is `n log n`. Nodes are shared rather than
copied, so an update rewrites only its search path.

## Result

`Result` exposes a conventional success-or-error value and basic transforms:

```fango
module Result exposing (Result(..), map, mapError, andThen, withDefault)

type Result error value = Err error | Ok value deriving (Eq, Ord, Show)
```

`map` transforms an `Ok`, `mapError` transforms an `Err`, `andThen` chains a
successful computation, and `withDefault` extracts a success or returns its
fallback.
