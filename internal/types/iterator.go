package types

// IteratorNextShape checks the source intrinsic before row erasure. Advancement
// must expose exactly the effects carried by its cursor plus owned Traversal.
func IteratorNextShape(t Type) bool {
	fn, ok := t.(*TFun)
	if !ok || len(fn.Eff.Labels) != 1 || fn.Eff.Tail == nil {
		return false
	}
	cursor, ok := fn.Arg.(*TCon)
	if !ok || cursor.Name != IteratorTypeName || len(cursor.Args) != 2 || !Equal(cursor.Args[1], fn.Eff.Tail) {
		return false
	}
	row, ok := cursor.Args[1].(*TVar)
	if !ok || row.Kind != RowVar {
		return false
	}
	traversal := fn.Eff.Labels[0]
	if traversal.Name != IteratorTraversalEffectName || !traversal.Suspension || traversal.Abort || len(traversal.Args) != 0 {
		return false
	}
	result, ok := fn.Ret.(*TCon)
	return ok && result.Name == "Maybe.Maybe" && len(result.Args) == 1 && Equal(cursor.Args[0], result.Args[0])
}
