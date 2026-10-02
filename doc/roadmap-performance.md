# Roadmap: generated-code performance

This document owns the next round of performance work found while closing the
typed JSON gap. The measured motivation is in the
[JSON baselines](design/json-performance-baselines.md#ascii-fast-paths-and-single-pass-spans);
the main [roadmap](roadmap.md#json-and-generated-code-performance) links here.
When a milestone lands, its durable contracts move to [design](design.md) and
[reference](reference.md) and its section is removed.

Every milestone keeps semantics unchanged and passes `make ci`. Each is timed
with alternating fresh-process pairs of the previous commit's binaries on the
10 MB typed JSON fixture, both backends, as in the baselines. A change that
helps one backend and costs the other states both numbers in its commit and
in the baselines.

## P1 — LLVM products without tags

**Problem.** The LLVM backend gives every ADT struct a `uint32_t tag`, and
every match, including record projection and update (`recordGet` and
`recordUpdate` in elaboration lower to a one-case `SwitchCtor`), switches on
it with a `fango_panic` default. A small scanner such as `Json.fastWhite`
becomes hundreds of IR instructions, which changes Clang's inlining decisions:
an ASCII whitespace loop that is smaller in source stopped being inlined at
its call sites and cost 11%, while `-inline-threshold=600` recovered the time.

**Change.** Mirror the Go backend's product classification
(`codegen/representation.go`, `productADT`/`productSwitch`):

- A single-constructor ADT has no `tag` member. Construction sets fields only,
  and `SwitchCtor` over it binds fields directly, with no switch and no
  default.
- An exhaustive `SwitchCtor` over a sum without a `Default` makes its last
  case the C++ `default`, as `taggedSwitch` does in Go, rather than emitting
  an unreachable panic. Core lint already proves coverage.
- Generated equality, show, and conversion helpers follow the same rules.

Nothing outside generated code reads the tag of a product (natives use
`member()`, and C sidecars see only scalars, strings, bytes, and opaque
handles), so this is not an ABI change. Update the LLVM design's
"Typed representations" section.

**Then retry** the ASCII-code whitespace loop in `Json.fastWhite`, which saved
about 7% on Go, and keep it only if LLVM no longer regresses.

**Acceptance.** An emission-shape test in `cmd/fango/llvm_test.go` (no
toolchain needed, like `TestLLVMUnchangedStateCommitsNothing`) asserts that a
record projection emits no tag switch. The LLVM differential suite passes.

## P2 — a text builder for escaped strings and collectors

**Problem.** Escaped JSON strings cost about 15% of Go decode time on the
fixture, around 110 ns per escape. Each escape builds a one-character string
and two list cells, and the end of the string reverses the list and joins it.
`Writer.collecting` and `Text.Writer.collecting`, and therefore
`Json.stringify`, use the same reversed-fragment pattern.

**Change.** Add an opaque, persistent `Text.Builder`:

```fango
empty : Builder
append : Builder -> String -> Builder
appendChar : Builder -> Char -> Builder
toString : Builder -> String
```

It is an ordinary immutable value: appending returns a new builder, and an
older builder still answers exactly its own text. The shared representation
(in `runtime/fangort` for the interpreter and Go, with a C++ counterpart for
LLVM) is a growable buffer shared by successive versions, plus each version's
length. An append writes in place only when the version it extends is the
newest one, claimed by an atomic compare-and-swap on the buffer's committed
length; otherwise it copies. Bytes below a version's length are never
rewritten, so the result is safe under tasks and never mutates text another
holder can observe. `toString` copies once.

JSON's string scanners carry a builder instead of a fragment list, appending
plain spans from the window and decoded escape characters. The two
collectors switch to it. The fragment-list helpers are removed once unused.

**Decision recorded here.** This adds one runtime type rather than a
JSON-specific unescape native, because parsing stays in Fango and three
consumers share the pattern. A cheaper constant-factor fix (literal strings
for common escapes, joining without the reverse) was considered but keeps
per-escape list cells.

**Acceptance.** Reference documentation for the module. Differential fixtures
cover persistence (appending twice to one builder, and appending to an older
version after a newer one) and a builder shared by concurrent tasks. Escape-
and collector-heavy JSON fixtures keep their output.

## P3 — smaller window and scan cursors

**Problem.** Every buffered scan returns its advanced cursor, usually inside a
`Maybe` or `Result`. `Text.Reader.Window` is an interface-backed buffer sum
plus an index, and `Json.Scan` adds line, column, and nesting allowance: six
words copied through every token's result.

**Change.** Measure two independent reductions and keep what pays:

- The scan cursor derives its column from a line-start position instead of
  updating the column for every token. Columns are byte-based, so the
  diagnostic is identical; only newlines touch line state.
- The window's buffer becomes a representation without the empty/buffered
  sum check, chosen so that `Window` does not grow. Zero-length bytes stand
  in for the empty buffer.

**Acceptance.** All JSON diagnostic fixtures report identical offsets, lines,
and columns, including across refills and with Latin-1 input.

## P4 — a Core inliner

**Problem.** Small helpers are not inlined: Go's inliner rejects most
generated functions, and Clang's decision depends on lowered size. The
earlier fast paths were hand-written workarounds for this.

**Change.** An automatic inliner in elaboration, before
`LintProgIn`, so the interpreter executes the result and the differential
suite referees it. There is no inlining pragma; one is added only if a
measured case needs it.

- **Candidates.** Non-recursive workers that are effect-free: an empty
  evidence row and no effect parameters, no `Perform`, `Handle`,
  `ResumeTail`, `Bracket`, `ControlExit`, or task nodes, Direct control, and
  no residual row (extending the purity test in `elaborate/specialize.go`).
  Recursion is decided on a Core call graph, so mutually recursive groups and
  lifted locals are excluded.
- **Cost.** Node count after simplification, with a discount for parameters
  the body immediately scrutinizes, so a function whose wrappers vanish at a
  call site that matches its result is inlined even above the base size.
  Wrapper bodies that only forward to another call are always inlined.
- **Simplification.** Beta reduction to strict `Let`, substitution of
  duplicable bindings, and case of a known constructor, generalizing the
  simplifier that scalar specialization already has. Inlined bodies are
  alpha-renamed and receive fresh capture variables, scope IDs, and type
  argument substitution, keeping every lint invariant. ABI summaries are
  recomputed.
- **Within a module first,** then across modules. Cross-module inlining makes
  a dependent's checked object contain its dependency's code, so each module
  exposes an unfolding set (the bodies of its candidates) with its own
  fingerprint, and the checked-object key and emission record of a dependent
  include the unfolding fingerprints of the dependencies it inlined from.
  Changing an inlinable body then invalidates only its inliners. Update the
  build-cache design, which currently promises that dependency bodies are
  withheld.

**Acceptance.** Lint, differential, and LLVM suites pass with the inliner
enabled. `DisableOptimizations` also disables it. A cache test changes an
inlined function's body and verifies that the dependent is rebuilt.

## P5 — static-argument specialization

**Problem.** Recursive functions that receive an unchanged function argument,
such as `scanListShort scanValue` with the element decoder or a predicate
passed through a scanning loop, call it indirectly on every iteration.

**Change.** When every self-call of a worker passes a parameter of function
type unchanged, and a call site supplies a known worker or a closed lambda,
clone the worker for that argument (owner-local, named like the scalar
variants) and redirect the call. The clone's body then calls the argument
directly, and P4 can inline it. Specialization is bounded per worker, so
distinct arguments cannot multiply clones without limit.

**Acceptance.** Fixtures for a specialized fold and a loop with an unchanged
predicate, a recursive call that changes the argument (not specialized), and
the clone bound.
