package eval

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

func (in *interp) inspectFailure(e *core.FailureInspect, fr *Frame) (Value, error) {
	args := make([]Value, len(e.Args))
	for i, arg := range e.Args {
		v, err := in.eval(arg, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(v); ok {
			return v, nil
		}
		args[i] = v
	}
	failure := args[len(args)-1].(*fangort.Failure)
	switch e.Name {
	case types.FailureEffectName:
		return failure.Effect(), nil
	case types.FailureOperationName:
		return failure.Operation(), nil
	case types.FailureArgumentCountName:
		return failure.ArgumentCount(), nil
	case types.FailureSuppressedName:
		return failureList(failure.Suppressed()), nil
	case types.FailureArgumentName:
		descriptor, err := in.typeDescriptor(e.Ty.(*types.TCon).Args[0], fr)
		if err != nil {
			return nil, err
		}
		payload, ok := fangort.FailureArgument[Value](args[0].(int64), failure, descriptor)
		if ok {
			return &CtorVal{Ctor: e.Result.Ctors[1], Fields: []Value{payload}}, nil
		}
		return &CtorVal{Ctor: e.Result.Ctors[0]}, nil
	}
	panic("eval: invalid failure inspection")
}

func failureList(values fangort.List[*fangort.Failure]) fangort.List[Value] {
	var output []Value
	for !values.IsEmpty() {
		output = append(output, values.Head())
		values = values.Tail()
	}
	result := fangort.ListNil[Value]()
	for i := len(output) - 1; i >= 0; i-- {
		result = fangort.ListCons(output[i], result)
	}
	return result
}
