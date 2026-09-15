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
- [x] **CSV expense report** — read a CSV named on the command line, aggregate
  by category, print an aligned table with totals. Forced a pure-fango ordered
  `Dict` (weight-balanced, `Ord`-keyed, pair-taking callbacks), which in turn
  forced tuples; truncated integer division as `Basics.quotientBy`, since
  `Basics` had `remainderBy` and `modBy` but no division at all; and
  `String.split`, `trim`, `padLeft`, and `padRight`. Money is exact integer
  cents, so float parsing and float precision formatting were **not** forced
  and remain unbuilt. Category order in the report comes free from the
  dictionary, which is why it is ordered rather than hashed.
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

- [x] **`grep`-lite** — pattern search across files named on the command
  line, correct exit codes. Forced the scoped `File` API over
  `Scope.bracket` (`withFile`, streaming `readLine`, `listDirectory`,
  `isDirectory`), typed `IO.Error` values with a platform-stable
  `describeError`, a bundled `Fail` effect, `String.contains`, and the
  native-boundary work behind them: opaque wrapper types erased only at the
  Go boundary and fallible natives returning Go errors. Search is a plain
  substring scan; the performance gate against Go remains unbuilt.
- [ ] **Parallel downloader or multi-file word count** — the concrete
  consumer for structured concurrency (task contexts and cancellation) and
  for network/process effects. Deliberately last.

## Cross-cutting expectations

Nearly every example immediately wants tuples or records to return two
things. Records were forced first, in Tier 1; tuples followed when designing
`Dict`, whose `toList` and fold callbacks would otherwise each need a named
record. Both are now available, and the choice is ordinary style: names when
the fields deserve them, positions when they do not.

Math basics (`abs`, `min`/`max`, conversions such as `toFloat`/`floor`) and
integer division will surface early as well.
