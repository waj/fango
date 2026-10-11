package types

// ScopedCallback describes the deliberately restricted rank-two row contract:
// the selected parameter is a unary callback whose row ends in a universally
// bound tail, after any effects the runner handles for it, and the runner's
// arguments/result/residual row and those effects' arguments are independent
// of that binder.
// The callback's result may mention the row because the runner consumes it
// inside the scope; the runner's own result may not.
func ScopedCallback(t Type, arity, index int) (*TVar, Row, Type, bool) {
	if arity < 1 || index < 0 || index >= arity {
		return nil, Row{}, nil, false
	}
	var args []Type
	var rows []Row
	rest := t
	for range arity {
		fn, ok := rest.(*TFun)
		if !ok {
			return nil, Row{}, nil, false
		}
		args, rows, rest = append(args, fn.Arg), append(rows, fn.Eff), fn.Ret
	}
	callback, ok := args[index].(*TFun)
	if !ok {
		return nil, Row{}, nil, false
	}
	row, ok := callback.Eff.Tail.(*TVar)
	if !ok || !row.Rigid || row.Kind != RowVar {
		return nil, Row{}, nil, false
	}
	outside := append([]Type{rest}, args[:index]...)
	outside = append(outside, args[index+1:]...)
	for _, label := range callback.Eff.Labels {
		outside = append(outside, label.Args...)
	}
	for _, r := range rows {
		outside = append(outside, r)
	}
	for _, ty := range outside {
		for _, v := range RigidVarsIn(ty) {
			if v.ID == row.ID {
				return nil, Row{}, nil, false
			}
		}
	}
	return row, rows[arity-1], callback, true
}

// ScopedParameterIndex locates the callback selected by the declaration's
// explicit row binder. A binder shared by multiple callbacks is invalid.
func ScopedParameterIndex(t Type, arity int, row *TVar) int {
	if row == nil {
		return -1
	}
	index := -1
	for i := range arity {
		fn, ok := t.(*TFun)
		if !ok {
			return -1
		}
		if callback, ok := fn.Arg.(*TFun); ok && Equal(callback.Eff.Tail, row) {
			if index >= 0 {
				return -1
			}
			index = i
		}
		t = fn.Ret
	}
	return index
}

// ContainsScopedEffect inspects proof types before row erasure.
func ContainsScopedEffect(t Type, unique int) bool {
	switch t := t.(type) {
	case *TCon:
		for _, a := range t.Args {
			if ContainsScopedEffect(a, unique) {
				return true
			}
		}
	case *TFun:
		return ContainsScopedEffect(t.Arg, unique) || ContainsScopedEffect(t.Eff, unique) || ContainsScopedEffect(t.Ret, unique)
	case Row:
		for _, l := range t.Labels {
			if l.Unique == unique {
				return true
			}
			for _, a := range l.Args {
				if ContainsScopedEffect(a, unique) {
					return true
				}
			}
		}
		return t.Tail != nil && ContainsScopedEffect(t.Tail, unique)
	}
	return false
}
