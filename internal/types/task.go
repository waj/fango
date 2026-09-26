package types

const TaskSpawnName = "Task.spawn"

// Transferability is structural, with no interpretation of function bodies.
// Nominal native handles and all functions are excluded, including phantom
// wrappers whose representation would otherwise look like empty data.
func Transferable(t Type, adts map[int]*ADTInfo, b *Builtins) bool {
	visiting := map[int]bool{}
	var check func(Type) bool
	check = func(t Type) bool {
		con, ok := t.(*TCon)
		if !ok {
			return false
		}
		for _, scalar := range []*TCon{b.Int, b.Float, b.String, b.Char, b.Bool, b.Unit} {
			if Equal(con, scalar) {
				return con.Name == scalar.Name
			}
		}
		adt := adts[con.Unique]
		if adt == nil || adt.Con.Name != con.Name || len(con.Args) != len(adt.Params) {
			return false
		}
		if adt.Repr == ReprBytes {
			return len(con.Args) == 0
		}
		if adt.Repr == ReprNativeAny || adt.Resource || adt.NativeIndexed {
			return false
		}
		for _, arg := range con.Args {
			if !check(arg) {
				return false
			}
		}
		if visiting[con.Unique] {
			return true
		}
		visiting[con.Unique] = true
		defer delete(visiting, con.Unique)
		for _, ctor := range adt.Ctors {
			for _, field := range ctor.Fields {
				if !check(SubstRigid(field, adt.ParamSubst(con.Args))) {
					return false
				}
			}
		}
		return true
	}
	return check(t)
}
