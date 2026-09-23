package types

// ControlledRepresentation reports whether a type's stored function ABI can
// vary with its enclosing transport family. Fixed Machine callbacks and opaque
// cursors have a single representation.
func ControlledRepresentation(t Type, adts map[int]*ADTInfo) bool {
	return controlledRepresentation(t, adts, map[int]bool{})
}
func controlledRepresentation(t Type, adts map[int]*ADTInfo, visiting map[int]bool) bool {
	switch t := t.(type) {
	case *TFun:
		control := FunctionControl(t)
		return control.Polymorphic && control.Transport < Machine || controlledRepresentation(t.Arg, adts, visiting) || controlledRepresentation(t.Ret, adts, visiting)
	case *TCon:
		if t.Name == CoroutineTypeName || t.Name == CompletionTypeName || t.Name == WorkOwnerTypeName || t.Name == WorkFacetTypeName || t.Name == WorkTypeName {
			return false
		}
		for _, arg := range t.Args {
			if controlledRepresentation(arg, adts, visiting) {
				return true
			}
		}
		adt := adts[t.Unique]
		if adt == nil || visiting[t.Unique] {
			return false
		}
		visiting[t.Unique] = true
		defer delete(visiting, t.Unique)
		for _, ctor := range adt.Ctors {
			for _, field := range ctor.Fields {
				if controlledRepresentation(field, adts, visiting) {
					return true
				}
			}
		}
	}
	return false
}
