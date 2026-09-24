# Fango language reference

Implemented language and library contracts. Read the topics relevant to the
change; [design](design.md) owns architecture and [roadmap](roadmap.md) owns
unfinished work. Use `rg -n '^#{1,3} ' doc/reference` to list topic headings.

## Setup and commands

[Setup and commands](reference/commands.md) — Setup, check, build, run, clean, fmt, and generated Go projects.

## Modules, imports, and source layout

[Modules, imports, and source layout](reference/modules.md) — Module identity, imports, exports, Prelude, and source roots; [lexical syntax and layout](reference/syntax.md).

## Bundled standard library

The bundled library is experimental and versioned with the compiler.
[Prelude](reference/modules.md#prelude-and-implicit-dependencies) defines the default scope.

| API | Reference |
| --- | --- |
| List, Range, Maybe, Tuple, Dict, Result | [Collections](reference/library-collections.md) |
| String, Basics integer helpers | [Text and numeric helpers](reference/library-text.md) |
| Bytes | [Byte sequences](reference/library-bytes.md) |
| Reader, Writer, Bytes.Source, Bytes.Sink | [Buffered readers and writers](reference/library-readers.md) |
| IO, File, Net | [Console, process, files, sockets, and structured errors](reference/library-io.md) |
| Fail, Failure, State, Random | [Effect APIs](reference/library-effects.md) |
| Runtime.Scope | [Cleanup scopes](reference/resources.md) |
| Runtime.Coroutine | [Scoped typed exchange and ownership](reference/library-coroutines.md) |
| Runtime.Completion | [Detached typed results and abort replay](reference/library-completion.md) |
| Runtime.Work | [Scoped work packages](reference/library-work.md) |
| Runtime.Cell | [Scope-owned write-once cells](reference/library-cells.md) |
| Runtime.Service | [Shared contexts and invocation authority](reference/library-services.md) |
| Async, Async.Cooperative, Runtime.Async.Cooperative | [Cooperative tasks and internal driver](reference/library-async-cooperative.md) |
| Runtime.NativeRequest | [Bounded native retention and driver callbacks](reference/library-native-requests.md) |
| Runtime.Native | [Native Go sidecars](reference/native.md) |
| Json | [Encoding and string tokens](reference/library-json.md) |
| Meta, Derive | [Metaprogramming](reference/metaprogramming.md) |

### Streams and cursors

[Stream and Iterator](reference/library-streams.md) — production, traversal, and ownership.

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

[Type classes and instances](reference/classes.md) — Constraints, instance selection, visibility, and defaulting.

## Algebraic data types and matching

[Algebraic data types and matching](reference/types.md) — Records, unions, lists, tuples, deriving, and patterns.

## Effectful function types

[Effectful function types](reference/effects.md) — Per-arrow rows, callback inclusion, and exact annotations.

## Effects and handlers

[Effects and handlers](reference/effects.md#effects-and-handlers) — Operations, aborts, stateful handlers, and resume discipline.

[Binding a closure to a handler activation](reference/effects.md#binding-a-closure-to-a-handler-activation) — Addressing one activation instead of the innermost handler.

## Cleanup scopes

[Cleanup scopes](reference/resources.md) — Scope API, release ordering, failure precedence, and resource lifetimes.

## Compile-time metaprogramming

[Compile-time metaprogramming](reference/metaprogramming.md) — Meta API, reflection, quotes, splices, and derivers.

### Derivers

[Deriver declarations and Meta builders](reference/metaprogramming.md#derivers).

## Entry points

[Entry points](reference/commands.md#entry-points) — Accepted main definitions and ambient IO.

## REPL

[REPL](reference/repl.md) — Prompt scope, imports, redefinition, rollback, and commands.
