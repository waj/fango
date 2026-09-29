# Compile-time metaprogramming

Meta reflection, quotes, splices, derivers, hygiene, and stage restrictions.

[Reference index](../reference.md).

Fango has one compile-time stage. A backtick-delimited quotation goes up a
stage and `$(…)` comes back down. Attributes also evaluate expressions at that
compile-time stage. `quote` is an ordinary identifier; `$` is a token only as
part of `$(`.

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
type TypeInfo = { name : String, moduleName : String, ty : TypeRepr, params : Items TypeRepr, shape : Shape, attributes : Attributes }
type Shape = Union (Items Ctor) | Record Ctor
type Ctor = { name : String, symbol : String, index : Int, owner : TypeRepr, fields : Items Field, attributes : Attributes }
type Field = { name : String, index : Int, ty : TypeRepr, attributes : Attributes }
```

A union constructor's fields are positional, so their `name` is empty; a
record's sole constructor carries the type's own name and its fields' names.
Field types come back instantiated at the reflected type's arguments, so a
generator sees `Int` rather than the declaration's parameter.
Types, constructors, and fields expose their generic attribute collections.
The synthetic record constructor has no attributes of its own; attributes
before `type` belong to `TypeInfo`. Attributes on a parent do not propagate to
its children.
`Meta.ctorsIn` flattens the two shapes into one constructor list.

`Items` is `Meta`'s own list — `NoItems | Item a (Items a)`, with
`Meta.foldItems`, `Meta.mapItems`, and `Meta.lengthItems`. `Meta` cannot import
`List`, because `List` derives its own instances and so depends on the module
that depends on `Meta`.

## Attributes

An attribute is an ordinary Fango expression inside `#[...]`. Libraries define
attribute types with ordinary declarations; no registration is required:

```fango
type Label = Label String

#[Label "configuration"]
type Config =
    { count : Int  #[Label "wire", Json.Default `0`]
    }
```

Each tag contains one or more comma-separated expressions, with an optional
trailing comma. Empty tags and missing items are rejected. Repeated tags and a
single grouped tag have identical semantics: expressions are evaluated left to
right, and the resulting attributes retain source order. Nested commas belong
to their list, tuple, or record expressions. Formatting preserves tag grouping.

