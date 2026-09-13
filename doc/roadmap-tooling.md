# fango developer tooling: formatter and editor support

This document owns two unstarted pieces of work that together make fango
usable in an editor: a source formatter and a language server. They are in one
document because they share prerequisites — capturing comments in the lexer, a
check entry point that accumulates diagnostics across stages, and byte-offset
to editor-position conversion — and splitting them would leave those shared
decisions owned by neither file.

Implemented architecture belongs in [the design](design.md) and implemented
behavior in [the reference](reference.md). Neither tool exists yet; both are
listed under Known limitations in the design.

## The shape: a library, not a tool inside a tool

The formatter is a library, `internal/format`, exposing a pure function from
one file's bytes to bytes. `fango fmt` is a thin front end over it, and
`fango lsp` later calls the same library for `textDocument/formatting`. One
binary, no duplication, no subprocess discovery.

The alternatives were considered and rejected. Putting the formatter inside
the language server would mean driving JSON-RPC to test formatting, in a
repository whose verification culture is golden files over `testdata/`; it
would also rule out a CI formatting gate and headless use. Shipping a separate
binary would add a second build target and the version- and
discovery-mismatch problems that `rustfmt`/rust-analyzer and
`elm-format`/elm-language-server both pay for, in exchange for nothing —
`cmd/fango` dispatches subcommands with a plain switch.

The decisive constraint is that **formatting must not need the module graph**.
`fango fmt` has to work on a file with type errors, missing imports, or no
project around it. So it runs on `lexer.Lex` and `parser.Parse` only,
deliberately before `internal/fixity`, which needs `modules.Load` to supply
imported fixities. Operator runs stay flat as `ast.OpChain` until fixity
resolution, so the pre-fixity AST is exactly the right input: the formatter
prints a run as written and never re-derives precedence or associativity.

The formatter is independently shippable and independently useful; the
language server is not. That settles the order — the formatter first.

## Formatter

### Style: author breaks are preserved

The formatter normalizes indentation and inter-token spacing and preserves the
author's decision to split or join a construct. It does not reflow to a target
width: a long line the author wrote stays long. This is the `gofmt` model
rather than the `elm-format` one.

The consequence for the implementation is that break decisions are not
searched for, they are read off the input. A construct was written multi-line
exactly when its source span contains a newline, which every node's `Span`
answers directly against `File.Content`.

### Comments: a lexer side channel

`skipSpaceAndComments` currently discards line and block comments by advancing
the scan position, so comments reach neither the token stream nor the AST.
They will be collected into a side list returned alongside the tokens, leaving
`Lex` and its callers unchanged.

Comment tokens in the main stream were rejected for a specific reason.
`parsePostfixAtom` implements the documented rule that whitespace or a comment
before `()` makes it an ordinary application as a byte-adjacency test on
neighbouring token spans. Interleaving a comment token would make a commented
call byte-adjacent to its predecessor and silently invert that rule. Several
other sites index the token slice directly for lookahead and forward scans and
would each need trivia-skipping wrappers.

A separate re-scan was also rejected: it would duplicate the nesting-depth
logic and the string- and character-literal skipping that stops `--` inside a
string literal from opening a comment, and that duplicate would drift from the
lexer silently.

The lexer gains one invariant test: tokens, comments, and whitespace must
exactly tile the file's content. That is what makes "no comment can be lost"
checkable rather than hoped for.

### Declaration extents

Comment attachment and blank-line preservation both need to know where a
declaration starts and ends, and `Decl` carries no span today.

The span will be added as a field on the declaration nodes the parser builds,
read through a free `ast.DeclSpan` function, rather than as a method on the
`Decl` interface. Several packages synthesize declarations — deriving, class
elaboration, module loading — and those have no meaningful source extent; a
required method would be a lie at each of those sites permanently. A free
function reports the zero span for them instead. The AST dump is untouched, so
no parse goldens churn.

