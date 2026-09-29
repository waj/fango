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

Author line breaks are preserved except when a multiline closing parenthesis
or lambda brace needs its own line, or a lambda's body and closing brace are
split across lines while its body starts on the arrow's line. Attributed nominal
records normalize to a multiline schema while preserving tag placement. Group
spans relative to the field name distinguish leading and trailing tags without
splitting the semantic attribute collection. Leading tags occupy separate lines
aligned with their following field declaration after the leading brace or comma.
The first inline trailing tag aligns using the longest participating rendered
field declaration plus two spaces; other fields do not affect that column.
Source gaps determine whether later tags share a line or continue one indentation
level below the field name. Attribute group boundaries and multiline contents
are retained. The printer does not search for a page width.
Spans identify multiline constructs, while source bytes locate keywords and the
definition name when AST NameSpan points at an annotation. Explicit semicolon
blocks retain separators and line structure, with each separator owned by its
left item.

Layout children indent below their owner, including braced lambda bodies
that the parser accepts at any column. Case/handler branches and block items
align. Then/else anchor to their own if. Composite commas, pipes, and closing
delimiters align with their opener; the parser admits
that punctuation at an enclosing layout boundary. Failed printing rolls back
the buffer before copying the declaration verbatim.
Application parentheses and lambda braces spanning lines close on a separate
line at the indentation of their opening line. Consecutive closings, including
mixed parentheses and braces, share that line when their openers share a line.
A lambda whose body starts and ends on the opening line stays wholly inline,
even if its source closing brace was on the next line. If the body itself spans
lines, it starts below the arrow or, for a Unit lambda, below the opening brace.
For a lambda used directly as a list item, tuple item, or record field value,
the closing brace aligns with the item or field name after its leading
punctuation. The container's own closing delimiter keeps its usual alignment.
Quotation spans retain both backticks. The printer renders their contents as
ordinary expressions, preserving body line breaks; a closing backtick written
on its own line aligns with the enclosing expression layout. Quotations are
atoms and do not require parentheses in argument position.
The printer consults matching tokens because grouping parentheses are absent
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
