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
	// effect. It has no runtime evidence record: Generator.yield lowers to a
	// semantic-Core Suspend and selects Machine transport instead.
	GeneratorEffectName = "Generator.Generator"
	GeneratorYieldName  = "Generator.yield"
	IteratorTypeName    = "Iterator.Iterator"
)

// RuntimeEvidenceEffect reports whether an effect row label needs an explicit
// runtime evidence parameter. IO is ambient and Generator is represented by
// Machine transport itself.
func RuntimeEvidenceEffect(label EffLabel) bool {
	return SurfaceName(label.Name) != "IO" && !label.Suspension
}

// IteratorConsumer reports the initial E8 combinators that consume a borrowed
// iterator cursor. Raw step exposure is intentionally deferred until Core can
// represent transfer to a successor cursor.
func IteratorConsumer(name string) bool {
	switch name {
	case "Iterator.forEach", "Iterator.fold", "Iterator.find", "Iterator.take":
		return true
	}
	return false
}

// Intrinsic reports whether a canonical symbol names a compiler intrinsic.
func Intrinsic(name string) bool {
	return name == ScopeBracketName
}

// IntrinsicArity is the number of parameters an intrinsic's synthesized
// worker takes. It is fixed by the compiler, not read from the declaration.
func IntrinsicArity(name string) int {
	switch name {
	case ScopeBracketName:
		return 3
	case GeneratorWithIteratorName:
		return 2
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
