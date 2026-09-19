# Compiler pipeline and resolution

Source loading, parsing, name resolution, and the boundary between inference and elaboration.

[Design index](../design.md). Source and checks: [Loader](../../internal/modules/graph.go), [Fixity](../../internal/fixity), [Resolution tests](../../internal/modules/graph_test.go), [Parser tests](../../internal/parser/parser_test.go).

## Pipeline

```text
source -> lexer -> parser -> AST -> fixity/name resolution
       -> inference -> typed AST -> elaboration -> Core -> Core lint
       -> module-owned machine lowering -> Go AST -> go build
```

The interpreter executes the same semantic Core. Go emission materializes
module-owned Machine families even for Direct entry points; the interpreter
installs Machine lowering when cursor intrinsics require it. Staging uses a
separate lint entry and lowers the exact splice operand and reachable completed
definitions (see [metaprogramming](metaprogramming.md)).

Every command discovers and validates the current graph through the filesystem
provider — exact path casing, bundled-name conflicts, sidecar presence, and
content hashes — and then reuses artifacts one module at a time. Nothing
outranks that: there is no whole-project success record, so no command can skip
the graph it is about to compile. Bundled inputs are covered the same way local
ones are, by the source and sidecar bytes their module key already contains.
Every artifact carries a digest of its
complete payload; invalid frames, structurally invalid emissions, missing
inputs, hash mismatches, and cache I/O errors are ordinary misses.

Compilation sessions report through one event observer. It records the parse,
resolve, check, elaborate, stage-snapshot, semantic-lint, lowering, and
emission stages with their owner, and the checked and emitted artifact hits, misses, and stores, and
the stage sections actually read, separately from them, so a caller can tell
reuse from work. Every site reports after the work it names, so an event
carries the elapsed time of its own stage rather than the gap to the next one,
and artifact events carry the bytes they moved. Stages long enough to be worth
watching also announce themselves before they run, so a caller can say what is
running while it runs without inferring it from the previous event; a begin
event carries no duration, and counting work means counting completions. Tests count the events; the CLI
presents them as progress and build statistics behind its verbosity flags. An
absent observer costs the pipeline nothing, which is what the commands install
by default.

`internal/check` owns the semantic path, and its installer is shared: a batch
command installs a whole entry graph, a REPL session installs its Prelude roots
and then one prompt import increment at a time into the checker it keeps. The
loader retains a merged AST only as a differential-test adapter; normal compilation consumes
resolved modules in dependency-first order. Each module is checked against the
declaration state already installed in the session, elaborated immediately,
and semantically linted before the next module. The entry/dependency role and
entry symbol are explicit inputs, so an imported declaration named `main` has
no entry-only obligations.

Inference publishes an immutable-by-contract `ModuleState` delta containing
the module's solved schemes and declaration tables, nominal types and effects,
classes and methods, instances, native/intrinsic/worker metadata, derivers,
capture summaries, and instance visibility. The typed-AST `CheckedModule`
handoff is separate: AST-keyed types, substitutions, staging callbacks, and
other inference workspace are not declaration state. Instance cutoffs in a
module state use module/source declaration references; live checker indexes are
only transient checking machinery.

Owner-scoped elaboration and lint use installed dependency worker signatures,
result-capture summaries, and validated capture contracts as context. They do
not traverse or revalidate dependency runtime bodies. Core definitions carry
an ABI summary for body-derived facts used by emission, including controlled
callback invocation and passive Machine-factory classification. Graph assembly
concatenates already validated Core and applies only entry-specific checks.
Instance overlap, blanket-context cycles, and duplicate derivers also have an
explicit graph compatibility pass over module states; declaration collisions
and effect-operation binder shadowing remain graph-resolution and per-module
header-validation rules, respectively.

The installable `ModuleObject` adds the module's public resolver interface,
owned runtime and stage Core, staging completion groups, templates, and ABI
summaries. Stage Core is a separate section of the artifact over the same node
pool, so it can be left unread; the rest of this paragraph describes both. Its typed reference-graph codec preserves shared and recursive
compiler data, uses exact IEEE floating-point bits, sorts maps, and rejects
unknown variants, malformed references, missing fields, and invalid source
provenance.

The codec writes a string pool, a table of node sizes, and tagged values. Each
distinct string — every type name, field name, string value, and span text — is
stored and allocated once, and a decoder walks bytes straight into typed values
without materializing the graph first, reaching any node from its recorded
offset without reading the ones before it. The size table is checked against
the payload length before anything is decoded, and every value decoded must end
exactly at its recorded boundary, so a damaged table cannot be read past its
own description or ask for an allocation the payload cannot hold. Encoding is
deterministic: traversal order is fixed, map keys are sorted, and the string
pool follows first encounter. Pointer identity and session allocation numbers are not semantic:
installation interns builtin/imported nominal names, allocates fresh local
nominal, type-variable, capture, scope, and resume identities, remaps template
indices, and reconstructs stable instance cutoffs without changing positional
parameter or evidence order.

