# Fango developer tooling: formatter and editor support

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
3. **Constructs that still fall back.** Making the corpus walk in the format
   tests fail on `INTERNAL FORMATTER ERROR` instead of skipping shows ten
   fixtures the printer cannot yet reproduce: `parse/{apps, operator_decls,
   records_inferred, semicolon_blocks}`, `check/operators`,
   `core/operators`, and `run/{err_meta_nested_quote,
   module_function_groups, row_argument_closed, user_operators}`. Close
   them, then make that walk strict so a new fallback cannot hide behind the
   skip; `TestHandlerFixturesFormat` pins the handler fixtures meanwhile.

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

The first server and VS Code client are implemented; see [editor
behavior](reference/commands.md#language-server-and-editor-support) and
[editor analysis](design/pipeline.md#editor-analysis).

The editor checker collects errors from independent modules after a module
fails, but the parser does not recover a failed file into a partial AST.
Future recovery could report syntax and type errors across an invalid graph
without cascaded errors from missing declarations. Other possible features are
inferred types on arbitrary expressions, document symbols, completion, and
semantic tokens. Non-VS Code client configuration can be documented when tested.

## API documentation

[`fango doc`](reference/commands.md#api-documentation) is implemented for the
bundled library. Most modules are documented in source;
`documentedModules` in the [doc command tests](../cmd/fango/doc_test.go)
lists them, holds them complete under `--strict`, and runs their examples
under both backends. A module joins that list when it reaches no gaps.

- Document the remaining bundled modules: IO, Net, Reader, Writer,
  Text.Reader, Text.Writer, Async, Http and its submodules, Json and its
  submodules, and Meta. Then replace the library reference topics with links
  to the generated reference once the website renders it.
- Decide how to document a user's own modules: which modules a local source
  root publishes, and how paths are reported outside the repository.
- Many IO, network, and task operations have no natural `Bool` assertion.
  Decide whether such examples stay prose, or whether the example runner
  gains a form that only checks a block compiles.

## REPL hardening

Fresh-session artifact reuse and atomic installation of prompt imports are
implemented ([REPL](design/repl.md#imports-and-resolution)). The work below
extends the session beyond them; reusing artifacts introduces no reload and
changes no declaration a live session has already accepted.

- Add a grouped-input mechanism for multiple top-level function equations;
  today the prompt accepts only one exhaustive equation per input.
- Implement `:reload`, re-reading the modules a session imported after they
  change on disk. `import` already gives the prompt a persistent module graph
  and resolver scope, so `:load` is not needed: a named module is imported,
  and the working directory (or the directory given to `fango repl`) is the
  source root. The intended shape: the graph re-reads every non-bundled node,
  compares content hashes, and uses the shared cache pipeline to prepare the
  changed modules plus their affected dependents, committing only on success.
  The checker gains owner/generation retraction for declaration tables,
  instances, and staging definitions while preserving the identities and
  declaration cutoffs needed by existing closures. Build on the implemented
  [module-state boundary](design/pipeline.md#pipeline) rather than depending on
  positional instance indexes; the operator
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
- Extend interruption coverage if CPU loops gain automatic checkpoints;
  current [cancellation](reference/library-async.md#cancellation) requires an
  explicit checkpoint in such loops.
- Add transcript coverage for reload, cross-generation errors, handler
  interaction, and recovery after failures.
- Decide whether leaving many [handler levels](reference/repl.md#handler-levels)
  needs something faster than one Ctrl-D or `:end` per level, once levels have
  seen use. `:type` does not yet show which of an expression's effects the
  installed levels handle.

- Add interactive line editing and persistent history.
