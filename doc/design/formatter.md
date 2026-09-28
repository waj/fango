# Formatter architecture

Single-file formatting, layout preservation, comment ownership, and round-trip verification.

[Design index](../design.md). Source and checks: [Formatter](../../internal/format/format.go), [Equivalence](../../internal/format/equiv.go), [Layout](../../internal/format/layout.go), [Corpus and safety tests](../../internal/format/format_test.go).

## Parser boundary

internal/format is a library over one file's bytes, used by fango fmt and the
VS Code formatting provider. It needs no module graph and stops before fixity
resolution, so unresolved or ill-typed files can still format. Operator runs stay
flat and never reassociate. [Commands](../reference/commands.md#formatting) owns
spacing, import sorting, literal spelling, and line-break behavior.

The lexer returns comments on a side channel. Tokens, comments, and whitespace
tile every successfully lexed file. Comments cannot become ordinary tokens:
adjacency distinguishes `f()` from `f ()`, and lookahead indexes the token slice.

## Layout and source fidelity

Author line breaks are preserved; the printer does not search for a page width.
Spans identify multiline constructs, while source bytes locate keywords and the
definition name when AST NameSpan points at an annotation. Explicit semicolon
blocks retain separators and line structure, with each separator owned by its
left item.

Layout children indent below their owner; case/handler branches and block items
align. Then/else anchor to their own if. Composite commas, pipes, and closing
delimiters align with their opener; the parser admits that punctuation at an
enclosing layout boundary. Failed printing rolls back the buffer before copying
the declaration verbatim.
Application parentheses retain a source break before the closing token. The
printer consults the matching token because grouping parentheses are absent
from expression spans.

A broken nominal type body is a layout exception: its deriving clause is emitted
as a separate indented line. A line break solely before `deriving` does not make
an inline right-hand side broken, so that split remains source-controlled.

Import sorting is the exception to line-structure preservation. Directly preceding
comments move with imports; detached comments stay with the block. Sorted broken
exposing lists use kind grouping and width wrapping because sorting discards the
original layout. Inline lists remain inline.

## Comment anchors and safety checks

Anchors include statements, branches, clauses, declaration bodies, and trailing
comments. A comment must reach its anchor through whitespace alone. If any comment
cannot be placed, or any shape cannot be rendered, preserve the entire declaration
verbatim. A declaration counts as printed only when all its comments are placed.

Re-lex and re-parse the output and compare span-free trees and comment text before
returning. Canonicalize intended import/exposing order changes and compare comments
as a multiset; fixtures separately check ordering. Any mismatch returns original
bytes. Lex/parse failure refuses formatting because recovery may omit declarations.

Emitter hazards: never join two minus characters into a comment; render a resume's
state clause after its arguments; handle legacy binding lists and ordered block
items consistently; destructuring has no NameSpan, so derive its extent from its
actual pattern/body. Missing span information must never imply inline layout.