Tags attach before `type`, before a union constructor name, or before a
positional constructor field's type atom. Record-field tags may appear before
the field name or after its complete type, before the comma or closing brace.
A trailing tag on a separate line still belongs to that field; the comma starts
the next field. Both placements may occur on one field, with leading tags
followed by trailing tags in the attribute collection. Formatting preserves
placement; see the [layout rules](commands.md#formatting).
Tags do not attach to value declarations or expressions. Qualified names and
aliases obey ordinary import rules; attributes do not make libraries implicit
imports.

Every expression is checked and evaluated when its declaration is checked,
before deriving, even if nothing reads the attributes. The existing purity,
source-order, safe-native, and step-budget restrictions apply. Types must be
closed and fully applied, with ordinary numeric defaulting. Attributes on a
generic declaration are shared by its instantiations; attribute payloads cannot
depend on the declaration's type parameters.

Payloads may contain scalars, records, unions, Lists, `Code`, and `TypeRepr`.
Function, resource, and native-handle payload types are rejected (`ATTRIBUTE
TYPE`); unsupported stored representations report `ATTRIBUTE VALUE`. Pure
helpers may compute attribute data. Quotes store code without executing their
bodies, retaining the declaring module's resolution and hygiene; generated code
is checked when spliced.

`Attributes` and `Site` are opaque compile-time-only types:

```fango
type Attached a = { value : a, site : Site }

attributes : Type a -> Attributes -> Items (Attached a)
failAt : Site -> String -> a
```

`Meta.attributes @Label field.attributes` retrieves exact matches by nominal
type identity and type arguments, in source order. No matches return `NoItems`.
The requested type must be closed at elaboration (`ATTRIBUTE TYPE`); concrete
partial applications work, and generic helpers can accept a specialized lookup
function. Attribute collections inherit reflection visibility: an abstract
import does not expose its hidden schema or metadata. Reading metadata before
its declaration completes fails at compile time.

The compiler permits repeated attributes. Their consumers decide whether
particular combinations and attachment sites are valid. `Meta.failAt` reports
`COMPILE-TIME FAILURE` at the individual expression's attachment site, including
when several expressions share a tag. JSON's rules are in the
[JSON reference](library-json.md#typed-values). Unconsumed, well-typed options do
not receive consumer-specific validation.

Metadata and source handles cannot reach runtime code. Attaching metadata to a
type does not make that type compile-time-only. Attributes work in the REPL;
failed attachment evaluation or derivation rolls back the declaration.

## Derivers

A `deriver` declaration opens `deriving` to a class:

```fango
class Tag a
    tag : a -> String

deriver Tag
    tag subject valueCode =
        Meta.match subject valueCode { bound -> Meta.lift bound.ctor.name }

type Colour = Red | Green Int deriving (Tag)
```

Later deriver methods may outdent from the first while staying indented under
`deriver`; the formatter aligns them.

A deriver method's type is dictated by the class: for a class method with *n*
arrows, its deriver method takes a `TypeInfo` plus *n* `Code` arguments and
returns `Code`. A deriver is otherwise an ordinary Fango function, checked by
ordinary inference. It must supply exactly the class's methods
(`MISSING METHOD`, `UNKNOWN METHOD`), a class may have only one deriver
(`DUPLICATE DERIVER`), and — like an instance — it is visible by dependency.

The compiler owns the traversal, so a deriver never invents a binder:

- `Meta.match : TypeInfo -> Code -> (Bound -> Code) -> Code` builds the
  exhaustive case over the type's constructors and binds every field, handing
  each branch a `Bound { ctor : Ctor, fields : Items BoundField }` whose
  fields carry `{ name, index, ty, value : Code, attributes }`. Nesting two calls produces
  the nested case a two-argument method needs.
- `Meta.construct : Ctor -> Items Code -> Code` goes the other way, for a
  method that produces an `a`. A record constructor produces a record literal.
- `Meta.lambda : String -> (Code -> Code) -> Code` builds a lambda and supplies
  its binder as `Code` to the callback. The caller must choose a private name.

A `deriver` must precede, in source order, any `deriving` clause that uses it
— including on a type declared earlier in the same file. The bundled `Derive`
module supplies the derivers for `Eq`, `Ord`, and `Show`; a file that writes
`deriving` depends on it automatically, the way a file that writes a quotation
depends on `Meta`.

## Quotes and splices

A quotation written as `` `expression` `` builds a value of the abstract type
`Meta.Code`. It does not evaluate the quoted expression — it describes it.
The contents are one ordinary Fango expression; the backticks supply grouping
and a fresh layout boundary like parentheses. Quotations are atoms, including
when passed as function arguments:

```fango
import Meta exposing (Code)

answer : Code
answer = `6 * 7`
```

`$(expression)` inside a quote is a **hole**: the expression is evaluated
along with the quote, in source order like any other argument, and must
produce `Code`, which is pasted into the quoted text:

```fango
twice : Code -> Code
twice c = `$(c) + $(c)`
```

`$(expression)` in ordinary program text is a **splice**: the compiler
evaluates the expression while compiling, and the code it produces takes the
splice's place and is checked there:

```fango
main() = print $(twice answer)     -- prints 84
```

Quotations may span lines and contain conditionals, cases, lambdas, strings,
and comments. Backticks inside strings, character literals, or comments do not
close a quotation. An empty quotation is a `SYNTAX PROBLEM`; an unclosed
quotation is an `UNFINISHED PROGRAM` and the REPL waits for more input.

The contents must be an expression, not a standalone binding or statement
block. To describe a computation with local bindings, quote an immediately
invoked lambda:

```fango
withBinding = `{
    x = 6
    x * 7
}()`
```

Nesting is limited to one level in each direction. A quotation nested in
quoted code and a splice inside a splice are both `STAGE ERROR`. A hole
steps out of the quoted stage, so its operand may construct a quotation:

```fango
withHole = `$(`6`) * 7`
```

No escape syntax is added to quoted code. Parentheses, braces, brackets, and
splice operands retain their own delimiter boundaries; a backtick inside one
of these opens a quotation, while a backtick at the current quotation's
boundary closes it.

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
