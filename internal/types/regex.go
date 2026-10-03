package types

// IsRegexLiteralConstructor validates the bundled wrapper that syntax creates.
// Names identify the library contract; callers additionally check the owning
// declaration and nominal identity in their installed type environment.
func IsRegexLiteralConstructor(ctor *CtorInfo) bool {
	if ctor == nil || ctor.Result == nil || ctor.Name != "Regex.Regex" || ctor.Result.Name != "Regex.Regex" || len(ctor.Result.Args) != 0 || len(ctor.Fields) != 1 {
		return false
	}
	field, ok := ctor.Fields[0].(*TCon)
	return ok && field != nil && field.Name == "Runtime.Native.Any" && len(field.Args) == 0
}
