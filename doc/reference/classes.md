# Type classes and instances

Class declarations, dictionary constraints, instance selection, and defaulting.

[Reference index](../reference.md).

A class has exactly one type parameter and an indented block of method
signatures. An instance supplies every method exactly once:

```fango
class Label a
    label : a -> String

type Item = Item String

instance Label Item
    label item = case item of
        Item text -> text

describe : Label a => a -> String
describe value = label value
```

Each method must be a function mentioning the class parameter. Additional
method type variables, open effect rows, superclasses, higher kinds, and
default methods are unsupported. Closed effect rows are allowed: a method
`read : a ->{Ask} Int` performs `Ask` when applied. Constructing a method
value must be pure, including implementations written as `method = expression`;
IO during construction is rejected with `UNHANDLED EFFECT`.

## Constraints

Qualified annotations put constraints before `=>`; multiple constraints use
parentheses, as in `(Num a, Ord a) => a -> a`. Inference retains required
constraints on generalized functions and values. An annotation omitting one
reports `MISSING CONSTRAINT`. Explicit constraints provide evidence to the
body, including structural constraints such as `Show (Box a)`; method calls
use that evidence. A constraint variable absent from the annotated type is
ambiguous. Local syntactic functions can generalize constraints; ordinary
local values remain monomorphic.

## Instance heads and blanket instances

Instance heads are either a bare type variable (a blanket instance) or a fully
applied named type. Direct function heads are unsupported, but a blanket can
match a function type. Applied heads are parenthesized. Their arguments can be
variables, repeated variables, concrete types, or nested applications.
Conditional instances may require structural constraints; every context
variable must occur in the head, and open effect rows are rejected:

```fango
type Box a = Box a

instance Show a => Show (Box a)
    show box = case box of
        Box value -> "Box " ++ show value
```

A blanket instance is an overridable default: it is resolved wherever the
concrete type is known, so a more specific instance applies even to a call
whose own type is still a variable. An instance for a constructed type is
composed instead: its evidence is assembled from its arguments' evidence, the
way the `Box` instance above delegates to `show value`. One type constructor
therefore has one head per class, and a second head specializing its arguments
reports `OVERLAPPING INSTANCE`:

```fango
instance Show (Box Int)      -- OVERLAPPING INSTANCE
    show box = "integer box"
```

Composition happens wherever the arguments are not yet known, and cannot
consult such a head, so the two would disagree depending on the call site.
Overriding the element's own instance works from anywhere, because that is the
part composition delegates to.

Blanket instances provide implementations for types satisfying their context.
For example, an application can use ordinary display as its logging default
and specialize particular domain types:

```fango
class LogValue a
    logValue : a -> String

instance Show a => LogValue a
    logValue x = show x

type Credentials = Credentials String String deriving (Show)

instance LogValue Credentials
    logValue credentials = case credentials of
        Credentials username _ -> username ++ " [password omitted]"

log : LogValue a => a ->{IO} ()
log x = print (logValue x)

main() =
    log 42
    log (Credentials "alice" "secret")
```

This prints `42` and `alice [password omitted]`. An unannotated
`forward x = logValue x` infers `LogValue a => a -> String`, so its caller
supplies the specialized dictionary. Annotating it with only `Show a` instead
reports `MISSING CONSTRAINT`: the blanket does not imply `LogValue a` inside
a polymorphic body. The same rule applies to structured types:
`render x = show (Box x)` infers `Show (Box a) => a -> String`.

## Instance selection

Matching first selects the most specific visible head, which orders a blanket
against a constructor head. Equivalent heads may have different contexts.
Duplicate head/context pairs, incomparable overlapping heads, and one
constructor head specializing another's arguments all report
`OVERLAPPING INSTANCE`; renaming variables, reordering constraints, or
repeating a constraint does not make a distinct instance.

Within the selected head group, only candidates whose contexts are satisfied
are applicable. A strict superset of constraints takes precedence over its
subset. This compares predicate sets, not logical implications through other
instances. Among the remaining candidates, the latest declaration in each
module wins. If candidates from multiple modules remain, the use reports
`AMBIGUOUS INSTANCE` with their declaration locations and contexts.

