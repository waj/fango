# Modules and imports

Module identity, exports, Prelude, and source discovery.

[Reference index](../reference.md).

A program may consist of local modules. A named file begins with a module
header, followed by all imports, followed by declarations:

```fango
module Geometry.Shape exposing (Shape(..), area)

import Geometry.Point
import Geometry.Point as Point
import Geometry.Point exposing (Point, origin)
import Geometry.Point as P exposing (Point, origin)
```

Every import permits qualified access through the full module name and, when
present, its single-capitalized-name alias. An import `exposing` list also
introduces selected names unqualified; it does not remove qualified access.
Imports cannot be interspersed with declarations, and duplicate module imports
or qualifier aliases are rejected.

## Exports and visibility

An exposing list is `(..)`, empty `()`, or a comma-separated list. A lowercase
item exports/imports a value or one effect operation. `Type`
or `Effect` exposes the abstract type/effect label; `Type(..)` also exposes all
constructors and `Effect(..)` all operations. `Class` exposes a class name;
`Class(..)` also exposes all its methods. Individual methods can be exposed as
lowercase values. Declaring an instance requires access to all class methods
(qualified access counts). Constructors cannot be selected
individually, and member lists cannot be partial. Qualified names are accepted
for values, operations, constructors, patterns, types, effect rows, and handler
clauses.

An explicit exposing list may also name what the module's own imports expose
unqualified, re-exporting it under the importing module's qualifier:

```fango
module Shapes exposing (Shape(..), area, describe)

import Shapes.Core exposing (Shape(..), area)
```

A re-exported name keeps its identity, so `Shapes.area` and
`Shapes.Core.area` are the same declaration, with the same instances and
diagnostics. `T(..)` re-exports members only when the import exposed
`T(..)` (`NON-PUBLIC EXPORT` otherwise). A name reachable only qualified, or
only through the prelude, is not re-exportable (`UNKNOWN EXPORT`), and
`exposing (..)` exports only the module's own declarations.

## Source roots and module identity

The entry file's directory is the source root. A non-bundled `Foo.Bar` resolves
exactly to `Foo/Bar.fango` beneath it. Imported files require a header whose
module name and casing match that path. A named entry must match its top-level
filename, so `Main.fango` declares `Main`. Headerless entry files remain
compatible, receive a private synthetic identity, and cannot themselves be
imported.

Source paths are case-sensitive, including directory names. A differently
cased local path produces a casing diagnostic when the complete module file
exists; an unrelated directory with similar casing does not occupy a module
name.

Bundled modules use the same dotted-name path convention beneath `stdlib/`.
For example, `Runtime.Local` is stored at `stdlib/Runtime/Local.fango`.
A native sidecar for a dotted module follows the same nested path and uses
the `.native.go` suffix beside its source.

## Prelude and implicit dependencies

Fango also ships standard-library modules, in the
[library root](commands.md#the-library-root) beside the compiler.
Their names are reserved: a named entry or local module that has the same name
is rejected with `RESERVED MODULE`, rather than replacing the bundled module.
One of them, `Prelude`, declares the default scope. It holds nothing but
imports, and every other module resolves as though they stood at the top of
its own file:

```fango
import Basics exposing
    ( Num, Eq, Ord, Show, show
    , (+), (-), (*), (/), (==), (/=), (<), (>), (<=), (>=), (++)
    )
import Console exposing (print, readLine, write)
import IO exposing (IO, stderr, stdin, stdout)
import List exposing (List)
import Maybe exposing (Maybe(..))
```

These are ordinary imports, so besides the unqualified names they also grant
qualified access: `IO.write` and `Basics.modBy` need no import line of their
own. Everything the exposing lists leave out does — the named methods
`fromInt` and `negate` among them, and every other standard-library module.
Importing a module the prelude already names is not a duplicate import; it
simply adds the names its own exposing list selects. Aliasing another module
to a qualifier the prelude holds is a `DUPLICATE IMPORT ALIAS`, so `import
Helper as IO` is rejected.

Because `Maybe`, `Nothing`, `Just` and `List` are in scope everywhere, a
module cannot declare its own: doing so is an `UNQUALIFIED COLLISION`, since
Fango rejects shadowing rather than resolving it. Pick another name, or opt
out with the pragma below.

A module opts out with the `{-# no-prelude #-}` pragma above its header,
after which the only names in scope are its own declarations and whatever its
own imports bring in:

```fango
{-# no-prelude #-}
module Bare exposing (double)

import Basics exposing ((+))

double x = x + x
```

A pragma is `{-#`, a directive name, and `#-}`. The file-header directive
`no-prelude` belongs above the `module` header; a later one is a `MISPLACED
PRAGMA`, and an unrecognized directive is an `UNKNOWN PRAGMA`. The separate
[`resource` pragma](resources.md#resource-types) belongs to a type declaration.
The bundled standard library sits below the prelude
and carries the pragma, which is why its modules import `Basics` explicitly.

Bracket list syntax, tuple syntax, `deriving`, and the staging forms are
implicit in a different way. A module that uses one automatically depends on
the bundled module the syntax desugars into — `List`, `Tuple`, `Derive`, or
`Meta` — but the dependency exposes nothing. `Nil`, `Cons`, `Pair`, `Triple`
and `Code` still follow the ordinary import rules; `List` and `Maybe` are in
scope because the prelude imports them, not because the syntax does.

## Entry modules and build manifests

`build` and `run` use only the entry module's `main`; a dependency's `main` is
an ordinary declaration. `check` does not require `main`. Imports expose only
the direct module's declared public interface, never its dependencies. Import
cycles are rejected with the complete cycle chain. The generated build
directory compiles each Fango module as a separate Go package within one
private Go module, allowing unchanged packages to use Go's build cache. It
belongs to the directory the entry file is in rather than to one program, so
every program there is a package main of its own and they share the modules
they both import. Each one has a `sources.json` beside its entry package,
containing each transitive Fango source and native sidecar's logical name,
path, and SHA-256 hash. Local paths are relative to the source root; bundled
paths begin with `<stdlib>/`. It records what that build was made from; what
decides whether a program is recompiled or relinked is the compilation cache
and the generated Go itself, module at a time, on keys that cover bundled and
local sources alike.

## Current naming limitation

An entry-file effect operation can clash with a parameter or pattern name in a
bundled module and report `SHADOWING` there (for example, an operation named
`value`). Effect operations are registered before module values are checked.
Choose a different operation name to avoid this limitation.
