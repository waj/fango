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
fango build [-o out] [--emit-go] main.fango
fango run main.fango [--] [args...]
fango check main.fango
fango fmt [-w] [-l] [file...]
fango repl [dir]
fango clean main.fango
```

`build` writes a native executable (defaulting to the source basename without
`.fango`). `run` builds if needed and runs the cached executable, forwarding
every argument after the source path to the program; an optional `--` is
removed first. `check` runs
through parsing, inference, elaboration, and Core validation without generating
Go. `clean` removes the source file's persistent `.fango/` build and compilation
cache artifacts.
`repl` starts an interactive session whose source root is `dir`, or the
working directory; see [REPL](repl.md).

Successful `check`, `build`, and `run` results are cached under
`.fango/cache/v1/` beside the entry file. If every previously discovered local
source and native sidecar still has the same content, an unchanged command can
skip source loading and compilation. The cache is transparent: corrupt,
incompatible, or unwritable entries are ignored and rebuilt, and there is no
status or disable flag. Distinct compiler executables and build modes use
distinct entries. `fmt` always parses its requested input and is not cached.

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
success. A missing, empty, or previously Fango-generated destination is
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
