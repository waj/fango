# fango developer tooling: formatter and editor support

Remaining formatter, language-server, and REPL work. Implemented formatter
invariants live in [design](design/formatter.md), command behavior in
[reference](reference/commands.md#formatting), and session invariants in
[REPL design](design/repl.md).

## Formatter

### Remaining work

1. **More comment anchors.** A comment reaching no anchor through whitespace
   alone sends its declaration to a verbatim copy, which is correct but coarse.
   Anchors inside an application's argument list and an operator run would
   narrow it further. There will always be positions with no sensible anchor,
   so the fallback stays.
2. **A renderer-level layout assertion** mirroring the parser's layout stack,
   refusing to emit a line at or left of the innermost layout column. The
   self-check already catches the damage after the fact; this would catch it at
   its source and name the construct responsible.

Each piece ends with a reformat of the standard library and the examples, which
the `ci` gate then holds.

### Open decisions

- The remaining style rules: spacing inside brackets and records, and whether
  equation groups align anything.
- Whether blank-line normalization needs a more specific style policy.
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

### Open decisions

- Which protocol library, or transport-only plus hand-written structs.
- Whether to advertise position-encoding negotiation or always convert to
  UTF-16.
- Feature order after the first version: go-to-definition, document symbols,
  completion, semantic tokens.

## REPL hardening

- Add a grouped-input mechanism for multiple top-level function equations;
  today the prompt accepts only one exhaustive equation per input.
- Implement `:reload`, re-reading the modules a session imported after they
  change on disk. `import` already gives the prompt a persistent module graph
  and resolver scope, so `:load` is not needed: a named module is imported,
  and the working directory (or the directory given to `fango repl`) is the
  source root. The intended shape: the graph re-reads every non-bundled node,
  compares content hashes, and re-resolves the changed modules plus their
  reverse dependents in a staging map committed only on success; the checker
  gains a `Retract(owners)` that deletes the canonical-keyed entries of those
  modules (types, classes, effects, constructors, values, workers, methods,
  operations, natives, capture summaries, derivers by owner) and marks their
  instances retracted rather than removing them, because instance limits are
  positional and the compile-time evaluator tracks an append-only declaration completion log,
  so retraction must preserve those identities and cutoffs; the operator
  table is rebuilt from the current nodes plus the prompt's own fixity
  declarations; the prompt's import list is re-applied against the new
  interfaces and names that vanished are reported; old memo cells and
  closures keep their old bindings, as they do under prompt redefinition.
- Decide whether the prompt should be able to see a module's private
  top-level scope, the way GHCi's `:load` puts the prompt inside a module.
  `import` shows only the public interface, which is consistent with every
  other module; debugging a private helper currently means exposing it.
- Reconcile values, custom types, constructors, and effects by generation so
  unchanged declarations retain identity while changed generative declarations
  cannot be confused with old values or closures.
- Decide dependency invalidation and whether removed declarations remain
  addressable by existing closures only.
- Connect Ctrl-C to the cleanup and cancellation protocol in
  [cooperative structured async](roadmap-effects.md#4-cooperative-structured-async)
  without corrupting the session or consuming input intended for `readLine`.
  Basic prompt cancellation may ship earlier once its active execution path
  has the corresponding cleanup guarantees.
- Add transcript coverage for reload, cross-generation errors,
  cancellation, handler interaction, and recovery after failures.

- Add interactive line editing and persistent history.
