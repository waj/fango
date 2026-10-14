# Roadmap: declaration metaprogramming

Expression quotes, expression splices, type reflection, and open `deriving`
are implemented and documented in [the design](design.md) and
[the reference](reference.md). Declaration generation is deferred until a concrete consumer needs named
declarations beyond ordinary deriving.

The future increment should preserve a property the module system has today:
a reader can determine a module's public names without executing compile-time
code. It should also permit generators whose useful result is inherently a
declaration group, including generated types.

## Proposed surface

A declaration quote would produce `Meta.Decls`, while expression quotations
produce `Meta.Code`. Its spelling remains unsettled now that expression
quotations use paired backticks and `quote` is an ordinary identifier. The
example below uses `DECL_QUOTE` as a pseudocode placeholder, not Fango syntax.

Names introduced by a declaration quote are explicit `Meta.Name` values. A
generator creates public names from strings and splices them wherever a name
is required:

```text
makeChoice : String -> Meta.Decls
makeChoice stem =
    ty = Meta.publicName stem
    yes = Meta.publicName (stem ++ "Yes")
    no = Meta.publicName (stem ++ "No")
    DECL_QUOTE
        type $(ty) = $(yes) | $(no)

$(makeChoice "Choice")
    exposing (Choice, ChoiceYes, ChoiceNo)
```

The `exposing` manifest belongs to the standalone declaration splice. It
states, literally at the splice site, every public name the generated group
may introduce. Expansion fails if the public names produced by the group and
the manifest differ. Generated private names use compiler-fresh `Meta.Name`
values and do not appear in the manifest. This gives generators stable handles
for referring to their own types, constructors, and values without making
their identity depend on text substitution.

The name-building operations are `Meta.publicName : String -> Name` for a
manifest-visible name and `Meta.freshName : String -> Name` for a hygienic
private name; the string passed to `freshName` is only a diagnostic hint.

`$(...)` is context-sensitive inside a declaration quote:

- in expression position it accepts `Meta.Code`, as it does today;
- in a declaration-name position it accepts `Meta.Name`;
- in type position it accepts `Meta.TypeRepr`;
- in declaration position it accepts `Meta.Decls`, allowing groups to be
  composed.

A standalone `$(...)` at declaration indentation accepts `Meta.Decls`.
Expression splices remain unchanged, so code such as
`getX = $(generateAccessor "x")` continues to generate the right-hand-side
expression and does not introduce a declaration.

The first increment should allow value, type, class, and instance
declarations. In particular, generated `TypeDecl` is part of the feature, not
a later extension. Module headers, imports, fixity declarations, effects, and
native declarations remain source structure and cannot be generated.

## Expansion model

Declaration splices require Haskell-style declaration groups: expand a group,
install all of its names and nominal identities together, then check later
source against the expanded group. A group may refer to names generated within
that same group through its `Meta.Name` handles. It may only use earlier
ordinary declarations or earlier expanded groups, retaining Fango's existing
source-order stage discipline.

Expansion must happen before resolving or inferring declarations that follow
the splice. Generated declarations then pass through the ordinary resolver,
inference, elaboration, and Core linter; both execution backends continue to
receive the same Core.

The existing visibility rule still applies after expansion. Public generated
names must be listed by the splice's manifest and may then be listed in the
module header. Private generated names remain inaccessible outside the module.

## Required diagnostics

- `SPLICE EXPORT MISMATCH` — the generated public names do not exactly match
  the standalone splice's `exposing` manifest.
- `INVALID SPLICE DECLARATION` — a generated group contains a module header,
  import, fixity, effect, or native declaration.
- Existing staging, compile-time effect/native/limit, duplicate-name, and
  ordinary type diagnostics apply unchanged.

## Delivery obligations

This increment changes declaration syntax, so it must update the TextMate
grammar and tokenize representative generated-declaration fixtures with
`vscode-textmate`. Parser and diagnostic goldens must cover declaration and
type/name holes, generated types whose constructors refer to the generated
type, declaration-group scoping, manifest mismatches, forbidden declaration
kinds, hygiene, rollback, and deterministic emission. The full Core linter,
interpreter/compiler differential suite, functional tests, and `go vet` remain
release gates.

Preserve the existing stage-safety, step-budget, and completion-group rules.
Measure elaboration cost when a forcing consumer justifies declaration generation.

## Expression quotation blocks

Deferred: allow a quotation to directly contain local bindings and statements
followed by a result expression, using ordinary Fango block rules. Today such
computations require an existing expression wrapper, such as an immediately
invoked lambda; see [quotes and splices](reference/metaprogramming.md#quotes-and-splices).
This extension is separate from the backtick syntax replacement and from
declaration generation.
