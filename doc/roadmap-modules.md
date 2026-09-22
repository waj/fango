# Roadmap: modules and distribution

Open source-distribution designs. Current loading and worker
contracts are in [pipeline](design/pipeline.md) and [backend](design/backend.md).

## Distributing the bundled sources

The library is [a tree on disk](design/pipeline.md#the-library-root) that the
compiler resolves and reads as source. What remains is publishing it as
something other than source.

- Precompiled library artifacts. Reuse the implemented checked-module codec and
  its installation boundary ([pipeline](design/pipeline.md#pipeline)) rather
  than designing a second artifact format: a precompiled library is the same
  object, published by a producer instead of a local build. It needs a durable
  identity in place of the compiler-executable fingerprint, a published source
  manifest, and a compatibility rule for a consumer built from a different
  compiler. Shipping those artifacts remains separate from project-local caching.
- Compiler/library skew has two detectors — bundled native declarations against
  the linked registry, and the bundled `List`'s shape — and one gap: a bundled
  `.native.go` edit does not reach the compile-time evaluator, which runs the
  copy linked into the compiler. A version stamp or a bundled-tree hash would
  close it, at the cost of making an edited library refuse to compile at all.
  Decide that with a concrete consumer rather than in advance.
- A project supplying its own Prelude, or otherwise relaxing RESERVED MODULE.
  Bundled names are reserved outright today, and the source-root spelling that
  would let a project override one is the same decision as package distribution;
  avoid incompatible parallel mechanisms.

Package fetching and independent library versioning remain deferred.

## Scoping operator fixity to its module

Fixity is [shared across a whole program](reference/syntax.md#declaring-operators):
two modules declaring the same operator's fixity differently is an error, so a
spelling has one precedence everywhere. The table is therefore built from every
parsed file, and a module's checked-artifact key names the whole of it rather
than the operators the module actually mentions. A program that declares an
operator consequently gives every module it shares with a neighbouring program
a different key, and the two stop sharing artifacts.

The direction is to attach fixity to the operator's own declaration, so it
travels with the declaring module's interface and reaches a consumer the way
any other exported contract does. A module's key would then name only its own
dependencies' contracts, and the graph-wide hash would leave it. Nothing forces
this yet: only `Basics` declares operators in the library, so in practice the
table is the same in every program.
