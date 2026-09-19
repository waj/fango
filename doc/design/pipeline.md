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

Successful batch commands also publish a compiler-fingerprinted project
artifact after validation. Before loading the graph, a later `check`, `build`,
or `run` revalidates the recorded graph through the filesystem provider,
including exact path casing, bundled-name conflicts, sidecar presence, and
content hashes. An exact match lets `check` reuse the prior success and lets
build/run reuse the emitted Go bytes, skipping parsing through emission.
Bundled inputs are covered by the exact compiler-executable fingerprint.
Artifacts carry a digest of their complete payload; invalid envelopes,
structurally invalid emissions, missing inputs, hash mismatches, and cache I/O
errors are ordinary misses.

Batch compilation sessions have a test-only event observer. It records cache
hits and misses and the parse, resolve, check, elaborate, semantic-lint,
lowering, and emission stages with their owner. It never writes CLI output.

`internal/check.Session` owns the uncached semantic path. The loader retains a
merged AST only as a differential-test adapter; normal compilation consumes
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

Each successful batch parse is persisted immediately as a content-addressed
`ParsedUnit`, before graph validation or checking. Its versioned JSON payload
contains the complete unresolved AST and discovery metadata derived by that
same parse: header, imports, Prelude choice, and syntax-driven dependencies.
Every cache use decodes a new tree, validates tagged variants and span bounds,
and binds spans to the current source file, so fixity, resolution, staging, and
inference mutations cannot accumulate in the artifact. Failed parses are not
cached. The formatter continues to lex and parse its requested text directly.

Operator runs remain flat until the complete graph is parsed. `internal/fixity`
then groups them before name resolution, including inside quotes. Fixity belongs
to a spelling and is graph-wide. No unresolved run reaches inference. Operators
resolve as ordinary values; only short-circuit `&&` and `||` lower to `If`.
Core has no operator node.

## Module graph and Prelude

`modules.Graph` runs source discovery, complete-graph validation, and per-module
resolution as separate phases. Discovery always rechecks provider paths,
headers, reserved bundled names, and native sidecars, even on parsed-unit hits.
Validation detects cycles and collects the complete effective fixity table,
including builtins, before any fresh tree is rewritten; the sorted table also
has a stable SHA-256 fingerprint. Resolution then processes modules in
dependency-first order with lexical tie-breaking. Local modules come from the
entry directory; bundled sources come from the embedded provider and reserve
their module names. Imported scopes expose only direct public interfaces,
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
