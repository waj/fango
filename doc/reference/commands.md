# Setup and commands

Compiler setup, command behavior, formatting, generated projects, and main.

[Reference index](../reference.md).

The repository uses Go 1.26 and provides a Nix development environment:

```sh
nix develop
go build -o fango ./cmd/fango
```

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
```

Flags precede the source path, because everything after it belongs to the
program `run` is about to start.

`build` writes a native executable (defaulting to the source basename without
`.fango`). `run` builds if needed and runs the cached executable, forwarding
every argument after the source path to the program; an optional `--` is
removed first. `check` runs
through parsing, inference, elaboration, and Core validation without generating
Go. `clean` removes the source file's persistent `.fango/` build and compilation
cache artifacts, its source-root fallback compilation cache, and the legacy
per-entry fallback namespace when present.
`repl` starts an interactive session whose source root is `dir`, or the
working directory; see [REPL](repl.md).

Compilation results are cached per module under `.fango/cache/v1/` beside the
entry file. Each module keeps one checked result and one generated Go file
there, and recompiling replaces them, so the cache grows with the modules a
project has rather than with its edit history — and restoring a file to an
earlier state recompiles it rather than finding what that state compiled to
before. Entry programs in one directory share the modules they have in common
and keep their own entry results, so alternating between them stays warm.

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

The standard library and the Go runtime support are files the compiler reads,
not part of the executable, so editing one takes effect on the next build. They
live in a single root holding `stdlib/` and `runtime/`, found in this order:

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

An installed layout therefore looks like:

```text
<prefix>/bin/fango
<prefix>/lib/fango/stdlib/
<prefix>/lib/fango/runtime/
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
entry file that declares no module header is named `<entry>`. A
build whose generated sources are all unchanged relinks nothing and prints no
`Linking` line. `check` stops after `Checking`.

`-vv` names the work running under each `Checking` line — elaborating, building
stage Core, linting Core — because most of a slow module's cost falls after its
check, and the module line alone cannot say which part a pause belongs to. It
then adds a table of where the time went and what the cache moved. Stages that
belong to no module — writing the generated project, and the Go toolchain — are
timed by the command itself, and time that belongs to no stage is reported as
`other`, so the rows always reconcile with the total. Modules that arrive from
cache defer their stage Core until some module is checked from source; the
`stage Core` row and the count beneath the cache block are what reading it
cost. Each reuse count is over every module that stage covered, so a module
whose generated code cannot be cached counts against reuse rather than
disappearing from the ratio.

`-timings json` writes the same measurements as one JSON object instead of the
prose, for recording build cost over time. `-no-cache` ignores and publishes no
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

The formatter keeps the author's line breaks rather than reflowing to a width,
so a construct written across several lines stays that way and one written
inline stays inline. That extends to where a keyword sits: a body moved below
its `=` or `->` stays below it, a `case` written on its declaration's own line
keeps its branches one level in from there, and a `then` or `else` given a line
of its own is anchored at the column of its `if`, so a chain of arms lines up
instead of staircasing rightward. An `exposing` list the author moved below
its keyword is printed in the leading-comma block form.

A multiline nominal type is the exception: its `deriving` clause is always an
indented line after its constructor alternatives or record schema. An inline
type may instead keep `deriving` on the declaration line or on a following
indented line, matching the source.

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

## Generated Go projects

`build --emit-go` writes a complete Go project instead of an executable. For
`Main.fango`, its default destination is the `Main.out` directory in the
current working directory; `-o DIR` selects another directory. The project has
one `go.mod`, a root `main.go`, the shared `fangort` package, and one package per
imported Fango module beneath `modules/`. It can be compiled by running
`go build .` inside the directory without network access. Emission is quiet on
success unless a [verbosity flag](#build-progress-and-statistics) asks
otherwise; it stops before the Go toolchain, so it reports no linking stage. A
missing, empty, or previously Fango-generated destination is
accepted; a non-empty unmanaged directory is rejected. Exported `.out`
directories are output artifacts and are not removed by `fango clean`.

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
