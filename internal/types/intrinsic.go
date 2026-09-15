package types

// Compiler intrinsics are bundled `native` declarations the compiler
// implements as a Core node rather than as a Go template or a sidecar call.
// They are recognized by resolved declaration identity — a canonical
// `Module.name` symbol owned by a bundled module — never by the spelling of
// an arbitrary user function.
const (
	// ScopeBracketName is the synchronous cleanup scope behind
	// `Scope.bracket`. No fango expression can release a resource when the
	// scope body exits to an outer handler, so elaboration supplies its body.
	ScopeBracketName = "Scope.bracket"

	// GeneratorWithIteratorName owns a private MachineIterator for the dynamic
	// extent of its consumer callback. Its source declaration is staged in E8;
	// Core ownership checks recognize the resolved identity, never spelling.
	GeneratorWithIteratorName = "Generator.withIterator"

	// GeneratorEffectName is the handled-at-an-owner-boundary suspension
	// effect. Its evidence is a lexical owner token; Generator.yield lowers
	// to a semantic-Core Suspend carrying that evidence.
	GeneratorEffectName         = "Generator.Generator"
	GeneratorYieldName          = "Generator.yield"
	IteratorTypeName            = "Iterator.Iterator"
	IteratorForEachName         = "Iterator.forEach"
	IteratorFoldName            = "Iterator.fold"
	IteratorNextName            = "Iterator.next"
	IteratorTraversalEffectName = "Iterator.Traversal"
	StreamWithProducerName      = "Stream.withProducer"
	StreamYieldEffectName       = "Stream.Yield"
	StreamYieldName             = "Stream.yield"
)

// RuntimeEvidenceEffect reports whether an effect row label needs an explicit
// runtime evidence parameter. IO is ambient. Traversal is discharged by checked
// cursor access; Yield carries a lexical owner token like handler evidence.
func RuntimeEvidenceEffect(label EffLabel) bool {
	return SurfaceName(label.Name) != "IO" && label.Name != IteratorTraversalEffectName
}

// CursorAccess is proof metadata for a cursor advancement. Zero is deliberately
// invalid: an ownership-aware Core operation must carry its access obligation.
type CursorAccess uint8

const ExclusiveAdvance CursorAccess = 1

// Intrinsic reports whether a canonical symbol names a compiler intrinsic.
func Intrinsic(name string) bool {
	switch name {
	case ScopeBracketName, GeneratorWithIteratorName, StreamWithProducerName, IteratorForEachName, IteratorFoldName, IteratorNextName:
		return true
	}
	return false
}

// IntrinsicArity is the number of parameters an intrinsic's synthesized
// worker takes. It is fixed by the compiler, not read from the declaration.
func IntrinsicArity(name string) int {
	switch name {
	case ScopeBracketName:
		return 3
	case GeneratorWithIteratorName, StreamWithProducerName:
		return 2
	case IteratorForEachName:
		return 2
	case IteratorFoldName:
		return 3
	case IteratorNextName:
		return 1
	}
	return 0
}
