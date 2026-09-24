package types

import "fmt"

// NativeStorage is a representation-blind storage edge. The only opaque
// payload admitted by a sidecar is the index of one local resource wrapper.
// Indices are source types, not runtime tags or untyped lookup keys.
type NativeStorage struct {
	Kind            string
	Handle, Payload int
}

// CheckNativeStorage reconstructs the contract from the declaration and its
// checked wrapper shapes. Backends must not infer it from Go representations.
func CheckNativeStorage(n *NativeInfo) (NativeStorage, error) {
	if n.Template != nil {
		return NativeStorage{}, nil
	}
	var args []Type
	rest := n.Scheme.Body
	for range n.Arity {
		fn, ok := rest.(*TFun)
		if !ok {
			return NativeStorage{}, fmt.Errorf("invalid native arrows")
		}
		args, rest = append(args, fn.Arg), fn.Ret
	}
	// One nominal wrapper must never appear at different indices in one
	// native declaration. Otherwise a sidecar could expose a public cast.
	indices := map[int]Type{}
	for i, t := range append(append([]Type(nil), args...), rest) {
		var wrapper *CtorInfo
		if i == len(args) {
			wrapper = n.ResultWrapper
		} else if i < len(n.ParamWrappers) {
			wrapper = n.ParamWrappers[i]
		}
		if wrapper == nil {
			continue
		}
		if old := indices[wrapper.Result.Unique]; old != nil && !Equal(old, t) {
			return NativeStorage{}, fmt.Errorf("native `%s` changes a wrapper's type index", n.Name)
		}
		indices[wrapper.Result.Unique] = t
	}
	if result, ok := rest.(*TCon); ok && n.ResultWrapper != nil && len(result.Args) > 0 {
		for i, arg := range args {
			if i >= len(n.ParamWrappers) || n.ParamWrappers[i] == nil {
				continue
			}
			input, ok := arg.(*TCon)
			if !ok || len(input.Args) == 0 {
				continue
			}
			if len(input.Args) != len(result.Args) {
				return NativeStorage{}, fmt.Errorf("native `%s` changes a wrapper's type indices", n.Name)
			}
			for j, a := range input.Args {
				if !Equal(a, result.Args[j]) {
					return NativeStorage{}, fmt.Errorf("native `%s` changes a wrapper's type index", n.Name)
				}
			}
		}
	}
	// General variables in the ABI need a same-index storage handle. Ordinary
	// phantom scalar wrappers do not authorize crossing arbitrary values.
	payload := -1
	for i, t := range args {
		if _, ok := t.(*TVar); ok {
			if payload != -1 {
				return NativeStorage{}, fmt.Errorf("native storage accepts one opaque payload")
			}
			payload = i
		}
	}
	_, opaqueResult := rest.(*TVar)
	if payload == -1 && !opaqueResult {
		if result, ok := rest.(*TCon); ok && n.ResultWrapper != nil && len(result.Args) == 1 && opaqueWrapper(n.ResultWrapper) {
			for i, arg := range args {
				input, ok := arg.(*TCon)
				if ok && i < len(n.ParamWrappers) && opaqueWrapper(n.ParamWrappers[i]) && len(input.Args) == 1 && Equal(input.Args[0], result.Args[0]) {
					return NativeStorage{Kind: "alias", Handle: i, Payload: -1}, nil
				}
			}
		}
		if result, ok := rest.(*TCon); ok && n.ResultWrapper != nil && len(result.Args) == 1 && opaqueWrapper(n.ResultWrapper) {
			fresh := true
			for _, wrapper := range n.ParamWrappers {
				fresh = fresh && wrapper == nil
			}
			if fresh {
				if len(args) != 1 {
					return NativeStorage{}, fmt.Errorf("empty native storage must be freshly allocated from Unit")
				}
				unit, ok := args[0].(*TCon)
				if !ok || unit.Name != "()" {
					return NativeStorage{}, fmt.Errorf("empty native storage must be freshly allocated from Unit, never retrieved by an untyped key")
				}
				return NativeStorage{Kind: "new", Handle: -1, Payload: -1}, nil
			}
		}
		return NativeStorage{}, nil
	}
	var index Type
	if payload >= 0 {
		index = args[payload]
	} else {
		index = rest
	}
	storage := func(t Type, wrapper *CtorInfo) bool {
		con, ok := t.(*TCon)
		return ok && len(con.Args) == 1 && Equal(con.Args[0], index) && opaqueWrapper(wrapper)
	}
	handle := -1
	for i, t := range args {
		if i < len(n.ParamWrappers) && storage(t, n.ParamWrappers[i]) {
			if handle != -1 {
				return NativeStorage{}, fmt.Errorf("native storage accepts one handle")
			}
			handle = i
		}
	}
	if payload >= 0 && handle == -1 && storage(rest, n.ResultWrapper) {
		return NativeStorage{Kind: "new", Handle: -1, Payload: payload}, nil
	}
	if payload == -1 && handle >= 0 && opaqueResult {
		return NativeStorage{Kind: "read", Handle: handle, Payload: -1}, nil
	}
	if payload >= 0 && handle >= 0 && !opaqueResult {
		if con, ok := rest.(*TCon); ok && (con.Name == "()" || con.Name == "Bool") {
			return NativeStorage{Kind: "write", Handle: handle, Payload: payload}, nil
		}
	}
	return NativeStorage{}, fmt.Errorf("native `%s` requires a same-index opaque storage contract", n.Name)
}

func opaqueWrapper(wrapper *CtorInfo) bool {
	if wrapper == nil || len(wrapper.Fields) != 1 {
		return false
	}
	field, ok := wrapper.Fields[0].(*TCon)
	return ok && field.Name == "Native.Any" && len(field.Args) == 0
}