Interning an imported declaration reaches every copy of it, including a
generated dictionary constructor that no name table exposes, and binds the
decoded declaration parameters to the installed ones. Otherwise a type reached
only by value — a capture contract's recorded clause fields, for instance —
would receive fresh variables and contradict the interned declaration it
describes.

Source spans carry their source identity, exact text, and bounded surrounding
anchors. Decoding binds them to caller-supplied current source files, validates
the range, and relocates a uniquely anchored span when comments moved it.
Thus an importer never retains a foreign dependency's stale file pointer or
blindly applies an old byte offset. Resolver objects contain only exported
maps; installing one cannot expose private names.

Installing a decoded object defers its stage Core rather than installing it.
Completing a module's stage snapshot elaborates its declarations against the
installed stage definitions of its dependencies, so a module checked from
source needs all of them and a compile whose modules all come from cache needs
none. Deferred sections are forced in installation order immediately before
the first module is checked from source, and again before a prompt runs a
splice, which is the only way the REPL reaches the evaluator without going
through installation. A deferral holds the object's own decoder and remapper,
so the stage half comes back as the same pointers for structure the installed
half already holds and is interned against the same declarations; a section
that cannot be read is a violated compiler invariant rather than a miss,
because its object is already installed. Reading a section is also where the
object's recorded stage fingerprint is checked against its contents, which
installation therefore does not recompute.

Module installation validates all decoded state before publication and uses a
checker checkpoint for the remaining mutation. Types, effects, classes,
instances, native/intrinsic metadata, IO identity, capture contracts,
visibility, templates, and the staging evaluator commit together. Rollback
retains fresh-supply advancement but restores every published table. Persistent
lookup treats any decode or compatibility failure as a miss and checks the
owner normally. An owner whose dependencies are not all summarized is checked
and left unpublished, as is every later consumer of it; caching never decides
whether a program compiles.

After graph preparation, the shared session looks up each owner in dependency
order. An owner keeps one checked object, at a slot named after the owner
rather than after a hash of its inputs, and a later check replaces it. The
artifact leads with the record of what it was built from: a base key over the
exact parsed source and native-sidecar identity, module role, effective fixity
hash, and ordered dependency semantic fingerprints, plus the compile-time
dependencies the prior successful check discovered and the stage fingerprints
they had. Only the artifact can report that second set, which is why it is
recorded rather than keyed; a lookup that had to name it in advance could not
be made without first doing the work. A record that disagrees with the current
graph is an ordinary miss, and the recheck overwrites the slot, so the cache
holds what a module is rather than what it has been. Restoring a module's
earlier source therefore rechecks it. Only successfully checked, elaborated,
and owner-linted objects are published, so completed dependency artifacts
survive a later entry failure.

Semantic and ABI summaries are canonical, source-position-independent views of
the installed declaration state and Core headers. Stage summaries cover
declarative stage Core and templates, with allocation identities normalized.
The staging evaluator records the complete Core closure reached by every splice
or deriver, including dictionary definitions, ordinary helpers, quote holes,
callbacks, and native owners. An owner answers to its sidecar package name as
well as its module name, so a headerless entry, whose sidecar is named after
its file, does not record itself as its own dependency. Cached Core retains symbolic global names, so a
later splice executes the currently installed dependency bodies. Record
validation recomputes recorded dependency stage fingerprints from the current
graph; this catches edits hidden behind an unchanged relay while allowing a
runtime-only importer to remain a hit after a dependency body edit. Comment and
source-position changes rebuild their own owner but do not change downstream
semantic, ABI, or stage fingerprints.

Every cached state is still installed through the graph compatibility checks.
Consequently independently cached branches cannot bypass duplicate-deriver,
instance-overlap, or blanket-cycle validation. Batch event instrumentation
reports `checked-cache-hit`, `checked-cache-miss`, and `stage-section`
separately from actual `check`, `elaborate`, and `semantic-lint` work, so
reusing a module and checking one are never confused for each other.

## Parsing and surface lowering

The lexer records byte spans and line/column positions without layout tokens.
The recursive-descent parser applies the offside rule from token columns.
Tokens opening constructs, an if's aligned then/else, and composite delimiters
have explicit layout exceptions; [syntax](../reference/syntax.md) owns their
surface rules. AST and diagnostic dumps are golden-test interfaces.

Layout and semicolon blocks share one ordered AST form; explicit blocks retain
separator spans for formatting. Bracket lists and tuples lower to canonical
bundled constructors. A trailing lambda uses ordinary application/lambda nodes.
These forms introduce no second type or evaluation system. Tuple is a syntax
root, always resolvable but never implicitly in scope.

Parses are not persisted. Every command parses each module in the graph from
source, and discovery reads those bytes anyway to hash them, so the front end
is deliberately the one stage with no artifact. Serializing an AST is not worth
it: a tree large enough to round-trip safely costs an order of magnitude more
to decode and validate than the recursive-descent parser costs to rerun, and
parsing is a small fraction of a warm command. Reuse begins at the checked
module, whose identity already contains the exact source hash.

