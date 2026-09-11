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
)

// Intrinsic reports whether a canonical symbol names a compiler intrinsic.
func Intrinsic(name string) bool {
	return name == ScopeBracketName
}

// IntrinsicArity is the number of parameters an intrinsic's synthesized
// worker takes. It is fixed by the compiler, not read from the declaration.
func IntrinsicArity(name string) int {
	if name == ScopeBracketName {
		return 3
	}
	return 0
}
