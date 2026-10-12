# Shared debugger commands

The stopped prompts in [REPL debugging](debugging.md) and
[native debugging](native-debugging.md) share command syntax, validation, help,
and presentation.

At `debug>`, a leading colon is optional: `next`, `:next`, `n`, and `:n`
are equivalent. Blank input repeats the last syntactically valid, supported
command in the current execution; an initial blank does nothing. Invalid
syntax or an unsupported command does not replace that command. Arguments are
validated before execution; commands without arguments reject extra text.
`help` lists only the current backend's supported commands.

The ordinary REPL prompt still requires colon commands. In particular,
`:step EXPR` starts an evaluation there; `step` takes no expression while
stopped. Debug command aliases do not reserve identifiers in Fango source.

| Command | Alias | Behavior |
| --- | --- | --- |
| `break FILE:LINE` | `b` | Set an exact-line breakpoint |
| `breaks` | | List breakpoints and their status |
| `delete ID` | | Remove a breakpoint |
| `continue` | `c` | Resume until a breakpoint or completion |
| `step` | `s` | Enter the next Fango source location |
| `next` | `n` | Step over calls |
| `finish` | `f` | Run until the activation returns |
| `stack` | | List frames |
| `frame INDEX` | | Select a frame for inspection and next/finish |
| `expand HANDLE [OFFSET]` | | Page through structural children |
| `locals` | | Inspect visible local bindings |
| `print NAME` | | Inspect one binding without evaluating a Fango expression |
| `quit` | `q`, `exit` | End the debugging session |
| `help` | `h` | List supported commands |

Backend capabilities and execution semantics still differ:

| Operation | Interpreter | Native |
| --- | --- | --- |
| `args` | Parameters are included in locals | Separate argument view |
| `locals` | Parameters, lexical locals, and captures | Locals and captures, excluding values classified as arguments |
| `raw args`, `raw locals`, `raw print EXPR` | Not yet supported | Go-level inspection |
| `stop` | Cancel the current input and return to the REPL | Not supported |
| `stack` | Fango source frames | Native frames, including generated frames |
| Ctrl-C | Cancel the current input | Interrupt execution |
| `quit` | Cancel the input, end handler levels, and leave the REPL | Terminate the launched process |

Unsupported operations produce an explicit diagnostic. Common command names
do not imply identical stop sequences: the interpreter has expression sites,
while native locations depend on linked machine code. Breakpoint resolution,
source generations, input, EOF, and cleanup follow the linked backend
references.

Source bindings display as `name : Type = value`, with `[handle ID]` when
expansion is available. Frames use `#INDEX name at FILE:LINE[:COLUMN]`;
breakpoints include their ID, location, and status. Errors use a `debugger:`
prefix. Source excerpts share caret underlines and retain explicit enclosing
or unavailable-span notices.

## Structural value previews

Both debuggers use the same formatter for source inspection. Lists display as
`[1, 2, 3]`, records as `{ name = "Ada", age = 42 }`, constructors as
`Just 42` or `Branch (Leaf 1) (Leaf 2)`, and tuples as `(7, "seven")`.
Empty records display as `{}`. Only the active constructor's fields appear.
Booleans, characters, strings, and unit use their Fango forms; string contents
escape interpolation markers. Inspection never invokes `Show` or other target
functions. Functions display as `<function>`, resources as `<opaque Type>`,
and unknown representations as `<opaque>`. Unavailable values carry a marker
such as `<unavailable>` or a more specific backend reason.

Each value preview allows four structural levels, 50 children per collection
or record, 512 visited values, and 256 bytes per string. String truncation
preserves complete UTF-8 characters. Lists spend depth on their elements,
not on their spine. Truncation uses `…`, and detected cycles use `<cycle>`.
These are previews: truncated or opaque output is not a complete Fango
expression. Native inspection also bounds debugger reads; an earlier backend
read limit or unavailable memory can yield a shorter preview.

`expand HANDLE [OFFSET]` returns up to 50
children, each with the same formatting and preview bounds. Handles expire
when execution resumes or ends. See the backend references for frame access,
raw inspection, and additional traversal limits.
