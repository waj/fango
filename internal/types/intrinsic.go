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

// FileHandleName is the bundled open-file resource. Its constructor is
// private and its field is a plain Int, so nothing about the type's shape
// says it holds a capability; the compiler knows it does by this name.
const FileHandleName = "File.Handle"

// ResourceType reports whether a canonical ADT name is a compiler-known
// resource: a value of that type is treated as capability-carrying by the
// capture analysis, so it can neither leave the scope that produced it nor be
// stored in a scoped handler's state (doc/design.md, "Functions and effects").
func ResourceType(name string) bool {
	return name == FileHandleName
}

// ResourceRunner answers the resource a bundled scoped runner acquires. The
// runners wrap `Scope.bracket` in ordinary fango; naming them here makes their
// call sites subject to the same instantiated-result check as `State.run`,
// and lets their own bodies call the intrinsic with a polymorphic result.
func ResourceRunner(name string) (resource string, ok bool) {
	switch name {
	case "File.withFile", "File.withOutput", "File.withAppend", "File.withOpened":
		return FileHandleName, true
	}
	return "", false
}
