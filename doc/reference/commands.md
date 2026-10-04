# Setup and commands

Compiler setup, command behavior, formatting, generated projects, and main.

[Reference index](../reference.md).

The repository uses Go 1.26 and provides a Nix development environment with
Python 3 for development helpers:

```sh
nix develop
go build -o fango ./cmd/fango
```

The flake also packages the compiler. `nix run github:waj/fango -- run
main.fango` runs it without a checkout, and `nix flake init -t
github:waj/fango` starts a project whose development shell provides `fango`.
The package installs the [library root](#the-library-root) layout and wraps the
executable so the Go it was built with comes first on `PATH`, since builds and
the interpreter's native worker run `go build`.

Repository test, vet, golden-update, and manual benchmark commands are in
[verification](../design/verification.md#verification-commands). GitHub Actions
runs `make ci` on pushes to master and on pull requests.

`make clean` removes the repository-local `fango` executable, every `.fango/`
build directory beneath the checkout (including those created under examples
and test fixtures), and the Go test cache. It does not remove exported `.out`
projects or other ignored application data.

`build`, `run`, `check`, and `clean` accept one `.fango` source file:

```text
fango build [-o out] [--emit-go] [verbosity] main.fango
fango run [verbosity] main.fango [--] [args...]
fango check [verbosity] main.fango
fango fmt [-w] [-l] [file...]
fango repl [dir]
fango clean main.fango
fango doc --stdlib [--module name]... [--strict]
```

Flags precede the source path, because everything after it belongs to the
program `run` is about to start.

`build` writes a native executable (defaulting to the source basename without
`.fango`). `run` builds if needed and runs the cached executable, forwarding
every argument after the source path to the program; an optional `--` is
removed first. `check` runs
through parsing, inference, elaboration, and Core validation without generating
Go. `clean` removes the persistent `.fango/` build and compilation cache
artifacts of the directory the source file is in — every program there, not
only that one — along with that directory's fallback compilation cache and the
legacy per-entry fallback namespace when present.
`repl` starts an interactive session whose source root is `dir`, or the
working directory; see [REPL](repl.md). `doc` writes the
[API reference](#api-documentation) of the bundled library as JSON.

Compilation results are cached per module under `.fango/cache/v1/` beside the
entry file. Each module keeps one checked result there, and one record of what
its generated Go was made from and what it came out as; the generated Go
itself is not copied there, because the build directory beside it already
holds exactly one of each, which is what the Go toolchain compiles. Editing a
file in the build directory therefore costs that module its reuse and it is
generated again, rather than being silently overwritten with something else.
Recompiling replaces a module's entries, so the cache grows with the modules a
project has rather than with its edit history — and restoring a file to an
earlier state recompiles it rather than finding what that state compiled to
before. Entry programs in one directory share the modules they have in common
and keep their own entry results, so alternating between them stays warm. The
build directory works the same way: each program is compiled and linked as a
package of its own, so building one leaves the others' generated packages and
executables alone.

Every command discovers, parses, and validates the current source
graph, and then reuses what is still valid: a checked module whose sources,
sidecar, operator table, and dependency contracts are unchanged, and generated
Go for a module whose own implementation and every contract it can reach
through its dependencies are unchanged — a module elsewhere in the program that
it cannot reach does not affect it. So editing a function body recompiles its own module and relinks
the program, while modules that only call it keep their generated code; changing
an exported type, instance, or calling convention recompiles the modules that
depend on it; and editing a comment recompiles only the module it is in, while
diagnostics elsewhere still point at current source positions. A module whose
implementation runs at compile time — through a splice or a deriver — also
recompiles the modules that execute it, transitively, even when nothing about
its type changed. `check` produces the same checked modules a later `build`
reuses, so checking first costs a build only its back end.

When the source-local cache cannot be written, artifacts use a shared
user-cache namespace for the absolute source root. The cache is transparent:
corrupt, incompatible, or unwritable entries are ignored and rebuilt, and no
failure to read or write one is ever a diagnostic. `-no-cache` compiles every
module from source, ignoring valid artifacts and publishing none, so a cold
build can be compared with a warm one without discarding either; the
[verbosity flags](#build-progress-and-statistics) report what was reused.
Distinct compiler executables use distinct
namespaces, so an upgraded compiler starts cold and never reads what an older
one wrote. `FANGO_BUILD_DIR` redirects generated build output, not this cache.
Parsing is never cached: every command reparses the whole source graph, and
`fmt` parses its requested input like any other.

## The library root

The standard library, Go runtime, and interpreter-worker support are files the
compiler reads, not part of the executable, so editing one takes effect on the
next build. They live in a single root holding `stdlib/`, `runtime/`, and
`internal/`, found in this order:

1. `FANGO_ROOT`, when set.
2. `lib/fango` beside the compiler, as `../lib/fango` then `lib/fango` relative
   to the executable's own directory.
3. The enclosing `github.com/waj/fango` checkout, walking up from the working
   directory. This is what makes a freshly built compiler work in its own
   source tree with nothing configured.

A directory qualifies only if `stdlib/Prelude.fango` is readable beneath it. A
`FANGO_ROOT` that does not qualify is reported rather than skipped, so a typo
cannot quietly select a different library. When no root is found at all, every
command that needs one fails with `MISSING LIBRARY` naming where it looked.
`FANGO_ROOT` names the library; `FANGO_BUILD_DIR` redirects generated build
output; neither selects the compilation cache.

`make install PREFIX=<prefix>` produces this layout, and `make install-lib`
installs only the library for packagers that build the executable themselves:

```text
<prefix>/bin/fango
<prefix>/lib/fango/stdlib/
<prefix>/lib/fango/stdlib/Runtime/
<prefix>/lib/fango/runtime/
<prefix>/lib/fango/internal/
```

A compiled program is self-contained and needs no root: the runtime support it
uses is copied into its generated project at build time.

The library is versioned with the compiler and expected to match it. Editing a
bundled `.fango` module is supported and invalidates exactly what depends on
it. Two mismatches are reported rather than miscompiled: a bundled native
declaration that disagrees with the compiler's registry (`INVALID BUNDLED
NATIVE`) and a `List` that no longer has the shape code generation projects
against (`INVALID BUNDLED LIST`). One is not: editing a bundled `.native.go`
sidecar changes compiled programs and the interpreter's native worker, but not
the compile-time evaluator, which uses the copy linked into the compiler. That
requires rebuilding the compiler.

## Build progress and statistics

`build`, `run`, and `check` are silent on success by default. `-v` reports each
module as it is worked on, naming the stage as the verb and marking the ones
served from cache. A line is printed when its work starts, so a build that
pauses pauses beneath the line naming what it is doing; reuse is reported on
completion instead, because a cache hit is the whole of that module's work and
there is no pause to attribute:

```text
   Parsing  10 modules
  Checking  List (from cache)
  Checking  Markdown
  Emitting  List (from cache)
  Emitting  Markdown
   Linking  go build
  Finished  markdown — 10 modules (9 cached, 1 compiled), 1.4 MB reused, 275ms
```

Discovery is one line, because nothing in it is cacheable: every command
reparses and revalidates the whole graph, so the count is all there is to
report. `Checking` and `Emitting` name one module each, in dependency order; an
entry file that declares no module header is named `<entry>`. A build whose
program compiles the same Go as the binary beside it relinks nothing and prints
no `Linking` line — including when a source edit changed nothing the Go
depends on, such as a comment. `check` stops after `Checking`.

`-vv` names the work running under each `Checking` line — elaborating, building
stage Core, linting Core — because most of a slow module's cost falls after its
check, and the module line alone cannot say which part a pause belongs to. It
then adds a table of where the time went and what the cache moved. Stages that
belong to no module — writing the generated project, and the Go toolchain — are
timed by the command itself, and time that belongs to no stage is reported as
`other`, so the rows always reconcile with the total. Work the compiler runs
concurrently — emitting several modules at once, reading deferred stage Core,
storing checked artifacts while later modules are checked — counts the time the
build spent on it, divided among its modules in proportion to each one's work,
so overlapping work is not counted twice. The rows reconcile as measured, that is:
each duration is rounded to the unit it is shown in, and a build of a second or
more is shown in seconds, so adding up a printed column lands near the printed
total rather than on it. Modules that arrive from
cache defer their stage Core until some module is checked from source; the
`stage Core` row and the count beneath the cache block are what reading it
cost. Each reuse count is over every module that stage covered, so a module
whose generated code cannot be cached counts against reuse rather than
disappearing from the ratio.

`-timings json` writes these stage and cache measurements as one JSON object
for recording build cost over time. `-no-cache` ignores and publishes no
artifacts, so a cold build can be measured against a warm one in place.

All of this goes to standard error. A program started by `run` still owns
standard output entirely, so its output is byte-for-byte what it wrote whatever
verbosity the build ran at.

## Formatting

`fmt` formats source. With no paths, or with `-`, it reads standard input and
writes to standard output; with paths it writes each formatted file to standard
output, `-w` rewrites the files in place, and `-l` lists the files that would
change and exits 1, which is how the repository gates its own sources. It
normalizes spacing, indentation, the `(op)` spelling of an operator name, and
runs of blank lines. A declaration is preserved exactly as written when it
holds a comment the formatter cannot anchor — one between an operator and its
operand, say, rather than above a statement or a branch — or when some part of
it has a line structure the printer cannot reproduce.

Redundant parentheses are dropped, because the syntax tree does not record
them. Grouping is not: an operator run is printed flat in the order it was
written, never regrouped, and a run that was parenthesized keeps its
parentheses. Literal spelling is preserved exactly — `1.50` and `1e3` are not
rewritten — as is the difference between `f()` and `f ()`.
Postfix unit calls bind tightly enough to appear directly as application
arguments, so `foo bar()` stays `foo bar()`; other nested applications retain
parentheses when removing them would change how the arguments are read.

The formatter keeps the author's line breaks rather than reflowing to a width,
except when placing a multiline closing parenthesis, lambda brace, or quotation
backtick, or when making a lambda consistently inline or multiline, or expanding
a record schema with attributed fields. A
construct written across several lines stays that way and one written inline
stays inline. That extends to where a keyword sits: a body moved below
its `=` or `->` stays below it, a `case` written on its declaration's own line
keeps its branches one level in from there, and a `then` or `else` given a line
of its own is anchored at the column of its `if`, so a chain of arms lines up
instead of staircasing rightward. An `exposing` list the author moved below
its keyword is printed in the leading-comma block form.
In a multiline application, a parenthesized argument or braced lambda closes
on a new line. Consecutive closing parentheses and lambda braces share a line
when their openers share a line; otherwise each closes on a separate line. A
later argument may follow the closings on that line, as in `)) options`.
Closing lines use the indentation of their opening lines. A braced lambda
body is indented below its enclosing statement, regardless of the source
column the parser accepted. When a lambda is a direct list item, tuple item,
or record field value, its closing brace aligns with the item or field name
after any leading comma and space. The container's own closing delimiter
retains its alignment.
A lambda whose body starts and ends on the opening line also closes there,
even when its source closing brace was on the next line. When the body spans
lines, it starts below `->`, or below `{` for a Unit lambda.

A multiline nominal type is the exception: its `deriving` clause is always an
indented line after its constructor alternatives or record schema. An inline
type may instead keep `deriving` on the declaration line or on a following
indented line, matching the source.

A record schema containing attributes is multiline. The formatter preserves
whether tags precede or follow a field. Leading tags each occupy a separate
line: the leading `{` or `,` introduces the first tag, and subsequent tags and
the field declaration align two columns farther right.

The first inline trailing tag aligns across fields that have one, leaving two
spaces after the longest participating field declaration. Unattributed fields
and fields whose trailing tags begin on separate lines do not affect that
column. Field names, colons, and types keep their ordinary spacing:

```fango
type Config =
    { name : String    #[Json.Key "full_name"]
    , count : Int      #[Json.Default `7`]
    , secret : String  #[Json.Skip, Json.Default `"local"`]
    }
    deriving (Encode, Decode)
```

Repeated inline tags have one space between them. Trailing tags starting on
separate lines are indented one level below the field name. Tag grouping and
line breaks are preserved, with multiline contents indented relative to their
tag's starting column. Tags are not automatically wrapped to a line width.

A bracket list or tuple written across lines uses that same leading-separator
style. Its separators and closing delimiter align with its opening delimiter;
elements remain grouped on the source lines the author chose. Nested lists and
tuples align independently, in expressions and patterns:

```fango
values =
    [ first, second
    , third
    ]

pair =
    ( first
    , second
    )
```

Import lines are sorted by module name and exposed names are sorted by kind —
types, effects and constructors first, then values, then operators — and
alphabetically within each kind. A comment written directly above an import
moves with it; one set off by a blank line stays at the top of the block. A
broken `exposing` list starts a line per kind and wraps to stay readable, which
is the one place the formatter consults a width: sorting has already discarded
the author's line structure there, so there is no break left to preserve. A
list written inline is left inline however long it is.
A comment above a statement, a branch, a handler clause or a declaration body
keeps its place, and one written at the end of a line stays at the end of that
line.

A file that does not lex or parse is left untouched and its diagnostics are
reported, because a failed declaration is dropped during recovery and formatting
would lose it.

The VS Code extension in `editors/vscode/` registers `fmt` as the formatter for
`.fango` files and enables format-on-save for them, both as ordinary settings
the user can override. It runs the executable named by `fango.path`, or one
built at the workspace root, or `fango` from `PATH`.

## Language server and editor support

`fango lsp` runs a Language Server Protocol server on standard input and
output. It accepts no paths or flags. The VS Code extension starts it for
`.fango` files using the same `fango.path` setting as the formatter. Install
the extension's npm dependencies before loading it from a local symlink.

Go to Definition follows values, functions, operators, types, constructors,
effect operations, imported modules, local binders, and nominal record fields
across local modules and the bundled library, including types in
[type witnesses](syntax.md#type-witnesses) and names in attribute
expressions and their quoted code. Hover shows a named symbol's type where one
is available. A contiguous group of `--` or `{- … -}` comments immediately above
a declaration appears below its type as Markdown, with the delimiters stripped
as [documentation comments](#documentation-comments) are; declaration pragmas and leading attribute
tags may sit between the comments and declaration or annotation. Multiline tag
contents do not become hover documentation. A blank line ends the group.
Bundled library files can also be opened directly for navigation and hover.
Find References searches `.fango` files in the workspace folders, including
unopened modules that import the queried symbol, and reachable bundled library
modules. It uses unsaved open buffers and includes the declaration when the
client requests it. Files that fail to check have no new reference index until
they are fixed. Hover does not infer the type of an arbitrary expression, and
document symbols are not available yet.

Open buffers, including unsaved local imports, are checked after a short
debounce. Errors appear as editor diagnostics and are cleared when resolved.
Errors in independent modules are reported together; a module depending on an
invalid one waits for that dependency to be fixed.
During an invalid edit, navigation, hover, and references can use the last
successful result where indexed symbols still occupy the same range with the
same text.
Workspace references refresh after open-buffer edits and watched file changes.
Diagnostics always describe the current buffer. The server uses full-document
sync and UTF-16 protocol positions.

## API documentation

`fango doc --stdlib` writes the bundled library's public API as JSON to
standard output, for tools such as the website to render. `--stdlib` is
required; no other source can be documented yet. Each `--module name`
selects one module by its exact name, and may be repeated; without one, every
bundled module is documented, including those the prelude never reaches.

Every bundled module is checked, but nothing is run beyond the compile-time
evaluation checking already does (attributes, splices, and derivers), and
nothing is built or written. An unknown module or an invalid argument exits
with status 2; a checking failure exits with 1. Diagnostics go to standard error and
standard output receives only the JSON document, on success.

`--strict` also requires documentation for each selected module and each
public declaration it owns, including constructors, record fields, class
methods, and effect operations. It reports every gap on standard error as
`path:line: id has no documentation`, writes no JSON, and exits with 1.
Re-exports and instances need no comment of their own. Without `--strict`,
undocumented entries carry an empty `documentation`.

### Documentation comments

Documentation is the Markdown in ordinary comments directly above a
declaration, with the [hover](#language-server-and-editor-support) attachment
rules: a contiguous group of `--` or `{- … -}` comments, each alone on its
lines, that a blank line ends. Declaration pragmas and the declaration's own
leading attribute tags may sit between the comments and the declaration. A
function's comment goes above its annotation, or above its first equation when
it has none. A module's goes immediately above its `module` line, after any
initial pragmas.

A comment line loses its `--` and one following space; a block comment's
later lines lose the indentation they share. Any further indentation is kept,
so fenced code and nested lists survive. A constructor or record field is
documented by a comment above it only when it begins its own line, after at
most a leading `=`, `|`, `,` or `{`; one written on its type's line shares
that line and has no comment of its own:

```fango
-- A value that may be absent.
type Maybe a
    -- No value.
    = Nothing
    -- A present value.
    | Just a
```

Library examples, as in [Maybe](../../stdlib/Maybe.fango), are fenced
`fango` blocks. A block brings its own imports, may bind helper names, and
every other top-level line is a `Bool` expression that holds. The test suite
runs them under both backends.

### Output

```json
{
  "schemaVersion": 1,
  "modules": [
    {
      "name": "Maybe",
      "documentation": "Optional values. …",
      "source": { "path": "stdlib/Maybe.fango", "line": 11 },
      "declarations": [
        {
          "id": "constructor:Maybe.Just",
          "name": "Just",
          "kind": "constructor",
          "signature": "Just : a -> Maybe a",
          "documentation": "A present value.",
          "source": { "path": "stdlib/Maybe.fango", "line": 27 },
          "parentId": "type:Maybe.Maybe"
        }
      ]
    }
  ]
}
```

Modules are sorted by name and each module's declarations by `id`. Output
depends only on the library sources: there are no timestamps or absolute
paths. `source.path` is relative to the repository, with forward slashes, and
`source.line` is the 1-based line that declares the name — an annotation's when
there is one.

| Field | Contents |
| --- | --- |
| `kind` | `value`, `type`, `constructor`, `field`, `class`, `method`, `effect`, or `operation` |
| `id` | `<kind>:<module>.<name>`; fields, methods, and operations add their parent, as in `method:Basics.Eq.==` and `operation:Fail.Fail.fail`. Operators use their bare spelling. |
| `name` | The declared name; operators are parenthesized |
| `signature` | Checked, not copied from source: inferred for unannotated functions, with constraints and effect rows. Types read `type Dict k v`, adding `= …` with their constructors or fields only when those are exported. Classes and effects read `class Eq a` and `effect Fail error`. Abort operations start with `abort`; scoped runners start with their `{-# scoped s #-}` line |
| `parentId` | A member's type, class, or effect, when the module exports it |
| `targetId` | On a re-export: the owner's declaration, whose signature, documentation, and source it repeats |
| `fixity` | An operator's declared fixity, such as `infixl 0` |
| `instances` | On a type or class: its checked instance heads across the library, derived ones included, as in `Ord a => Ord (Maybe a)`. A class names a type by module when another type shares its name |

Optional fields are omitted when absent. Private declarations, unexported
members, and the representation of an opaque type never appear.

## Generated Go projects

`build --emit-go` writes a complete Go project instead of an executable. For
`Main.fango`, its default destination is the `Main.out` directory in the
current working directory; `-o DIR` selects another directory. The project has
one `go.mod`, the shared `fangort` package, one package per imported Fango
module beneath `modules/`, and the program itself as a package main beneath
`entries/`, named after the source file. It can be compiled by running
`go build ./entries/Main` inside the directory without network access. It is
the same layout the build directory uses, which is what lets a program that
was built export without being generated again; exporting a second program
into one directory adds it beside the first rather than replacing it. Emission is quiet on
success unless a [verbosity flag](#build-progress-and-statistics) asks
otherwise; it stops before the Go toolchain, so it reports no linking stage. A
missing, empty, or previously Fango-generated destination is
accepted; a non-empty unmanaged directory is rejected. Exported `.out`
directories are output artifacts and are not removed by `fango clean`.

## Interpreting in WebAssembly

`cmd/fango-wasm` runs one program through the Core interpreter instead of
generating Go, so it needs no Go toolchain at run time. It builds for WASI:

```sh
GOOS=wasip1 GOARCH=wasm go build -o fango.wasm ./cmd/fango-wasm
```

The host supplies a filesystem holding a [library root](#the-library-root)
(only `stdlib/` is read) named by `FANGO_ROOT`, and passes the entry file as
the only argument (default `/src/main.fango`). Standard input is the
program's, output goes to stdout and diagnostics to stderr. The exit status is
the program's own, `1` for a compile error, and `2` for an internal error. A
non-Unit `main` value is printed as `print` would, through `Display`.

The interpreter runs without a native worker, so natives that need one (File,
Net, and Async) and user sidecars fail with an error rather than building one. The
website's playground runs programs this way in the browser.

## Entry points

A file passed to `build` or `run` must define `main`. A pure value is valid:

```fango
main = 42
```

A value-style `main` may perform ambient IO:

```fango
main : ()
main = print 42
```

The recommended effectful form is a nullary function:

```fango
main : () ->{IO} ()
main() = print "hello"
```

A value-style `main` may perform ambient IO but no unhandled custom effect. A
function-style effectful `main` must have exactly the shown IO/Unit shape and
one discarded Unit parameter; `main _ = ...` remains compatible. Non-Unit pure
`main` values are primarily observable in the REPL and test harness; an
ordinary built executable exits without printing them.
