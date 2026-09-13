# fango developer tooling: formatter and editor support

This document owns the unfinished half of two pieces of work that together
make fango usable in an editor: a source formatter and a language server. They
are in one document because they share prerequisites — comments on a side
channel from the lexer, a check entry point that accumulates diagnostics across
stages, and byte-offset to editor-position conversion — and splitting them
would leave those shared decisions owned by neither file.

The shape is settled and partly built: `internal/format` is a library, `fango
fmt` is a front end over it, and the language server will be another. [The
design](design.md) records the architecture that is implemented — the comment
side channel, formatting as a single-file pre-fixity operation, preserved
author breaks, the verbatim fallback, and the self-check — and [the
reference](reference.md) records what `fango fmt` does today. What remains is
below.

## Formatter

Every declaration in the bundled standard library and the examples now prints
structurally, comments included, and the corpus round-trips through the
formatter unchanged. A declaration is still copied verbatim when it holds a
comment with no anchor, or when some part of it has a line structure the
printer does not reproduce.

### Remaining work

1. **Bracket lists and tuples broken across lines.** A list written over
   several lines is the one composite left without a block form; it needs the
   same spelling decision the `exposing` and record block forms already
   settled.
2. **More comment anchors.** A comment reaching no anchor through whitespace
   alone sends its declaration to a verbatim copy, which is correct but coarse.
   Anchors inside an application's argument list and an operator run would
   narrow it further. There will always be positions with no sensible anchor,
   so the fallback stays.
3. **A renderer-level layout assertion** mirroring the parser's layout stack,
   refusing to emit a line at or left of the innermost layout column. The
   self-check already catches the damage after the fact; this would catch it at
   its source and name the construct responsible.

Each piece ends with a reformat of the standard library and the examples, which
the `ci` gate then holds.

### Traps worth remembering

- `a--b` is a comment, not an operator. The emitter must never put `-`
  immediately after `-`. Operator runs and negation are always spaced today, so
  nothing produces it, but a tighter spelling would.
- `resume` heads an application, so `resume value with next` is the head, its
  argument, and then the state clause — the `with` clause renders after the
  arguments, not on the head.
- A declaration's `NameSpan` points at the name on its annotation line, not on
  its definition line, so breaks and adjacency are read from the source rather
  than from spans.
- Blocks carry two AST shapes, a legacy binding list and an ordered item list,
  and both must print the same way.
- A destructuring binding has no name, so its `NameSpan` is the zero span. A
  block deriving its own span from that reported offset zero with no file
  attached, which read as "written on one line" and sent every such declaration
  to a verbatim copy.

### Open decisions

- The remaining style rules: spacing inside brackets and records, and whether
  equation groups align anything.
- Whether the author's blank lines below the imports need any rule beyond
  reproducing them, which is what happens above the imports today.
- Whether an `ast.Bad` declaration node should let the formatter work on files
  that do not parse. It would also improve batch `fango check`, which reports
  one syntax error per run today. The formatter should not be coupled to it.

## Language server

Scope for a first version: diagnostics, formatting, and coarse hover, as a
`fango lsp` subcommand of the same binary.

### Prerequisite: extract the check path

`compileFileGraph` lives in `cmd/fango` and stops at the first stage that
produces errors, so a single syntax error anywhere in the graph hides every
type error everywhere. It moves to `internal/check`, accumulating diagnostics
across stages, and takes a `modules.Provider` instead of constructing a
filesystem provider internally. The existing `check`, `build`, and `run`
subcommands become callers. This is worth doing on its own merits and is most
of the server's backend.

### What a useful first version needs

An overlay provider for unsaved buffers, over that new seam — without it the
server reports diagnostics for the last saved state. Position conversion,
because `source.Pos` counts bytes while the protocol defaults to UTF-16 code
units; the conversion belongs in the server, and the only thing `source` needs
is the inverse direction from a position back to a byte offset. Publishing
hygiene, since diagnostics are per-URI and sticky and must be cleared for
files that no longer have errors, including dependency files the user never
opened. Full-document sync with a short debounce; incremental change
application is not worth implementing.

### What it does not need

Severity stays unmodeled: the compiler has no warnings, so the field would
have one value. Diagnostic codes need no schema change either — the existing
title maps onto the protocol's code field and the Elm-style prose onto the
message, so the rendering the compiler already produces survives into the
editor.

Hover ships in its cheap form first: match the hovered offset against
identifier spans and look the name up in the declaration table the checker
already returns, rendering with the existing type printer. That is the REPL's
`:type` promoted into the editor, and it needs no checker change. A real
span-to-type index — a checker observer recording spans against types, with
the final substitution applied at query time rather than record time — is a
later version, and once it exists go-to-definition is nearly free, since the
declaration table already carries name spans.

### Dependencies

The protocol layer will use a library rather than hand-rolled JSON-RPC. This
introduces the repository's first external Go dependency, which sits against
the design's stated goal of one Go toolchain and no compiler framework
dependencies. Two things keep that honest: the dependency is scoped to the
server package, so the compiler and the formatter stay dependency-free and no
existing subcommand gains a transitive dependency; and the lighter the
dependency tree the better — a full protocol package pulls in a logging stack
and its own encoder, where transport-only framing plus hand-written structs
for the handful of methods actually answered would not.

The design's wording needs restating when this lands, so that the invariant
reads as "the compiler is dependency-free" rather than being quietly
contradicted.

### Editor clients

No VS Code language client at first. Helix, Neovim, and Zed attach to an
arbitrary server binary with a few lines of declarative configuration and no
build step, so documenting those is enough to get the server real use while
its surface is still changing. The VS Code extension carries a formatting client
already, but that is a few dozen lines of plain JavaScript against Node
builtins and the `vscode` module the host supplies. A language client is a
different proposition — a TypeScript toolchain, a lockfile, and a bundler — and
should wait until the server has earned it. The TextMate grammar stays either
way: it is the pre-server-start fallback and coexists with semantic tokens.

Capturing comments changes lexer internals but not comment syntax, so the
grammar needs no change for the formatter. The lockstep rule is satisfied by
re-tokenizing the standard library and examples and confirming the language
configuration still matches what the formatter emits.

### Open decisions

- Which protocol library, or transport-only plus hand-written structs.
- Whether to advertise position-encoding negotiation or always convert to
  UTF-16.
- Feature order after the first version: go-to-definition, document symbols,
  completion, semantic tokens.