Operator runs remain flat until the complete graph is parsed. `internal/fixity`
then groups them before name resolution, including inside quotes. Fixity belongs
to a spelling and is graph-wide. No unresolved run reaches inference. Operators
resolve as ordinary values; only short-circuit `&&` and `||` lower to `If`.
Core has no operator node.

## The library root

The standard library and the Go runtime support are a tree on disk rather than
bytes in the compiler executable, so editing either takes effect on the next
build. `internal/libroot` resolves one root holding `stdlib/` and `runtime/`
side by side: `FANGO_ROOT`, then the install layout beside the executable, then
the enclosing checkout, which is what lets the repository's own tests and
`go run ./cmd/fango` work with nothing configured. A root is accepted only if
`stdlib/Prelude.fango` is readable beneath it, so a partial tree misses rather
than half-loading. An explicit `FANGO_ROOT` that holds no library is an error
rather than a reason to keep looking: a typo must not silently select whatever
tree the working directory happens to sit in. [Commands](../reference/commands.md#the-library-root)
owns the search order and the install layout as user-visible behavior.

Resolution is process-wide and computed once, because the prelude is reached
from focused checker tests and the evaluator's native executor, neither of
which has any business carrying installation layout. For the same reason the
root cannot change within a process.

The trees are read through an exact-name index rather than joined paths, which
preserves what embedding gave for free. Names stay case-exact, which a
case-insensitive filesystem would otherwise lose and which the local provider
beside it enforces deliberately. Directories stay flat, so a dotted module
still does not name a nested file. Bytes are read once and retained, so every
reader in a process sees one library and a mid-compile edit cannot tear a build
across two versions of it.

What the library cannot do is disagree with the compiler that reads it. Bundled
native declarations are checked against the linked interpreter registry
(`INVALID BUNDLED NATIVE`) and the bundled `List`'s constructor layout against
what the backends project (`INVALID BUNDLED LIST`), both of which were compiler
invariants when the sources were embedded and are user-reachable now. The
residual gap is a sidecar: editing a bundled `.native.go` reaches compiled
programs and the interpreter's worker, but not the copy of `stdlib` linked into
the compiler, which is what the compile-time evaluator runs. That one needs a
compiler rebuild, and nothing detects it.

## Module graph and Prelude

`modules.Graph` runs source discovery, complete-graph validation, and per-module
resolution as separate phases. Discovery always rechecks provider paths,
headers, reserved bundled names, and native sidecars. Validation detects cycles
and collects the complete effective fixity table, including builtins, before any
fresh tree is rewritten; the sorted table also has a stable SHA-256 fingerprint.
Resolution then processes modules in dependency-first order with lexical
tie-breaking. Local modules come from the
entry directory; bundled sources come from the [library root](#the-library-root)
and reserve their module names. Imported scopes expose only direct public interfaces,
although instance visibility includes transitive dependencies.

Prelude contains only imports and emits no Go package. Its imports enter each
non-opted-out module through the ordinary import path, qualifiers included.
Identical canonical bindings may repeat, so explicit imports can expose more
names from a prelude module. Prelude remains in the manifest: changing the
default scope invalidates builds. Bundled modules opt out to avoid cycles.

The REPL and focused checker tests load the same bundled dependency closure.
Roots are Prelude and the syntax dependencies Meta, Derive, List, and Tuple;
rooting them exposes no names. The checker records that owner set so fixture
projections omit the whole prelude without skipping its checking or linting.
Only focused tests without a resolver bind exposed surface names directly.

Batch builds fill a fresh graph. REPL imports stage new nodes and a copied
operator table, committing only if the increment succeeds. Committed nodes are
already resolved and are not reprocessed. Incremental elaboration installs
intrinsics, native boundary metadata, and capture contracts under the same
rules as a batch program; prompt checks use all installed definitions.

## Identities and visibility

Resolution canonicalizes module declarations and imported references before
inference; local binders keep their source names. A graph-wide fresh supply
keeps nominal ADT/effect identities distinct. Types use generation-stable
`Unique` identities, not display names. Constructors have a separate namespace.

Functions with syntactic parameters are module-wide; ordinary values and block
bindings are source-ordered. Shadowing is rejected. Source dependency analysis
and inference scheduling preserve that scope and strict evaluation order.
Core has no imports, but definitions retain source-module owners and `Prog.Entry`
identifies the entry independently of spelling.

Record schemas are nominal. Resolution records visible schema candidates for
field uses and inferred patterns/literals; labels filter visibility but never
select a type. Named records first resolve their type name. Inference settles
the receiver before checking the visible schema.

## Elaboration boundary

Inference determines types and contracts. Elaboration resolves defaulting,
builds evidence, collapses application spines, chooses direct/indirect calls,
lifts polymorphic locals, compiles pattern matrices, and introduces ANF where
Go requires statements. Core lint checks the result before execution.

Native declaration validation and materialization are described in
[backend and runtime](backend.md#native-boundaries); surface syntax belongs in
[native sidecars](../reference/native.md).
