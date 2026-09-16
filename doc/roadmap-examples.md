# Roadmap: example programs

Unfinished programs that drive language/library growth. Implemented examples
and expected outputs live in [examples](../examples/); their differential tests
live in [CLI tests](../cmd/fango/e2e_test.go).

## Data structures, files, and OS APIs

- **Conway's Game of Life:** choose a grid representation (Array, Dict, or nested
  lists), add required floored division/time/sleep support, and animate with IO.write.
- **Markov-chain generator:** Dict of List values, seeded Random, and large-input
  performance coverage against Go.

## Interpreters and solvers

- **Sudoku solver:** parsing, grid representation, backtracking, early search exit,
  and deep-recursion behavior.
- **Lisp interpreter:** reader, Dict environments, closures, special forms, and a
  REPL loop; use it to assess language ergonomics across these features.

## Systems and performance

- Add the unimplemented grep-lite performance comparison against Go.
- **Parallel downloader or multi-file word count:** a consumer for structured
  concurrency, cancellation, and network/process APIs. This follows the
  [effects roadmap](roadmap-effects.md).

## Delivery requirements

Use real programs to select missing APIs rather than speculative library breadth.
Each example needs expected output and interpreter/compiler differential coverage;
interactive programs need scripted stdin, and filesystem/stateful programs need
isolated fixture data. Prefer Fango implementations, with natives justified by
semantics or measurements. Remove completed entries; do not retain feature-history
summaries here. Numeric helpers such as abs/min/max/conversions should follow an
actual consumer.
