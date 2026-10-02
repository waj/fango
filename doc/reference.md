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
| String, Char, Basics integer helpers | [Text and numeric helpers](reference/library-text.md) |
| Bytes | [Byte sequences](reference/library-bytes.md) |
| Reader, Writer, Bytes.Source, Bytes.Sink | [Buffered readers and writers](reference/library-readers.md) |
| Encoding, Text.Reader, Text.Writer, Text.Builder | [Encodings and text I/O](reference/library-text-io.md) |
| IO, File, Net | [Console, process, files, sockets, and structured errors](reference/library-io.md) |
| Http, Http.Server, Http.Route, Http.GZip | [HTTP/1.1 server and middleware](reference/library-http.md) |
| Fail, Failure, State, Random, Runtime.Local | [Effect APIs](reference/library-effects.md) |
| Runtime.Scope | [Cleanup scopes](reference/resources.md) |
| Async | [Tasks, handlers, cancellation, and channels](reference/library-async.md) |
| Runtime.Native | [Native Go sidecars](reference/native.md) |
| Json | [Streaming parsing and encoding](reference/library-json.md) |
| Meta, Derive | [Metaprogramming](reference/metaprogramming.md) |

### Streams and cursors

[Stream and Iterator](reference/library-streams.md) — explicit state, traversal, and composition.

## Native Go sidecars

[Native Go sidecars](reference/native.md) — Declarations, boundary types, FangoHost, and native workers.

## Experimental LLVM backend

[Experimental LLVM backend](reference/llvm.md) — backend selection, platform
support, and parallel C sidecars.

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

[REPL](reference/repl.md) — Prompt scope, imports, redefinition, rollback, and commands.
