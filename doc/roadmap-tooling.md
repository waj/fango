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

[`fango doc`](reference/commands.md#api-documentation) is implemented, and
every bundled module is documented in source, with no separate library
reference pages, under the
[verification gate](design/verification.md#differential-fixtures-and-examples).

- Decide how to document a user's own modules: which modules a local source
  root publishes, and how paths are reported outside the repository.
- Many IO, network, and task operations have no natural `Bool` assertion.
  Decide whether such examples stay prose, or whether the example runner
  gains a form that only checks a block compiles.

## REPL hardening

Fresh-session artifact reuse, atomic imports, and
[transactional reload](design/repl.md#transactional-reload) are implemented.
The work below extends those contracts.

- Add a grouped-input mechanism for multiple top-level function equations;
  today the prompt accepts only one exhaustive equation per input.
- Reloading changed native sidecars while retaining native heap values needs
  an explicit worker-generation or migration contract; current reload keeps
  the worker and rejects incompatible changes. Reclaiming unreachable historical
  generations is also deferred; current sessions retain them until exit.
- Decide whether the prompt should be able to see a module's private
  top-level scope, the way GHCi's `:load` puts the prompt inside a module.
  `import` shows only the public interface, which is consistent with every
  other module; debugging a private helper currently means exposing it.
- Extend interruption coverage if CPU loops gain automatic checkpoints;
  current [cancellation](../stdlib/Async.fango) requires an
  explicit checkpoint in such loops.
- Decide whether leaving many [handler levels](reference/repl.md#handler-levels)
  needs something faster than one Ctrl-D or `:end` per level, once levels have
  seen use. `:type` does not yet show which of an expression's effects the
  installed levels handle.

- Add interactive line editing and persistent history.
