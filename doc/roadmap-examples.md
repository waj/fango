# Roadmap: example programs

Unfinished programs that drive language/library growth. Implemented source
examples live in [examples](../examples/), with expected outputs and other
fixture files in [examples/fixtures](../examples/fixtures/); their differential
tests live in [CLI tests](../cmd/fango/e2e_test.go).

## Data structures, files, and OS APIs

- **Conway's Game of Life:** choose a grid representation (Array, Dict, or nested
  lists), add required floored division/time/sleep support, and animate with IO.write.
- **Markov-chain generator:** Dict of List values, seeded Random, and large-input
  performance coverage against Go.

## Interpreters and solvers

- **Lisp interpreter:** reader, Dict environments, closures, special forms, and a
  REPL loop; use it to assess language ergonomics across these features.

## Systems and performance

- Add the unimplemented grep-lite performance comparison against Go.
- **Parallel downloader or multi-file word count:** a consumer for structured
  concurrency, cancellation, and network/process APIs. This follows the
  [Async roadmap](roadmap-async.md#implementation-stages): native readiness for
  overlapping downloads, or a parallel/mixed executor for CPU parallelism.

## Delivery requirements

Use real programs to select missing APIs rather than speculative library breadth.
Each example needs expected output and interpreter/compiler differential coverage;
interactive programs need scripted stdin, and filesystem/stateful programs need
isolated fixture data. Prefer Fango implementations, with natives justified by
semantics or measurements. Remove completed entries; do not retain feature-history
summaries here. Numeric helpers such as abs/min/max/conversions should follow an
actual consumer.
