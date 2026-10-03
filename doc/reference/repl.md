# REPL

Persistent prompt scope, imports, transactions, redefinition, commands, and line editing.

[Reference index](../reference.md).

`fango repl [dir]` evaluates expressions, installs
value/function/type/effect/class/instance/deriver declarations, imports
modules, and accepts multiline layout-sensitive input. The prompt starts in
the same scope `Prelude` gives a module, and, since a later prompt may use
them, it also resolves the modules surface syntax desugars into — so `[1, 2]`,
`(1, 2)`, and `deriving` work without putting `List`, `Tuple`, or `Derive` in
scope. Names resolve exactly as they do in a module: an unqualified name must
be exposed by the prelude, an import, or a prompt declaration, and a qualifier
must be an imported module's name or alias, so `Dict.empty` is an
`UNKNOWN QUALIFIER` until `import Dict`, while `IO.write` works because the
prelude imports `IO`. Definitions echo
their inferred types; expressions print a value and type. Errors do not end the
session. Redefinition is allowed at the prompt, while existing memoized values
and closures retain earlier bindings.

## Imports

An `import` line at the prompt takes every form a module's import does:

```text
> import Geometry.Point as P exposing (origin)
loaded Geometry.Point
```

The source root is the directory given to `fango repl`, or the working
directory, and a local `Foo.Bar` resolves to `Foo/Bar.fango` beneath it under
the same rules and diagnostics as a build (`MISSING MODULE`, `RESERVED
MODULE`, `MODULE/PATH MISMATCH`, `IMPORT CYCLE`, and so on). Bundled modules
outside the prelude, such as `Dict` or `String`, import the same way. The
session echoes `loaded M` for each module the import brought in for the first
time, dependencies included, in dependency order; a module already loaded
echoes nothing. The imported module's instances and derivers become usable at
the prompt, and its sidecar, if any, runs in the session's native worker.

Prompt imports are cumulative. Importing a module again adds the names its
new exposing list selects, and repeating an alias for the same module is
accepted; binding the alias to a different module is a `DUPLICATE IMPORT
ALIAS`, as it would be in a file. An import is all-or-nothing: if any of its
modules fails to load, check, or expose a requested name (`UNKNOWN IMPORT`),
the session keeps neither the modules nor the names, and the same import can
be retried after the file is fixed.

## Declarations and redefinition

A prompt declaration may redefine a name the prompt itself declared, but not
one an import or the prelude exposes: that is the `UNQUALIFIED COLLISION` it
would be in a module. Qualified access to the exposed name stays available.
Imports see only a module's public interface; a private name is a `PRIVATE
OR UNKNOWN NAME` under any qualifier.
Prompt inputs remain sequential; module-wide function visibility applies to
imported source modules. The prompt accepts a single exhaustive patterned function equation and
top-level destructuring bindings. It does not collect multiple function
equations into a grouped input; use a source file for those.
Record type declarations echo `Name : record`; their synthetic internal
constructor is not part of the surface namespace.
Classes cannot be redefined. Type redefinition creates a fresh identity and
can install fresh instances for that identity. Failed instance, deriver, and
deriving declarations do not modify the persistent declaration environment,
and neither does a declaration whose splice fails part way through. Types are
printed with their class contexts. An expression's result shows its
[representation](classes.md#standard-classes) through available `Show`
evidence, so a String result is quoted; otherwise it prints `<value : T>` or
`<function>` without adding a Show constraint to the expression.

## Evaluation and commands

Effectful expressions run directly. Ordinary effectful declarations such as
`x = print 1` are rejected. Effectful function definitions are accepted and
execute only when explicitly applied.

An expression may perform IO and any number of `Fail` applications. The
prompt handles each `Fail` for that input alone: a failure ends the input and
prints its value and type instead of a result, through `Show` when the type has
an instance and as `<value : T>` otherwise:

```text
> Client.run { Client.getText "relative" }
Unhandled Error: InvalidUrl "relative" : Error
```

In a terminal the line is red, unless `NO_COLOR` is set. Any other effect the
expression performs, such as `Http` or `State Int`, has no handler at the
prompt; checking reports `UNHANDLED EFFECT` and nothing runs. Run such code
inside its handler, as in `Client.run { … }`.

Supported commands are:

```text
:type <expr>   show a type without evaluating
:help          show command help
:quit, :q      leave the REPL (Ctrl-D also exits)
```

Ctrl-C clears a partial prompt input. During evaluation it cancels the host
context and wakes cancellation-aware Async operations. Async runners finish
language cleanup and drain children before returning an outcome; root and child
CPU loops must cooperate with cancellation. Accepted definitions remain
installed. A blocked console `readLine` is interrupted without assigning the
next line to the old expression. Native operations and task CPU loops that do
not cooperate can delay the prompt; source task cancellation does not insert
compiler polling into those loops.

`:reload` is not implemented; a module edited on disk after it was imported is
not re-read in the same session.

## Line editing

When both stdin and stdout are a terminal, the REPL edits input with an
Emacs-style line editor: cursor and word movement, kill and yank, Up/Down
through history, and Ctrl-R reverse search. Submitted prompt lines, one entry
per physical line and without an immediate repeat, are kept in
`~/.fango_history` across sessions; lines a program reads are not. A `| `
continuation line starts at the previous line's indentation, and a line left
holding only that indentation submits like a blank one.

A program's `readLine` is edited too. The editor redraws the program's partial
output line (`Name? `) as that read's prompt unless it is long or holds control
characters such as colour codes, in which case typing may overwrite it.
Ctrl-C there interrupts the evaluation, as it does elsewhere, and Ctrl-D ends
only that read: the program sees end of input and the session continues. Ctrl-D
on an empty prompt line exits as before.

Piped or redirected input reads plain lines, with the prompt printed to stdout,
and keeps no history.
