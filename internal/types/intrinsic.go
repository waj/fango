package types

// Compiler intrinsics are bundled native declarations implemented as Core
// nodes. Recognition uses resolved declaration identity, never user spelling.
const (
	// ScopeBracketName owns synchronous resource cleanup on normal and abort exits.
	ScopeBracketName = "Scope.bracket"
	// StreamWithProducerName owns a private cursor for its consumer's extent.
	StreamWithProducerName = "Stream.withProducer"
	// StreamYieldEffectName carries the lexical owner token for suspension.
	StreamYieldEffectName       = "Stream.Yield"
	StreamYieldName             = "Stream.yield"
	IteratorTypeName            = "Iterator.Iterator"
	IteratorNextName            = "Iterator.next"
	IteratorTraversalEffectName = "Iterator.Traversal"
)

// RuntimeEvidenceEffect reports whether a row label needs runtime evidence.
// IO is ambient. Traversal uses the cursor's checked advancement identity.
func RuntimeEvidenceEffect(label EffLabel) bool {
	return SurfaceName(label.Name) != "IO" && !(label.Suspension && label.Name == IteratorTraversalEffectName)
}

// CursorAccess is advancement proof metadata. Zero is deliberately invalid.
type CursorAccess uint8

const ExclusiveAdvance CursorAccess = 1

func Intrinsic(name string) bool { return IntrinsicArity(name) != 0 }

// IntrinsicArity is fixed by the compiler, not read from the declaration.
func IntrinsicArity(name string) int {
	switch name {
	case ScopeBracketName:
		return 3
	case StreamWithProducerName:
		return 2
	case IteratorNextName:
		return 1
	}
	return 0
}