For a value declaration the span covers the whole equation group including its
annotation line, which is what the reference's rule that blank lines and
comments do not split a group requires.

### Output must re-parse to the same tree

Indentation is semantics in a layout-sensitive language, so the formatter
carries two independent defenses.

The renderer tracks a minimum-column stack mirroring the parser's layout
contexts, and refuses to emit a line at or left of the innermost layout
column. That catches the dangerous class at its source: a continuation line
landing back at the case-branch column silently becomes a new branch.

Beyond that, the formatter re-lexes and re-parses its own output and compares
span-free trees before returning anything. On a mismatch it returns the
original bytes and an internal error. `gofmt` does not do this because Go is
not layout-sensitive; for fango it is the single most valuable decision in the
design, because no bug in parenthesization, un-desugaring, or rendering can
then corrupt a file.

### What the parser drops, and how it comes back

Literal spelling is decoded at parse time, but every literal node carries a
span, so raw text is recovered by slicing the source. One helper does this and
is the only code permitted to print a literal.

Parentheses have no AST node. A nested `OpChain` appearing as an operand can
only have arisen from explicit parentheses, since the parser keeps runs flat,
so re-wrapping exactly those reproduces the original grouping; the remaining
cases are the ordinary ones of non-atomic application arguments and negation
operands.

List and tuple literals are lowered to constructor applications at parse time.
The `Sugared` flag distinguishes them from a hand-written constructor
application, and gating the un-lowering on that flag is both necessary and
sufficient — spans cannot help, because every synthetic constructor in a
lowered list shares the whole bracket span.

Two nodes carry dual representations, a single-row form and a grouped form for
value declarations, and a legacy binding list beside an ordered item list for
blocks. The printer normalizes each through one accessor.

Calls written against `()` are recoverable structurally rather than lost: the
adjacent form binds tighter, so the two spellings differ in tree depth.

### Refusing, and the verbatim fallback

The formatter refuses, leaving the file byte-identical, on a lex error, on a
parse error, and on a failed self-check. Refusing on parse errors is not
conservatism: recovery drops a failed declaration entirely and there is no
error node, so formatting a broken file would silently delete code.

Within a successful format, any declaration holding a comment in a position
with no anchor — between an operator and its operand, say — is copied
verbatim from its source region. Comments then cannot be moved or lost by
construction, and the formatter is useful on real code from the first stage
rather than the last.

### Staging

The first increment formats only the module header, imports, pragmas, fixity
declarations, and type and effect declarations, copying every other
declaration verbatim. That ships a useful `fango fmt` — canonical import
blocks being the most-wanted part — while exercising the whole skeleton:
document IR, renderer, region table, self-check, CLI, goldens, and the corpus
gate. Expressions, then layout constructs, then author-break fidelity, then
full comment reassociation follow, with the verbatim fallback holding
correctness throughout.

The gate lands last: a one-time reformat of the bundled standard library and
the examples, then a listing mode wired into `make ci` over those two trees
only. Test data is excluded, since it holds deliberately malformed inputs.
`make fmt` already means gofmt over the Go sources, so the new target needs a
distinct name.

### Open decisions

- Comment attachment rules: which anchor a comment binds to when it sits
  between two constructs, and whether a blank line before it changes that.
- The style rules themselves: operator-chain wrapping, import ordering and
  whether imports are sorted at all, spacing inside brackets and records,
  and alignment of equation groups.
- Blank-line policy. A proposal to start from: collapse runs to one, exactly
  one blank line between top-level declarations, none inside an equation
  group.
- Whether a later `ast.Bad` declaration node should let the formatter work on
  files that do not parse. It would also improve batch `fango check`, which
  today reports one syntax error per run. The formatter should not be coupled
  to it.

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
its surface is still changing. The VS Code extension is grammar-only today,
with no build step at all; adding a client means a TypeScript toolchain, a
lockfile, and a bundler, and should wait until the server has earned it. The
TextMate grammar stays either way — it is the pre-server-start fallback and
coexists with semantic tokens.

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
