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

Operator runs remain flat until the complete graph is parsed. `internal/fixity`
then groups them before name resolution, including inside quotes. Fixity belongs
to a spelling and is graph-wide. No unresolved run reaches inference. Operators
resolve as ordinary values; only short-circuit `&&` and `||` lower to `If`.
Core has no operator node.

## Module graph and Prelude

`modules.Graph` owns discovery, validation, public interfaces, dependency order,
and resolution. Local modules come from the entry directory; bundled sources
come from the embedded provider and reserve their module names. Ordering is
dependency-first with lexical tie-breaking. Imported scopes expose only direct
public interfaces, although instance visibility includes transitive dependencies.

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
