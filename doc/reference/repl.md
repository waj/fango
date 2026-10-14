# REPL

Persistent prompt scope, imports, transactions, redefinition, handler levels, commands, and line editing.

[Reference index](../reference.md).

`fango repl [--debug] [dir]` evaluates expressions, installs
value/function/type/effect/class/instance/deriver declarations, imports
modules, and accepts multiline layout-sensitive input. The prompt starts in
the same scope `Prelude` gives a module, and, since a later prompt may use
them, it also resolves the modules surface syntax desugars into — so `[1, 2]`,
`(1, 2)`, and `deriving` work without putting `List`, `Tuple`, or `Derive` in
scope. Names resolve exactly as they do in a module: an unqualified name must
be exposed by the prelude, an import, or a prompt declaration, and a qualifier
must be an imported module's name or alias, so `Dict.empty` is an
`UNKNOWN QUALIFIER` until `import Dict`, while `IO.write` works because the
prelude imports `IO`. An input runs under the
[default handlers](commands.md#default-handlers) of the effects it performs
that no handler level grants, as a program's entry runs `main`: `print` uses
the session's standard streams and `Random.int` draws from system entropy.
Definitions echo
their inferred types; expressions print a value and type. Errors do not end the
session. Redefinition is allowed at the prompt, while existing memoized values
and closures retain earlier bindings.

The opt-in [debugger](debugging.md) adds breakpoints, stepping, and scoped
inspection with a separate prompt while execution is paused.

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
interactive session shows `loading M` on a single line that is overwritten as
other modules load. After the whole input succeeds, that line becomes
`loaded M (and N other modules)`, counting only newly loaded dependencies;
the parenthetical is omitted when there are none, and one dependency uses
`and 1 other module`. An input with several imports names its newly loaded
explicit imports once, in input order, as in `loaded Foo, Bar (and 3 other modules)`.
Aliases do not change the module names in this summary. Failed imports clear
the progress line before printing diagnostics and print no success summary.
Piped or redirected sessions echo `loaded M` for each newly loaded module,
dependencies included, in dependency order. A module already loaded echoes
nothing. The imported module's instances and derivers become usable at
the prompt, and its sidecar, if any, runs in the session's native worker.

Prompt imports are cumulative. Importing a module again adds the names its
new exposing list selects, and repeating an alias for the same module is
accepted; binding the alias to a different module is a `DUPLICATE IMPORT
ALIAS`, as it would be in a file. An import is all-or-nothing: if any of its
modules fails to load, check, or expose a requested name (`UNKNOWN IMPORT`),
the session keeps neither the modules nor the names, and the same import can
be retried after the file is fixed.

An import that would start or replace the native worker is rejected while a
handler level is active. End the levels with `:end`, then repeat the import.

## Reloading modules

`:reload` re-reads imported local modules and their dependencies from the
session's source root. It discovers new dependencies and removes dependencies
that are no longer reachable. Bundled source stays fixed for the session.
Reload prepares the complete new module environment before installing it:
syntax, resolution, type, staging, and compatibility errors leave the previous
session available. Ctrl-C cancels preparation at a stage boundary, also leaving
the session unchanged. Successfully checked cache artifacts can survive a
failed reload and be reused on retry.

New expressions use the updated modules. Prompt declarations keep their
earlier module bindings, including private helpers and concrete dictionaries;
memoized values stay memoized and effectful declarations never run again.
Redefine a prompt declaration to use the new code. For example, after changing
`Maths.adjust` in its source file:

```text
> f x = Maths.adjust x
> :reload
reloaded Maths
retained earlier module bindings: f
```

`Maths.adjust 10` now calls the updated function; `f 10` keeps its earlier
implementation. Ordinary polymorphic parameters still receive evidence at
their call site. Reload lists affected retained prompt definitions so this
difference is visible. Retained generations remain in memory until the
session exits.

Unchanged type, effect, and class contracts keep their nominal identities.
Changing a declaration's contract creates a new identity; existing values
cannot be passed to functions expecting the new type. Retained type/effect
names are annotated with `before reload` when their identities change, and
retired class names carry a generation suffix. An unrelated function-body edit
does not change a type's identity. Prompt-owned declarations retain theirs.

Import aliases and cumulative exposing lists survive reload. A removed public
name is removed from saved explicit exposing selections and reported as
`removed imported names: M.name`; existing definitions can still retain it.
Wildcard selections follow the new interface. Other import errors, including
collisions with prompt declarations, reject the reload. Fixities are rebuilt
from current modules and accepted prompt fixity declarations; earlier prompt
definitions keep their original grouping.

End every `use` handler level with `:end` before reloading; otherwise the command
reports `RELOAD WITH ACTIVE HANDLERS`. Reload keeps the native worker and its
heap. Changed or additional sidecars, or incompatible native interfaces, report
`RELOAD REQUIRES RESTART`; restart the REPL to adopt those changes. Sidecars
still needed by retained code stay in the worker when their module leaves the
current graph.

Success prints `reloaded M, N` for changed, added, or removed local modules,
followed by any removed-name and retained-binding reports. Unchanged inputs
print `nothing to reload`. Failure prints diagnostics and
`reload failed; session unchanged`. `:reload` accepts no arguments.

## Declarations and redefinition

An effect header opens continuation input so indented operations can follow.
A blank line, a new top-level input, or EOF submits an operation-free effect.


A prompt declaration may redefine a name the prompt itself declared, but not
one an import or the prelude exposes: that is the `UNQUALIFIED COLLISION` it
would be in a module. Qualified access to the exposed name stays available.
Imports see only a module's public interface; a private name is a `PRIVATE
OR UNKNOWN NAME` under any qualifier.
A name declared at the prompt does not reach an imported module: a module
checked after `a = 1` may still name a parameter or local `a` without a
`SHADOWING` error.
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

Effectful expressions run directly, and so does a value declaration whose
body performs effects: `x = print 1` runs its body once, when entered, under
the rules an expression gets, then echoes `x : ()` and binds the result. A
later use of `x` does not run the body again, and neither does ending a level
the body used; redefining `x` does. A failure, a runtime error, or an
interruption leaves the name as it was. A destructuring declaration evaluates
its subject the same way and projects the names from it. Effectful function
definitions execute only when explicitly applied, and a class default is
checked by a module's rules.

The value lives in the evaluator that computed it. An `import` that starts or
rebuilds the session's native worker leaves it behind, and using it there is a
runtime error: `` `x` holds the result of an effectful declaration that ran
before the native worker started or was rebuilt; define it again ``.

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
inside its handler, as in `Client.run { … }`, or install the handler as a
[level](#handler-levels).

Supported commands are:

```text
:type <expr>   show a type without evaluating
:reload        refresh imported local modules atomically
:uses          list the installed handler levels
:end           end the innermost handler level (Ctrl-D also does)
:help          show command help
:quit, :q      end every level and leave the REPL
```

Ctrl-C clears a partial prompt input. During evaluation it cancels the host
context and wakes cancellation-aware Async operations. Async runners finish
language cleanup and drain children before returning an outcome; root and child
CPU loops must cooperate with cancellation. Accepted definitions remain
installed. A blocked console `readLine` is interrupted without assigning the
next line to the old expression. Native operations and task CPU loops that do
not cooperate can delay the prompt; source task cancellation does not insert
compiler polling into those loops.

## Handler levels

A [`use` item](syntax.md#use-items) typed at the prompt installs a handler
level: the rest of the session is the item's callback, so every later input
runs inside the head's handlers until the level ends. The prompt shows how many
levels are installed:

```text
> import Http.Client as Client
> use Client.run
1> use Client.configure { c -> { c | readTimeoutMs = 2000 } }
2> Client.getText "https://example.com/status"
"ok" : String
2> :end
1> :end
>
```

The head runs once, so what it holds lasts for the level: `use Client.run`
keeps its connection pool across inputs, and `use State.run 0` its state. An
input may perform what any level's callback may, besides IO and `Fail`; when
several levels grant the same effect, one whose type arguments already agree is
used, and otherwise the innermost. A level takes precedence over an effect's
default handler. `:uses` lists each level's item, the effects
its callback may perform, and its binders.

`use patterns <- head` binds the callback's parameters for the inputs that
follow, echoing their types. A scoped runner may head a level: its scoped
values stay usable until the level ends.

```text
> use reader <- Reader.withBytes (Bytes.fromString "hello world")
reader : Reader {local scope}
1> Bytes.toStringLossy (Reader.readUpTo reader 5)
"hello" : String
```

A value definition that uses a level's binders, directly or through another
such definition, belongs to that level and goes away with it; a name it
replaced comes back. A definition that only performs what a level grants, such
as `n = State.get()` under `use State.run 0`, ran once when entered and keeps
its value after the level ends. Other declarations are unaffected by levels,
and a type, class, instance, or effect declaration may not use a level's
binders.

`:end` or Ctrl-D ends the innermost level: its callback returns, the head
finishes, and its result prints unless it is Unit. Ctrl-D with no level left
leaves the session; `:quit` and the end of piped input end every level first,
innermost first. Ending a level runs the head's cleanup, such as closing
`Client.run`'s connections.

Each input handles its own failures, so a failing input leaves the levels in
place, and Ctrl-C stops only the running input. An abort that escapes an input
toward an installed handler ends the levels it passes through; the handler's
result prints with how many levels ended:

```text
2> stop ()
Nothing : Maybe () (left 2 levels)
>
```

A head that returns without calling its callback installs nothing; its result
prints like an expression's.

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
