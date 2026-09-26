package eval

import (
	"fmt"

	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

type descriptorEnv map[int]*fangort.TypeDescriptor

func (in *interp) typeDescriptor(t types.Type, fr *Frame) (*fangort.TypeDescriptor, error) {
	switch t := t.(type) {
	case *types.TVar:
		for frame := fr; frame != nil; frame = frame.parent {
			if descriptor := frame.types[t.ID]; descriptor != nil {
				return descriptor, nil
			}
		}
		return nil, fmt.Errorf("eval: missing descriptor for type parameter %d", t.ID)
	case *types.TFun:
		return fangort.NominalType("<function>", false), nil
	case *types.TCon:
		args := make([]*fangort.TypeDescriptor, len(t.Args))
		for i, arg := range t.Args {
			descriptor, err := in.typeDescriptor(arg, fr)
			if err != nil {
				return nil, err
			}
			args[i] = descriptor
		}
		name := t.Name
		if in.env.adts[t.Unique] != nil {
			name = fmt.Sprintf("%s#%d", name, t.Unique)
		}
		return fangort.NominalType(name, types.InspectionShapeSafe(t, in.env.adts), args...), nil
	default:
		return nil, fmt.Errorf("eval: invalid descriptor type %T", t)
	}
}

func (in *interp) instantiateDescriptors(params []*types.TVar, args []types.Type, fr *Frame) (descriptorEnv, error) {
	if len(params) != len(args) {
		return nil, fmt.Errorf("eval: type descriptor argument count mismatch")
	}
	result := descriptorEnv{}
	for i, param := range params {
		descriptor, err := in.typeDescriptor(args[i], fr)
		if err != nil {
			return nil, err
		}
		result[param.ID] = descriptor
	}
	return result, nil
}

func snapshotFailure(exit *ExitRequest) *fangort.Failure {
	if exit == nil {
		return nil
	}
	var copyExit func(*ExitRequest) *fangort.ExitRequest
	copyExit = func(exit *ExitRequest) *fangort.ExitRequest {
		result := &fangort.ExitRequest{Payload: exit.Payload, PayloadTypes: exit.PayloadTypes}
		if exit.Op != nil {
			result.OperationName = exit.Op.Name
			result.Operation = exit.Op.Index
			if exit.Op.Owner != nil {
				result.Effect = exit.Op.Owner.Name
			}
		}
		for _, secondary := range exit.Suppressed {
			result.Suppressed = append(result.Suppressed, copyExit(secondary))
		}
		return result
	}
	return fangort.SnapshotFailure(copyExit(exit))
}
