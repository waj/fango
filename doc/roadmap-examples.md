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
  seeded handler advances handler-local state while the system handler only
  obtains its initial seed from entropy), plus loop-by-recursion ergonomics.
- [x] **Word/line/byte count (`wc` clone) over stdin** — pipe any text through
  it. Forced `readLine : () ->{IO} Maybe IO.Line` with exact terminators,
  ASCII-whitespace `String.words`, `String.byteLength`,
  `List.foldl`, and nominal records with projection and functional update for
  carrying the three counters.
- [x] **Markdown-lite to HTML converter** — headings, emphasis, lists,
  paragraphs. Forced Unicode-scalar `Char`, substring and prefix operations,
  partial record and pinned-value patterns, and an immutable chunk builder.

## Tier 2 — data structures, files, and OS surface

- [x] **Todo CLI with persistence** — `todo add "buy milk"`, `todo list`,
  `todo done 2`. Forced command-line arguments, current-directory file
  read/write, exit codes, records for todo items, and a bundled `Json.Encode`
  deriver. Encoding is derived; the exact-schema decoder is hand-written.
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
