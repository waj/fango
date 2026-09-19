# Roadmap: persistent per-module compilation cache

Required work for independently reusable parsed units, checked module objects,
and emitted Go packages shared by batch commands and fresh REPL sessions.
Implemented project-cache behavior belongs in
[pipeline](design/pipeline.md#pipeline) and
[backend](design/backend.md#module-emission-and-build-cache); this document
owns the remaining architecture, milestone dependencies, and acceptance gates.

[Roadmap index](roadmap.md). Related work:
[module distribution](roadmap-modules.md#unembedding-the-bundled-sources),
[language server](roadmap-tooling.md#language-server), and
[REPL hardening](roadmap-tooling.md#repl-hardening).

## Goal and decisions

An unchanged module with unchanged semantic inputs must skip parsing,
resolution, inference, elaboration, Core lint, machine lowering, and Go
emission. Dependency implementation edits must preserve importer hits when
the dependency's semantic and ABI contracts are unchanged and the importer
does not execute the changed implementation during compilation.

Two decisions govern the implementation:

- Track compile-time dependencies separately. Ordinary exported functions can
  run inside splices, so their bodies affect compile-time consumers without
  automatically invalidating runtime-only consumers.
- Replace the whole-project shortcut once module caching is complete. Every
  invocation discovers and validates the current graph using cached parsed
  metadata; one module-artifact pipeline owns compilation validity.

No syntax changes, public cache flags, automatic eviction, source-distribution
changes, or REPL reload feature are included. Cache behavior stays transparent
and quiet. Failed, corrupt, incompatible, or unwritable entries are misses.

## Target architecture and invariants

### Shared compilation session

Extract the batch pipeline into `internal/check`, following the language-server
roadmap's proposed location. It owns graph preparation, checker/staging state,
module-object installation, and diagnostic results. The CLI and REPL become
clients; diagnostic rendering remains outside this package.

The session accepts source providers, a cache store, and optional internal test
instrumentation. It supports:

- Preparing a complete entry graph or a pending REPL import increment.
- Discovering sources, native sidecars, implicit dependencies, and Prelude
  dependencies.
- Establishing deterministic dependency order and the effective graph-wide
  fixity table before checking.
- Checking or installing one module at a time.
- Returning module objects, manifests, runtime Core, and link information.
- Preparing and committing imports transactionally.

Existing tests that need a merged `core.Prog` retain an assembly adapter.
Assembly concatenates validated objects in deterministic order; it does not
recheck or elaborate them. Diagnostic accumulation across failed stages remains
separate language-server work.

### Artifact layers

Use explicit JSON DTOs with independent artifact-kind/schema tags. Keep
artifacts beneath `.fango/cache/v1/<compiler-fingerprint>/`, separated by
artifact kind. Hash canonical encoded inputs with SHA-256.

| Artifact | Contents | Lookup inputs |
| --- | --- | --- |
| `ParsedUnit` | Unresolved AST, declaration/header/import/export metadata, implicit dependencies, fixities | Compiler/schema identity and exact source hash |
| `ModuleObject` | Installable checker state, resolver interface, owned runtime and stage Core, templates, native metadata, semantic and ABI summaries | Parsed-unit identity, module identity, entry/dependency role, sidecar presence/hash, effective fixity hash, ordered dependency semantic fingerprints, recorded stage dependencies |
| `Emission` | Generated Go bytes for one owner | Module-object hash, imported ABI/link fingerprints, entry/dependency role, print-main mode |

Named module identity and source-root-relative entry identity distinguish
role-sensitive objects. Parsed artifacts remain reusable across filenames by
binding their source-file slot during decoding.

Keep old content-addressed variants until cleaning. Mutable lookup indexes may
point to immutable objects, but must never be authoritative for validity.
Missing or damaged indexes cause misses. Compiler/schema changes select a cold
namespace; no migration of old artifacts is required.

`check` creates parsed and checked artifacts without emitting Go. A later build
reuses those objects. Build/run create the same checked artifacts, allowing
subsequent checks to reuse them.

### Three distinct fingerprints

**Semantic interface:** exported schemes; nominal representations and schemas
needed for inference, variance, reflection, or ABI; effects, operations, classes
and methods; instance heads, constraints and selection metadata; capture
contracts; native declarations and intrinsic identities; deriver availability;
foreign-type representation dependencies; and transitively visible
instances/derivers.

Include compiler optimization facts that affect callers, such as native
forwarding and identity-method recognition. An opaque source export does not
justify omitting representation information the compiler actually uses.

**ABI/link interface:** worker arity, type/evidence/row parameter order, Unit
erasure, Direct/Exit/Machine families, callback invocation behavior,
passive-factory classification, generated specialization symbols, nominal
representation, and required package/native links.

**Stage implementation:** executable Core and templates usable during
compilation, including ordinary exported functions, private helpers,
dictionaries, and transitive references. Exclude source positions and incidental
allocation identities.

Ordinary runtime body edits preserve importer hits only when semantic and ABI
summaries remain unchanged and those bodies were not compile-time dependencies.
A changed capture-flow contract is a semantic change even if the public type
is unchanged. Source text, comments, positions, and native Go implementation
bytes affect the owner's artifact inputs but do not themselves enter the
downstream semantic fingerprint.

### Serialization and identity

- Encode every AST, type, Core, pattern, decision-tree, template, and
  capture-contract variant explicitly. Unknown tags or missing required fields
  are misses.
- Use canonical nominal IDs `(kind, qualified name)`, with reserved identities
  for builtins and stable identities for generated dictionary types.
- On installation, intern nominal IDs into the current session and allocate
  fresh type variables, capture variables, scopes, resumes, and template
  indices.
- Preserve graph sharing with reference tables and two-pass decoding, including
  recursive types and template holes that refer to AST nodes.
- Keep identity numbering separate from ABI parameter ordering. Remapping must
  not reorder positional arguments; canonical set fields must remain valid
  after remapping.
- Encode floating-point values by IEEE bits so NaN, infinities, and negative
  zero round-trip.
- Sort maps and mathematical sets for hashing. Preserve declaration,
  constructor, field, parameter, and evidence order where semantically
  significant.
- Store local spans as validated byte offsets attached to the current source
  file. Reference foreign provenance through the defining module's current
  source map rather than copying stale dependency offsets.
- Do not serialize checker callbacks, evaluator cells, native processes, or
  whole Go implementation structs.

The cache is disposable. Validate envelope identity, payload hashes, reference
bounds, and required structure before installation. Decode into temporary state
and publish only after validation succeeds. Integrity validation on load is
distinct from rerunning semantic Core lint on an already validated object.

## Required milestones

Implement the remaining milestones in dependency order. Each has an
independently reviewable result and acceptance gate. The implemented in-memory
module boundary is described in
[pipeline design](design/pipeline.md#pipeline), including its typed persistent
module-object cache and stage-dependency validation; the owner-scoped backend
and its emission cache are described in
[backend design](design/backend.md#module-emission-and-build-cache). Do not
substitute whole-project cache hits for any milestone's module-level acceptance
tests.

### M7: integrate fresh REPL sessions and transactional imports

Builds on the persistent checked-module and module-backend boundaries.
Starting points are REPL bootstrap, `importInput`, `install`, evaluator
definition installation, and native-worker replacement.

Required work:

- Bootstrap Prelude and syntax roots through the shared session and
  dependency-role artifacts.
- Prepare all imports in one prompt input before committing graph, resolver,
  checker, staging, runtime definitions, or native-module changes.
- Keep already imported modules frozen for the session, matching existing
  behavior. A fresh session rereads current sources.
- Use the effective fixity table of each prepared import increment. Previously
  accepted prompt declarations retain their existing interpretation.
- Preserve module-value generalization independently of prompt-value
  monomorphism.
- Prepare evaluator definitions and native-worker configuration before commit.
  Failed multi-import prompts must not leave earlier imports installed or
  close the previous worker.
- Keep successful immutable disk artifacts after a failed prompt transaction.
- Preserve `loaded M` messages and dependency order on hits.

Acceptance: fresh cached sessions match cold transcripts, including staging,
deriving, resource/capture checks, imported `main`, native sidecars, and
redefinition generations. Test failure after an earlier import succeeded within
the same prompt, unknown exposed names, failed splices, and native-worker
preparation failure. Retrying after correction must behave like a clean
session. Count imported-module work separately from parsing/checking/evaluating
the prompt's own input.

### M8: remove the project shortcut and complete the public contract

Depends on M7.

Required work:

- Remove whole-project success/emission bypasses from CLI entry points. All
  commands use graph discovery and the shared module-artifact pipeline.
- Leave legacy artifacts ignored until cleaning; no migration is required.
- Keep any reference implementation needed for differential testing isolated
  from production command paths.
- Update design/reference explanations for pipeline boundaries, semantic
  versus stage invalidation, backend summaries, staging installation, REPL
  transactions, cache location, fallback, and cleaning.
- Update the distribution roadmap to reuse the new codec for future
  precompiled-library artifacts. Shipping those artifacts, external source
  roots, and package distribution remain unfinished.
- Keep the language-server roadmap using the shared check session. Diagnostic
  accumulation remains a separate feature.
- Remove completed cache milestones after their durable contracts are
  documented.

Acceptance: every required behavior is demonstrated through the module pipeline
with the old shortcut unavailable. No test may pass only because it reused a
whole-project success record.

## Verification and release gates

Use deterministic stage counts, fresh sessions, and temporary cache roots;
elapsed-time thresholds are not acceptance criteria. Graph discovery, artifact
decoding/integrity checks, and interface compatibility checks remain necessary
on hits and must be measured separately from compiler stages.

| Change or scenario | Required result |
| --- | --- |
| Identical second check/build | Zero module parse, resolve, check, elaborate, lint, lower, or emit work as applicable |
| Dependency implementation edit with unchanged contracts | Rebuild owner; runtime-only importers remain checked/emission hits |
| Dependency comment edit | Reparse/rebuild owner as required; importer semantics remain reusable and diagnostic locations stay current |
| Exported scheme, schema, instance, capture, or ABI change | Invalidate affected dependency consumers |
| Stage implementation or deriver change | Invalidate compile-time consumers, including transitive ones |
| Graph fixity change | Reuse parsed artifacts; checked artifacts use the new complete table |
| Native-sidecar body edit | Revalidate owner and materialize native package; preserve importers when interface/ABI is unchanged |
| New import graph combining cached modules | Repeat graph/path/fixity/declaration compatibility checks |
| Missing emission artifact | Reuse checked object; run only owner backend work |
| Corrupt/incompatible/unwritable cache | Compile normally without cache diagnostics |
| Failed REPL import | Restore session state; retain valid immutable disk artifacts |
| Multiple entries and role changes | Share dependency artifacts; distinguish entry-role behavior |
| Clean | Remove project-local and precisely identified fallback artifacts; preserve exported projects |

Codec tests must additionally cover deterministic bytes under varied map
insertion and allocation orders, pointer sharing, recursive references, all
node tags, float bit patterns, invalid offsets/references, foreign-source
relocation, and template-index remapping.

Run cold-versus-cached comparisons for diagnostics, Core dumps, generated Go,
executable output, and REPL transcripts. Include all-hit, all-miss, and mixed
graphs, plus concurrent writers and separate compiler processes. Verify that a
different compiler fingerprint selects a cold namespace and that reverting a
source change can reuse retained content-addressed variants.

Run focused tests at each milestone. Before final completion, run
`make test-short`, `make test`, `make ci`, and `go vet ./benchmarks`. Preserve
Core lint and interpreter/compiler differential coverage. Do not run timing
benchmarks during ordinary development. The
[verification design](design/verification.md) owns the repository-wide gates.

## Documentation maintenance

This roadmap owns only unfinished work and its acceptance criteria. Promote
implemented boundaries and invariants into the relevant design topics as each
milestone lands; promote behavior and diagnostics into reference topics.
Remove completed milestones instead of retaining an implementation diary.

Keep the roadmap index navigational. Link module distribution, tooling, and
REPL roadmaps here instead of maintaining competing plans. Shipping
precompiled library artifacts and implementing transactional REPL reload remain
separate work even after all milestones here are complete.
