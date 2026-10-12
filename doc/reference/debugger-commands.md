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
| `locals` | | Inspect visible local bindings |
| `print NAME` | | Inspect one binding without evaluating a Fango expression |
| `quit` | `q`, `exit` | End the debugging session |
| `help` | `h` | List supported commands |

Backend capabilities and execution semantics still differ:

| Operation | Interpreter | Native |
| --- | --- | --- |
| `frame INDEX` | Select a frame for inspection and next/finish | Not yet supported |
| `expand HANDLE [OFFSET]` | Page through structural children | Not yet supported |
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
or unavailable-span notices. Structural value formatting and preview bounds
remain backend-specific; see each backend's inspection reference.
