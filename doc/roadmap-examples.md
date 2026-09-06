# fango example programs

This is the pipeline of example programs that drives language and standard
library growth. Each entry is a real, useful program rather than a feature
showcase, chosen because building it forces a distinct cluster of missing
capabilities. Per [the roadmap](roadmap.md), stdlib APIs are added only when
an example demands them, implemented in fango where possible with natives only
where semantics require.

Conventions: each finished example lands in `examples/` with a `.expected`
output file (the mandelbrot pattern); interactive programs use scripted stdin
transcripts; every example gets interpreter/compiler differential coverage.

Mark an example `[x]` when it runs end-to-end and its coverage is in place.

## Tier 1 — console and text

Forces the core stdlib: strings, `Maybe`, list combinators.

- [x] **Number-guessing game** — interactive higher/lower loop. Forced
  `Maybe`, `String.toInt`, and a `Random` effect with swappable
  seeded/system handlers (the game performs `Random.int` opaquely; the
  handlers advance a native PRNG cell, since a pure-fango state handler
  awaits parameterized handler state), plus loop-by-recursion ergonomics.
- [ ] **Word/line/char count (`wc` clone) over stdin** — pipe any text through
  it. Forces an end-of-input story for `readLine` (likely `Maybe String`),
  `String.words`/`split`/`length`, `List.foldl`/`map`/`filter`, and immediate
  tuple/record pressure for carrying the three counters.
- [ ] **Markdown-lite to HTML converter** — headings, emphasis, lists,
  paragraphs. Forces substring and prefix operations, character-level string
  processing, and efficient string building.

## Tier 2 — data structures, files, and OS surface

- [ ] **Todo CLI with persistence** — `todo add "buy milk"`, `todo list`,
  `todo done 2`. Forces command-line arguments, file read/write, exit codes,
  a serialize/parse round trip, and records for the todo item.
- [ ] **CSV expense report** — read a CSV, aggregate by category, print an
  aligned table with totals. Forces file IO, `Dict` insert/update/fold, and
  number formatting (`String.padLeft`, float precision).
- [ ] **Conway's Game of Life** — animated in the terminal. Forces the grid
  representation decision (`Array` vs `Dict` vs list-of-lists), integer
  division and modulo, `sleep`/time, and ANSI control via `IO.write`.
- [ ] **Markov-chain text generator** — feed it a book, get plausible
  nonsense. Forces `Dict` with `List` values, reuses `Random`, and provides
  large-input performance and benchmark fodder against Go.

## Tier 3 — interpreters and solvers

Forces the error-handling story.

- [ ] **Calculator REPL with variables** — precedence, parentheses,
  `x = 3 * (2 + y)`. Forces recursive-descent parsing and positioned errors,
  and is the motivating first use case for **aborting handlers** the roadmap
  waits on: a `Fail` effect whose handler does not resume.
- [ ] **Sudoku solver** — read a puzzle, solve by backtracking, print the
  grid. Forces `Maybe`-driven search, grid parsing, and early exit from
  search (more aborting-handler or `Result` pressure); deep recursion also
  probes stack behavior.
- [ ] **Lisp interpreter** — reader, `Dict` environments, closures, special
  forms, a REPL loop. The capstone: everything above at once, and the best
  "is this language pleasant to write?" stress test.

## Tier 4 — systems programs

Forces performance work and concurrency.

- [ ] **`grep`-lite** — pattern search across files named on the command
  line, correct exit codes. Forces multi-file IO, directory listing, and
  efficient string search; a performance gate against Go.
- [ ] **Parallel downloader or multi-file word count** — the concrete
  consumer for structured concurrency (nursery, channels, cancellation) and
  for network/process effects. Deliberately last.

## Cross-cutting expectations

Nearly every example immediately wants tuples or records to return two
things, so expect the records decision to be forced within Tier 1. Math
basics (`abs`, `min`/`max`, conversions such as `toFloat`/`floor`) and
integer division will surface early as well.