For example, a conditional blanket overrides an unconditional fallback when
its context is available, regardless of their declaration order:

```fango
class Inspect a
    inspect : a -> String

instance Inspect a
    inspect _ = "<inspect not implemented>"

instance Show a => Inspect a
    inspect x = show x

instance Inspect String
    inspect x = "\"" ++ x ++ "\""

type Foo = Foo

main() =
    print (inspect 42)
    print (inspect "Hola")
    print (inspect Foo)
```

This prints `42`, `"Hola"`, and `<inspect not implemented>`. With incomparable
contexts such as `Show a` and `Eq a`, the later declaration wins when both
apply in the same module. A `(Show a, Eq a)` context beats either one when
applicable.

An unresolved
type is never guessed from the set of instances. Predicates containing any
unresolved or quantified variable retain their evidence requirement, even
when only one instance currently matches. Concrete predicates resolve through
instances; explicitly passed evidence takes precedence. If no context in the
most-specific head group is satisfied, resolution does not fall back to a
less-specific head. Cycles, nesting-limit failures, and ambiguity encountered
while checking a context are errors, not reasons to try a fallback.

## Resolution termination

Structured contexts need not be smaller than their heads. For example,
`Show (Box a) => Show (Wrapper a)` can delegate to a wrapper's `Box a` field.
A circular requirement encountered at a concrete use reports
`INSTANCE RESOLUTION` with its cycle; a growing chain reports the nesting
limit instead. Declarations with such structural cycles are allowed, but
nothing breaks one: a head specializing another's arguments is rejected, so
every use of such a cycle fails. Blanket contexts must constrain only their
head variable, and cycles between their class requirements are rejected at
declaration time with `INSTANCE CONTEXT`.

Inside an instance method, its own head is available as self evidence using
the instance's declared context. This permits direct recursive implementations
without requiring callers to supply a circular self constraint.

## Instance visibility

Classes, instances, and derivers retain source-position visibility even when
function dependencies are checked in a different order.
Later instances do not change earlier concrete calls; a polymorphic function
still uses the evidence supplied by its caller.
Instances may live outside both the class's and the type's defining module
(orphan instances). Resolution sees the defining module and its transitive
imports; exposing lists do not hide instances. Overlap checking covers the
entire loaded module graph. A missing concrete implementation reports
`MISSING INSTANCE`.

## Standard classes

The standard classes are independent (in particular, `Ord` does not imply
`Eq`):

| Class | Methods | Standard instances |
| --- | --- | --- |
| `Num a` | `fromInt : Int -> a`, `(+)`, `(-)`, `(*) : a -> a -> a`, `negate : a -> a` | `Int`, `Float` |
| `Eq a` | `(==) : a -> a -> Bool` | `Int`, `Float`, `String`, `Char`, `Bool`, `()` |
| `Ord a` | `(<)`, `(>)`, `(<=)`, `(>=) : a -> a -> Bool` | `Int`, `Float`, `String`, `Char` |
| `Show a` | `show : a -> String` | `Int`, `Float`, `String`, `Char`, `Bool`, `()` |

Their operator-named methods are in the prelude, as is `show`. The named ones —
`fromInt` and `negate` — are not, so using them unqualified takes a `Basics`
import. `print` is an ordinary
Show-constrained function that writes `show value` followed by a newline.
Strings display raw, not quoted.

## Defaulting

When evaluation requires a concrete type, an unresolved variable defaults to
`Int` only if its defaulting requirements include standard `Num` and no classes
outside standard `Num`, `Eq`, `Ord`, and `Show`. Eligibility may expand general
instance contexts: `Num a, LogValue a` qualifies through `Num a, Show a` in the
example above. This does not choose evidence; the original constraints are
resolved after defaulting, so concrete specializations still win. A custom
bare constraint without an eligible blanket prevents defaulting.
Other unresolved constraints, including undetermined phantom types, report
`AMBIGUOUS CONSTRAINT`. Decimal literals do not default. An unconstrained
runtime type variable defaults to Unit.
