# Compile-time metaprogramming

Meta reflection, quotes, splices, derivers, hygiene, and stage restrictions.

[Reference index](../reference.md).

fango has one compile-time stage. `quote` goes up a stage and `$(…)` comes
back down, and together they are the whole staging surface. `quote` is a
reserved word; `$` is a token only as part of `$(`.

## Type reflection

`typeOf T` produces an opaque `Meta.TypeRepr` for a closed, fully applied
type. Nominal types compare by compiler identity, while applications and
function arrows compare structurally, including the effects on each arrow.
`Meta.sameType`, `Meta.head`, `Meta.args`, `Meta.isVar`, and `Meta.typeName`
inspect this representation; display text never determines identity.

`Meta.Lift` provides `lift : a -> Code` for `Int`, `Float`, `String`, `Char`,
`Bool`, and `()`. The generated literal retains its scalar type. `Meta.fail`
stops expansion and reports `COMPILE-TIME FAILURE` at the splice site.

`Meta.info : TypeRepr -> Reflected` reads a type's schema:

```fango
type Reflected = Opaque | Visible TypeInfo
```

It answers `Visible` only when the type's schema is readable where `typeOf`
was written. That is not a new rule: `exposing (T)` reflects as `Opaque` and
`exposing (T(..))` reflects in full, exactly as those two forms already govern
constructor patterns and record fields. A type variable and a scalar are
`Opaque` too — neither has a schema to read.

```fango
type TypeInfo = { name : String, moduleName : String, ty : TypeRepr, params : Items TypeRepr, shape : Shape }
type Shape = Union (Items Ctor) | Record Ctor
type Ctor = { name : String, symbol : String, index : Int, owner : TypeRepr, fields : Items Field }
type Field = { name : String, index : Int, ty : TypeRepr }
```

A union constructor's fields are positional, so their `name` is empty; a
record's sole constructor carries the type's own name and its fields' names.
Field types come back instantiated at the reflected type's arguments, so a
generator sees `Int` rather than the declaration's parameter.
`Meta.ctorsIn` flattens the two shapes into one constructor list.

`Items` is `Meta`'s own list — `NoItems | Item a (Items a)`, with
`Meta.foldItems`, `Meta.mapItems`, and `Meta.lengthItems`. `Meta` cannot import
`List`, because `List` derives its own instances and so depends on the module
that depends on `Meta`.

## Derivers

A `deriver` declaration opens `deriving` to a class:

```fango
class Tag a
    tag : a -> String

deriver Tag
    tag subject valueCode =
        Meta.match subject valueCode (\bound -> Meta.lift bound.ctor.name)

type Colour = Red | Green Int deriving (Tag)
```

A deriver method's type is dictated by the class: for a class method with *n*
arrows, its deriver method takes a `TypeInfo` plus *n* `Code` arguments and
returns `Code`. A deriver is otherwise an ordinary fango function, checked by
ordinary inference. It must supply exactly the class's methods
(`MISSING METHOD`, `UNKNOWN METHOD`), a class may have only one deriver
(`DUPLICATE DERIVER`), and — like an instance — it is visible by dependency.

The compiler owns the traversal, so a deriver never invents a binder:

- `Meta.match : TypeInfo -> Code -> (Bound -> Code) -> Code` builds the
  exhaustive case over the type's constructors and binds every field, handing
  each branch a `Bound { ctor : Ctor, fields : Items BoundField }` whose
  fields carry `{ name, index, ty, value : Code }`. Nesting two calls produces
  the nested case a two-argument method needs.
- `Meta.construct : Ctor -> Items Code -> Code` goes the other way, for a
  method that produces an `a`. A record constructor produces a record literal.

A `deriver` must precede, in source order, any `deriving` clause that uses it
— including on a type declared earlier in the same file. The bundled `Derive`
module supplies the derivers for `Eq`, `Ord`, and `Show`; a file that writes
`deriving` depends on it automatically, the way a file that writes `quote`
depends on `Meta`.

## Quotes and splices

`quote atom` builds a value of the abstract type `Meta.Code`. It does not
evaluate the quoted expression — it describes it. The quoted text is ordinary
fango and takes exactly one atom, so anything larger is parenthesized:

```fango
import Meta exposing (Code)

answer : Code
answer = quote (6 * 7)
```

`$(expression)` inside a quote is a **hole**: the expression is evaluated
along with the quote, in source order like any other argument, and must
produce `Code`, which is pasted into the quoted text:

```fango
twice : Code -> Code
twice c = quote ($(c) + $(c))
```

`$(expression)` in ordinary program text is a **splice**: the compiler
evaluates the expression while compiling, and the code it produces takes the
splice's place and is checked there:

```fango
main() = print $(twice answer)     -- prints 84
```

Nesting is limited to one level in each direction. A quote inside a quote and
a splice inside a splice are both `STAGE ERROR`.

Quoted code is resolved in the module that wrote it and checked at the site
that splices it. Because names are resolved before inference, generated code
can neither capture nor be captured by names at the splice site.

## Stage visibility

A splice operand may execute only completed dependency groups whose declarations
precede the splice. A direct or transitive dependency on a later declaration
reports `STAGE ERROR`, even if that declaration has already been checked for
another caller. Quoted code can refer to later module functions: it is checked
where it is spliced, and its generated references participate in dependency
checking. **Top-level definitions are available at both stages.** Local
binders are not: a lambda parameter, block
binding, or case binder belongs to the stage it was introduced at, and using
one at the other stage is a `STAGE ERROR`. Staged code that needs a runtime
value takes it as a function argument instead.

## Compile-time restrictions

Compile-time code runs inside the compiler and is restricted accordingly:

- it must type with an empty effect row (`COMPILE-TIME EFFECT`);
- it may not reach a Go sidecar or a bundled native that observes external
  state; deterministic `Random.runSeeded` is safe, while system entropy from
  `Random.runSystem` requires IO and reports `COMPILE-TIME EFFECT`;
- it is bounded by an evaluation-step budget (`COMPILE-TIME LIMIT`).

Together these make generated Go reproducible.

`Code` is a **compile-time-only type**: a definition whose type mentions it is
not emitted, and no expression in an ordinary definition may have such a type.
Both halves report `STAGE ERROR`. In practice a code-producing helper is an
ordinary definition that simply never reaches the executable:

```fango
repeat : Int -> Code -> Code
repeat n c = if n <= 1 then c else twice (repeat (n - 1) c)
```

Quotes, splices, derivers, and `deriving` all work at the prompt. A failed
input leaves nothing behind: a `deriving` clause whose deriver fails does not
install its type, and a declaration whose splice fails does not install its
name.

Only expressions can currently be quoted and spliced. Generating a declaration
group, including a type declaration, remains a future milestone described in
the roadmap.
