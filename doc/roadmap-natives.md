# Roadmap: stdlib native code lives beside its module

This is a [roadmap](roadmap.md) proposal under iteration; nothing here is
implemented. When accepted and built, durable results move to
[the design](design.md) and [the reference](reference.md) and this file is
removed.

## Problem

A bundled module cannot implement its natives in its own `<Module>.native.go`.
`Random`'s PRNG (cell, LCG, entropy seeding) had to be implemented in
`runtime/fangort`, with `stdlib/Random.native.go` reduced to three delegator
lines, because:

- Compiled programs only ever see inline templates, and templates may
  reference only `fangort.*` (`internal/modules/modules.go`,
  `validateTemplate`).
- Bundled sidecars are explicitly not materialized into build directories
  (`cmd/fango/pipeline.go:98`, `if native.Bundled { continue }`); their only
  role today is interpreter-side support code, statically linked into the
  compiler binary.

Consequences: fangort accretes module-specific logic (PRNG, string
indexing) that belongs beside its module; every new stdlib native is a
**4-place change** (fangort func, `stdlib/*.native.go` delegator,
`internal/natives` Spec, template declaration); and the same fango
declaration means different things in bundled vs user modules.

## Proposal

Unify bundled natives around the **call-form sidecar mechanism user modules
already use**, and make bundled sidecars work in both backends. A stdlib
native becomes a 2-place change: the call-form declaration
(`swapSeed : Int -> Int` / `swapSeed = native`) and the exported Go function
in `stdlib/<Module>.native.go`.

Everything needed already half-exists:

- **Compiled**: codegen emits sidecar calls whenever `Template == nil`
  (`internal/codegen/gen.go:716`, `nativeAlias`/`nativeImportPath`); user
  sidecars are materialized as `native/<module>/native.go`. The change is to
  stop skipping bundled sidecars at materialization and run them through the
  same scalar-ABI validation as user sidecars.
- **Interpreter**: `stdlib/*.native.go` is already compiled into the fango
  binary as the Go package `github.com/waj/fango/stdlib` and the evaluator
  dispatches `NativeCall` registry-first (`internal/eval/eval.go:249`),
  erroring only on a miss. Registering the statically linked functions makes
  bundled call-form natives run interpreted — no dynamic loading involved, so
  the reason user sidecars are compiled-only does not apply to bundled ones.

### What keeps using templates

Templates stay for the two cases that genuinely need them:

1. **Basics scalar primitives** (`intAdd = native "$1 + $2"`, comparisons,
   `fdiv`, `append`): inlining to Go operators is what keeps generated
   arithmetic native-speed and constant-foldable; the runtime benchmarks
   gate this.
2. **Effect operations** (`IO.readLine`, `IO.write`): their interpreter
   implementations need the per-session `Runtime` (reader/writer), and their
   compiled forms are the native boundary default of `Perform`.

Everything else — pure, scalar-typed module natives — uses sidecars.

### Sidecar rules for bundled modules

Same as user sidecars: `package native`, Go-stdlib-only imports, closed
scalar ABI (`Int`/`Float`/`String`/`Bool`/Unit), bidirectional
declaration/function correspondence, exported name = capitalized declaration
name. Bundled files lose their current validation exemption
(`validateNatives`' early `if n.bundled { return errs }` narrows to the
template/infix checks).

Precondition: evict the interpreter-adapter code that currently squats in
stdlib sidecars and violates that ABI:

- `EvalBasics` (dispatches on the interpreter's `any` values) moves to
  `internal/natives` — it is interpreter-only code and that is its home.
- `IO.native.go`'s `PrintTo`/`ReadLineFrom`/`WriteTo` adapters likewise move
  to `internal/natives` (which can import fangort directly).
- The `String`/`Random` delegators disappear; their implementations move
  **out of fangort into their sidecars** (`StringLength`, `ByteAt` →
  `stdlib/String.native.go`; PRNG cell + `SwapSeed`/`NextInt`/`EntropySeed`
  → `stdlib/Random.native.go`). fangort shrinks back to genuinely shared
  runtime: Unit, Show/formatting, IO writers, the general-handler engine.

### Interpreter registration

Phase 1 (small): keep hand-written Specs in `internal/natives`, but they now
call the sidecar functions (`stdlib.SwapSeed(...)`) — the implementation
exists exactly once. The existing load-time check (`INVALID BUNDLED NATIVE`,
arity/effect agreement) extends to call-form bundled natives so a missing
registration is still caught at module load, not at first call.

Phase 2 (optional, kills the last hand-written table): a `go:generate` step
in `internal/natives` parses `stdlib/*.native.go`, and emits the typed
wrapper table from the exported signatures. Adding a stdlib native then
really is: declaration + Go function.

### State semantics

A bundled sidecar may hold private package state (Random's PRNG cell). Each
backend is its own process, so "one cell per process" is unchanged; the
sequences still match across backends because both run the same Go function.
In compiled programs the cell lives in the materialized `native/Random`
package; in the interpreter/REPL it lives in the statically linked stdlib
package and persists across prompts — same behavior as today's
fangort-hosted cell.

### Migration

1. Move interpreter adapters (`EvalBasics`, IO adapters) into
   `internal/natives`; delete delegator content from stdlib sidecars.
2. Materialize + validate bundled sidecars; convert `String.length`,
   `String.byteAt`, `Random.swapSeed`, `Random.nextInt`,
   `Random.entropySeed` from templates to call-form; move their bodies from
   fangort into the sidecars; drop the fangort copies.
3. Registry entries point at the sidecar functions (phase 1).
4. Docs: `doc/design.md` (bundled-native architecture), `doc/reference.md`
   (native sidecar section: bundled modules use the same mechanism and also
   run interpreted).

## Rejected alternative

Widen templates instead: allow `stdlib.X($1)` in templates and materialize
`stdlib/*.native.go` as one package into builds. Smaller diff, but it keeps
the template ceremony for every native, keeps implementations reachable only
through string templates, leaves user and bundled modules on different
mechanisms, and still needs the adapter eviction. The sidecar unification
makes bundled modules a strict superset of user modules (same mechanism,
plus interpreter execution via static linking), which is the more honest
architecture.

## Open questions

- Should bundled sidecars be allowed to import fangort? User sidecars
  cannot, and the materialized copy would need import-path rewriting
  (`github.com/waj/fango/runtime/fangort` → the build's `fangobuild/fangort`).
  Current lean: no — none of the candidates need it, and keeping the rule
  identical to user sidecars is simpler.
- Is phase 2 (generated registry) worth the build-step complexity now, or
  only once the stdlib grows past a handful of natives?
- Does the REPL need anything? Bundled sidecar state persisting across
  prompts matches today's behavior; per-session isolation would require a
  `Runtime`-style context for pure natives, which is a bigger semantic
  change and probably not wanted.
- Effect operations stay templates for now; if a future effect op wants a
  sidecar-sized implementation, does it get a third mechanism or do effect
  templates learn to call sidecar functions?

## Verification

The existing gates cover this fully: sidecar validation diagnostics get
fixture coverage; the interpreter/compiler differential suite proves both
backends run the same implementations (seeded Random sequences byte-equal);
`TestEmitDeterministicAndFormatted` covers the new materialized packages;
runtime benchmarks confirm Basics templates (untouched) keep their inlining.
