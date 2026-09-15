package types

// InspectionShapeSafe checks all constructors of an immutable nominal type,
// independently of the constructor present in a runtime value. Type parameters
// are checked through their actual descriptors at instantiation. Recursive data
// is safe unless a field or type argument contains a function or resource.
func InspectionShapeSafe(t Type, adts map[int]*ADTInfo) bool {
	return inspectionShapeSafe(t, adts, map[int]bool{})
}
func inspectionShapeSafe(t Type, adts map[int]*ADTInfo, visiting map[int]bool) bool {
	switch t := t.(type) {
	case *TVar:
		return true
	case *TFun:
		return false
	case *TCon:
		// A snapshot may itself retain opaque function/resource payloads.
		if t.Name == FailureTypeName {
			return false
		}
		for _, arg := range t.Args {
			if !inspectionShapeSafe(arg, adts, visiting) {
				return false
			}
		}
		adt := adts[t.Unique]
		if adt == nil {
			switch t.Name {
			case "Int", "Float", "String", "Char", "Bool", "()":
				return len(t.Args) == 0
			default:
				return false
			}
		}
		if adt.Resource {
			return false
		}
		if visiting[t.Unique] {
			return true
		}
		visiting[t.Unique] = true
		defer delete(visiting, t.Unique)
		for _, ctor := range adt.Ctors {
			for _, field := range ctor.Fields {
				if !inspectionShapeSafe(field, adts, visiting) {
					return false
				}
			}
		}
		return true
	default:
		return false
	}
}
