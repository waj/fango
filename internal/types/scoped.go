package types

// ScopedCallback describes the deliberately restricted rank-two row contract:
// the final parameter is a unary callback, its row is universally bound, and
// the runner's arguments/result/residual row are independent of that binder.
// The callback's result may mention the row because the runner consumes it
// inside the scope; the runner's own result may not.
func ScopedCallback(t Type, arity int) (*TVar, Row, Type, bool) {
	if arity < 1 {
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
	callback, ok := args[arity-1].(*TFun)
	if !ok || len(callback.Eff.Labels) != 0 {
		return nil, Row{}, nil, false
	}
	row, ok := callback.Eff.Tail.(*TVar)
	if !ok || !row.Rigid || row.Kind != RowVar {
		return nil, Row{}, nil, false
	}
	outside := append(append([]Type(nil), args[:arity-1]...), rest)
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
