# Algebraic data types and matching

Nominal records and unions, list and tuple syntax, deriving, and patterns.

[Reference index](../reference.md).

A nominal record declares a named type and a fixed ordered field schema:

```fango
type Counts = { lines : Int, words : Int, bytes : Int } deriving (Eq, Show)

zero = Counts { words = 0, lines = 0, bytes = 0 }
bump : Counts -> Counts
bump counts = { counts | lines = counts.lines + 1 }
```

Record fields may carry [typed attributes](metaprogramming.md#attributes)
before their names or after their types. Consumers such as
[JSON derivation](library-json.md#typed-values) interpret this metadata; it does
not change the record's Fango field names or types.

A record literal provides every declared field exactly once; source field order
does not affect its type. `value.field` projects a field, and
`{ value | field = expression, ... }` produces a new value of the same nominal
type. Update expressions are evaluated left to right and the original value is
evaluated once. A projection or update receiver must already have a known
nominal record type.

A projected function may be called wherever more effects are allowed than its
field declares: a field typed `() -> Int` is callable in a body performing `IO`
or a user effect, the same widening a function argument gets. The field's
declared row states what calling it performs, not what the body around the call
may do.

## Inferred records

A literal may omit its type name when the expected type already says which
record it is:

```fango
type Point = { x : Int, y : Int }
type Circle = { center : Point, radius : Int }

near = Circle { center = { x = 10, y = 20 }, radius = 8 }

far : Circle
far = { center = { x = 90, y = 90 }, radius = 1 }
```

The type comes from context and from nothing else: an annotation, a parameter
type at the call site, the declared type of an enclosing field, or a branch
unified with a known type. Field labels never choose a record, so adding a
second record type with the same labels anywhere in scope cannot change how an
existing program infers. A literal no context reaches reports `AMBIGUOUS
RECORD`, even when exactly one record in scope has those labels. Once the type
is known the schema is checked as usual, and a label whose schema this module
cannot see still reports `PRIVATE RECORD FIELD`.

A capitalized name immediately before a record-shaped `{ ... }` names the
record being built, so a constructor that takes an inferred record literal
parenthesizes it (`Wrap ({ x = 1 })`); writing `Wrap { x = 1 }` reports
`UNKNOWN RECORD` and says so. Other braced expressions remain arguments:
`Wrap { value }` applies `Wrap` to a Unit callback, and
`Wrap { value | x = 1 }` passes a record update. In expression position,
`{ x = 1 }` remains a record literal, `{ value | x = 1 }` remains an update,
and `{ x = 1; x }` is a lambda body with a local binding.

## Record patterns and visibility

A nominal record pattern names its type and any fields to inspect, and may omit
the type name on the same terms:

```fango
case line of
    IO.Line { text = "", ending = ending } -> ending
    IO.Line { text = text } -> text

startsAt : Point -> Int
startsAt { x = x } = x
```

Fields are keyed and may be reordered. Omitted fields are implicit wildcards,
so `IO.Line {}` is irrefutable. Duplicate and unknown fields are rejected, and
matching requires the field schema exposed by `Type(..)`. An inferred record
pattern delimits itself when it begins a function parameter, as in `startsAt {
x = x }`. After a constructor pattern it needs parentheses: `foo W ({ x = x
})` has a `W` parameter followed by an inferred record parameter, while `foo
W { x = x }` is a record pattern named `W`.

Records participate in module abstraction. An exposing item `Counts` makes
only the type name available, while `Counts(..)` additionally exposes its field
schema for construction, projection, and update. Derived equality
compares fields in declaration order. Derived `Show` has the form
`Counts { lines = 1, words = 2, bytes = 3 }`.

## Union types

A `type` declaration defines a nominal type. Its right-hand side is either a
record schema or one or more constructor alternatives; it is never a type
alias:

```fango
type Status a = Pending | Done a

orPending : a -> Status a -> a
orPending fallback status =
    case status of
        Pending -> fallback
        Done x -> x
```

Constructor arguments are type atoms. Parenthesize applied or function types,
as in `Cons a (List a)` or `Fn (a -> b)`. Constructors are ordinary curried
values and can be partially applied.

## Lists

Lists have construction syntax backed by the bundled `List` type:

```fango
empty = []
numbers = [1, 2, 3]
extended = [0, 1 | numbers]
```

`[a, b]` is `Cons a (Cons b Nil)`, and `[a, b | tail]` is
`Cons a (Cons b tail)`. Elements are evaluated from left to right, followed
by the tail, and the existing tail is shared rather than copied, in constant
time however many lists already share it. All elements have one type and
the tail must be a list of that type. A trailing comma is not accepted, and
`|` requires at least one element on its left and one tail expression on its
right. Bracket syntax selects the bundled constructors directly and needs no
import, and it is the preferred spelling in source: `Cons` and `Nil` are the
underlying constructors, valid but discouraged. There is no infix cons
operator. `List` is also exposed by Prelude; naming `Nil` or `Cons` requires
an import.

## Tuples

Tuples use parentheses and commas in type, expression, and pattern position
alike:

```fango
labelled : (Int, String)
labelled = (1, "one")

keyOf : (k, v) -> k
keyOf pair =
    case pair of
        (key, _) -> key
```

`(a, b)` is `Tuple.Pair a b` and `(a, b, c)` is `Tuple.Triple a b c`, in
every position. Elements are evaluated left to right. A tuple holds two or
three elements; four or more is a `TUPLE TOO BIG` error pointing at nominal
records, whose fields have names. `(e)` with no comma stays an ordinary
grouped expression, type, or pattern, and `()` remains Unit. Like bracket
syntax, tuple syntax selects the bundled types directly and needs no import.

A class context is told apart from a tuple type by its `=>`, so
`(Eq a, Show a) => (a, a) -> String` reads the way it looks.

## Deriving

Equality, ordering, and display are opt-in, either handwritten instances or an
explicit deriving clause:

```fango
type Tree a = Leaf a | Branch (Tree a) (Tree a) deriving (Eq, Ord, Show)
```

`Eq`, `Ord`, and `Show` are derivable out of the box, and any class becomes
derivable through a `deriver` declaration (see
[Compile-time metaprogramming](metaprogramming.md)). Deriving a
class with no deriver is `CANNOT DERIVE`.

A generated instance's context is exactly what its generated methods need: a
field the generator never reads, and a phantom parameter that appears in no
field, add no constraint. Direct recursive fields reuse the instance being
derived. Other cyclic context chains follow the ordinary resolution error
rules. Concrete function fields require suitable blanket evidence; polymorphic
function fields retain their class constraint. Mutually recursive deriving
groups are not supported. A type error inside generated code points at the
line of the generator that produced it, with a note naming the `deriving`
clause that ran it.

Derived equality compares constructors and corresponding fields. Derived
`Show` writes the constructor name followed by each field's
[argument-position representation](classes.md#standard-classes), so nested
constructors and negative numbers are parenthesized and strings quoted:
`Labeled "x" (Circle (-2))`. A record shows as `Name { field = value, … }`,
its field values unparenthesized, and is itself parenthesized as an
argument. Derived ordering compares constructors by
declaration position, then fields left to right; all four of `<`, `>`, `<=`,
and `>=` are generated together.

## Pattern matching

`case` branches may start at different columns as long as each stays deeper
than the lesser of the `case` keyword's column and its enclosing layout
column. This preserves branches to the left of an inline `case`. At a changed
column, a later branch's pattern and `->` must be on the same line. The
formatter aligns branches. Patterns support
constructors, nominal records, integer/float/string/Char literals, variables,
pinned values, `()`, and `_`. Unit has exactly one inhabitant, so `()` is an
exhaustive Unit pattern. `^expected` compares with an existing local, top-level,
imported, or qualified value and requires `Eq`; it does not bind a name. Pins
cannot refer to a binder introduced by the same pattern. Branch bodies may be
inline expressions or blocks. Matches
must be exhaustive and non-redundant, patterns must have the constructor's
exact arity, and a pattern cannot bind the same variable twice.
Integer patterns require both `Num` and `Eq`: the scrutinee is compared with
the pattern's `fromInt` value. Branch order is preserved even when different
integer literals compare equal under a custom instance. Redundancy checking
accounts for collective constructor coverage and identical literal tests;
it does not attempt to prove laws of user-defined equality.
Pins are conservatively refutable, so pinned branches need structural or
catch-all coverage after them. Identical pins can make a later branch
redundant, but different pins are never assumed to cover a type collectively.

List patterns use the same bracket forms:

```fango
case values of
    [] -> "empty"
    [only] -> "singleton"
    [first, second | rest] -> "two or more"
```

A pattern without `|` matches exactly its written length. A tail pattern
matches the remaining list, so `[first | rest]` is the bracket spelling of
`Cons first rest`. List patterns participate in the same exhaustiveness,
redundancy, nesting, pinning, and duplicate-binder checks as constructor
patterns.

## Destructuring bindings

The same patterns may appear on the left of strict local or top-level value
bindings:

```fango
Pair first second = pair
```

Such a binding must bind at least one name and its single row must be
exhaustive. It has no direct annotation syntax; an annotation directly above one
is a `DESTRUCTURING ANNOTATION` error, so annotate a named subject and
destructure that subject on the following declaration. The RHS is checked and
evaluated once, then every bound name becomes visible simultaneously. A
top-level destructuring group is monomorphic and all its names participate in
ordinary collision and export checks. `main` must be a direct declaration and
cannot be introduced inside such a pattern.
