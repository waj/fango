# Roadmap: modules, distribution, and native values

Open source-distribution and foreign-value designs. Current loading and worker
contracts are in [pipeline](design/pipeline.md) and [backend](design/backend.md).

## Unembedding the bundled sources

Bundled Fango, sidecars, and Go runtime support are embedded in the compiler.
Manual edits require rebuilding. Successful repeated project commands reuse a
compiler-fingerprinted project artifact, so an unchanged invocation does not
recheck the bundled Prelude or rerun its derivers. Shipping prechecked bundled
modules independently of a prior project compilation remains open; it must not
silently accept compiler/library skew.

Embedding currently guarantees agreement with canonical Meta symbols/classes and
the bundled-native registry. Removing it needs a version/skew diagnostic and a
review of RESERVED MODULE protection. Prelude is editable only in the compiler's
copy; a project's ability to supply one is part of the same source-root decision.

Open choices:

- Files beside the binary, serialized checked interfaces/Core, or source for
  development plus a precompiled distribution artifact. Reuse the implemented
  checked-module codec and its installation boundary
  ([pipeline](design/pipeline.md#pipeline)) rather than designing a second
  artifact format: a precompiled library is the same object, published by a
  producer instead of a local build, and it needs a durable identity in place
  of the compiler-executable fingerprint, a published source manifest, and a
  compatibility rule for a consumer built from a different compiler. Shipping
  those artifacts remains separate from project-local caching.
- Whether Go runtime support remains embedded: generated projects/workers still
  need its source materialized in arbitrary build directories.
- Source-root spelling and whether it is a development escape hatch or the
  foundation for future package distribution. Avoid incompatible parallel mechanisms.
- Lockstep validation via version stamp, bundled-tree hash in sources.json, or
  stricter agreement. Per-file hashes already serve invalidation.

## Opaque native types

The proposed GoAny type would hold a native Go object without Eq/Show. Compiled
code could use Go any after extending native validation; the difficult boundary
is the interpreter's scalar worker protocol, which cannot transmit pointers.
The implemented same-module scalar wrappers suit explicitly closed files and
connections, not persistent structures where an ID table would retain every version.

The proposed direction is to move Core interpretation into the sidecar worker,
keep checking in the REPL process, and send serialized Core. Interpreter/native
values then share a heap and GC, with no pointer wire encoding. The evaluator's
package dependencies exclude inference, so that split has an existing boundary.

Loading/unloading changes would respawn and replay pure declarations, coordinated
with [REPL generations](roadmap-tooling.md#repl-hardening). Go plugins do not supply
unloading and require matching build artifacts, so they do not solve this lifecycle.
Core serialization is shared with the precompiled-source option above; reuse
the implemented [module-object codec](design/pipeline.md#pipeline).
Co-locating native values remains a separate proposal, not an implemented
native ABI.
