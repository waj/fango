# Fango language reference

Implemented language and library contracts. Read the topics relevant to the
change; [design](design.md) owns architecture and [roadmap](roadmap.md) owns
unfinished work. Use `rg -n '^#{1,3} ' doc/reference` to list topic headings.

## Setup and commands

[Setup and commands](reference/commands.md) — Setup, check, build, run, clean, fmt, [API documentation](reference/commands.md#api-documentation), and generated Go projects.

## Modules, imports, and source layout

[Modules, imports, and source layout](reference/modules.md) — Module identity, imports, exports, Prelude, and source roots; [lexical syntax and layout](reference/syntax.md).

## Bundled standard library

The bundled library is experimental and versioned with the compiler. It
documents itself: the comments in its sources are its API reference, which
[`fango doc --stdlib`](reference/commands.md#api-documentation) extracts and
editor hover shows. [Prelude](reference/modules.md#prelude-and-implicit-dependencies)
defines the default scope.

| Area | Modules |
| --- | --- |
| Classes, operators, and integer helpers | [Basics](../stdlib/Basics.fango) |
| Collections and results (list syntax: [Lists](reference/types.md#lists)) | [List](../stdlib/List.fango), [Range](../stdlib/Range.fango), [Maybe](../stdlib/Maybe.fango), [Tuple](../stdlib/Tuple.fango), [Dict](../stdlib/Dict.fango), [Result](../stdlib/Result.fango) |
| Streams and cursors | [Stream](../stdlib/Stream.fango), [Iterator](../stdlib/Iterator.fango) |
| Text | [String](../stdlib/String.fango), [Char](../stdlib/Char.fango), [Regex](../stdlib/Regex.fango), [Encoding](../stdlib/Encoding.fango), [Text.Builder](../stdlib/Text/Builder.fango) |
| Bytes and buffered IO | [Bytes](../stdlib/Bytes.fango), [Reader](../stdlib/Reader.fango), [Writer](../stdlib/Writer.fango), [Text.Reader](../stdlib/Text/Reader.fango), [Text.Writer](../stdlib/Text/Writer.fango) |
| Handles, console, process, files, and sockets | [IO](../stdlib/IO.fango), [Console](../stdlib/Console.fango), [Process](../stdlib/Process.fango), [File](../stdlib/File.fango), [Net](../stdlib/Net.fango) |
| URLs and HTTP | [Url](../stdlib/Url.fango), [Http](../stdlib/Http.fango), [Http.Wire](../stdlib/Http/Wire.fango), [Http.Server](../stdlib/Http/Server.fango), [Http.Server.Route](../stdlib/Http/Server/Route.fango), [Http.GZip](../stdlib/Http/GZip.fango), [Http.Client](../stdlib/Http/Client.fango) |
| Effects | [Fail](../stdlib/Fail.fango), [Failure](../stdlib/Failure.fango), [State](../stdlib/State.fango), [Random](../stdlib/Random.fango), [Runtime.Local](../stdlib/Runtime/Local.fango) |
| Cleanup scopes ([guide](reference/resources.md)) | [Runtime.Scope](../stdlib/Runtime/Scope.fango) |
| Tasks and channels ([handlers in tasks](reference/effects.md#handlers-in-tasks)) | [Async](../stdlib/Async.fango) |
| JSON | [Json](../stdlib/Json.fango), [Json.Field](../stdlib/Json/Field.fango), [Json.Pull](../stdlib/Json/Pull.fango) |
| Metaprogramming ([guide](reference/metaprogramming.md)) | [Meta](../stdlib/Meta.fango), [Derive](../stdlib/Derive.fango) |
| Native sidecars ([guide](reference/native.md)) | [Runtime.Native](../stdlib/Runtime/Native.fango) |
| REPL handler levels ([guide](reference/repl.md#handler-levels)) | [Runtime.Prompt](../stdlib/Runtime/Prompt.fango) |
| Default scope ([rules](reference/modules.md#prelude-and-implicit-dependencies)) | [Prelude](../stdlib/Prelude.fango) |

## Native Go sidecars

[Native Go sidecars](reference/native.md) — Declarations, boundary types, FangoHost, and native workers.

## Values and operators

[Values and operators](reference/syntax.md#values-and-operators) — Scalar literals, operators, precedence, and fixity.

### Declaring operators

[Operator declarations and fixity](reference/syntax.md#declaring-operators).

## Declarations, annotations, and functions

[Declarations, annotations, and functions](reference/functions.md) — Bindings, application, equations, inference, and visibility.

### Tail-call guarantee

[Eligibility and limitations](reference/functions.md#tail-call-guarantee).

## Type classes and instances

[Type classes and instances](reference/classes.md) — Default methods, constraints, instance selection, visibility, and defaulting.

## Algebraic data types and matching

[Algebraic data types and matching](reference/types.md) — Records, unions, lists, tuples, deriving, and patterns.

## Effectful function types

[Effectful function types](reference/effects.md) — Per-arrow rows, callback inclusion, and exact annotations.

## Effects and handlers

[Effects and handlers](reference/effects.md#effects-and-handlers) — Operations, aborts, stateful handlers, and resume discipline.

[Effectful closures](reference/effects.md#closures-and-handler-effects) — Effects remain visible in callable types.

## Cleanup scopes

[Cleanup scopes](reference/resources.md) — Scope API, release ordering, failure precedence, and resource lifetimes.

## Compile-time metaprogramming

[Compile-time metaprogramming](reference/metaprogramming.md) — Meta API, typed attributes, reflection, quotes, splices, and derivers.

### Derivers

[Deriver declarations and Meta builders](reference/metaprogramming.md#derivers).

## Entry points

[Entry points](reference/commands.md#entry-points) — Accepted main definitions and ambient IO.

## REPL

[REPL](reference/repl.md) — Prompt scope, imports, redefinition, rollback, commands, and line editing.
