package eval

import (
	"fmt"

	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

// sidecarCall runs one call-form native in the worker and applies the
// declaration's boundary shapes (doc/design.md, "Go backend and runtime"):
// wrapper arguments are erased to their scalar field, a wrapper result is
// rebuilt around the returned scalar, and a fallible native's outcome becomes
// an ordinary `Result` value. The constructors come from the checked
// declaration, so this mirrors generated Go rather than re-deriving anything.
func (in *interp) sidecarCall(executor NativeCaller, key string, n *types.NativeInfo, args []Value) (Value, error) {
	wire := args
	if n != nil {
		copied := false
		if (n.Storage.Kind == "new" || n.Storage.Kind == "write") && n.Storage.Payload >= 0 {
			wire = append([]Value(nil), args...)
			copied = true
			wire[n.Storage.Payload] = fangort.PackNativeValue(args[n.Storage.Payload])
		}
		for i, wrapper := range n.ParamWrappers {
			if wrapper == nil || i >= len(args) {
				continue
			}
			if !copied {
				wire = append([]Value(nil), args...)
				copied = true
			}
			cv, ok := args[i].(*CtorVal)
			if !ok || cv.Ctor != wrapper || len(cv.Fields) != 1 {
				return nil, fmt.Errorf("eval: native %s argument %d is not a %s value", key, i+1, types.SurfaceName(wrapper.Name))
			}
			wire[i] = cv.Fields[0]
		}
	}
	v, err := executor.Call(in.ctx, in.ioctx, key, wire)
	if err != nil || n == nil {
		return v, err
	}
	if n.Storage.Kind == "read" {
		return fangort.UnpackNativeValue[any](v), nil
	}
	if failure, ok := v.(fangort.IOFailure); ok {
		if n.Fallible == nil {
			return nil, fmt.Errorf("eval: native %s returned a failure but is not declared fallible", key)
		}
		return failureValue(n.Fallible, failure), nil
	}
	if n.ResultWrapper != nil {
		v = &CtorVal{Ctor: n.ResultWrapper, Fields: []Value{v}}
	}
	if n.Fallible != nil {
		return &CtorVal{Ctor: n.Fallible.Ok, Fields: []Value{v}}, nil
	}
	return v, nil
}

// failureValue builds `Err (IO.Error { kind, path, message })` from a
// classified failure, the interpreter's twin of the generated Go branch.
func failureValue(shape *types.FallibleShape, failure fangort.IOFailure) Value {
	fields := make([]Value, 3)
	fields[shape.KindIdx] = &CtorVal{Ctor: shape.Kinds[failure.Kind]}
	fields[shape.LocationIdx] = failure.Path
	fields[shape.MessageIdx] = failure.Message
	return &CtorVal{Ctor: shape.Err, Fields: []Value{&CtorVal{Ctor: shape.Error, Fields: fields}}}
}
