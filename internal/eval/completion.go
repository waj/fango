package eval

import (
	"fmt"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
	"strconv"
)

type completionValue struct {
	value    Value
	failure  *ExitRequest
	snapshot *fangort.Failure
}

func detachCompletion(value Value) *completionValue {
	if exit, ok := asExit(value); ok {
		return &completionValue{failure: detachCompletionExit(exit)}
	}
	return &completionValue{value: value}
}
func detachCompletionExit(exit *ExitRequest) *ExitRequest {
	if exit == nil {
		return nil
	}
	copy := &ExitRequest{Op: exit.Op, Payload: append([]Value(nil), exit.Payload...), PayloadTypes: append([]*fangort.TypeDescriptor(nil), exit.PayloadTypes...)}
	for _, child := range exit.Suppressed {
		copy.Suppressed = append(copy.Suppressed, detachCompletionExit(child))
	}
	return copy
}

func (in *interp) evalCompletion(e *core.Completion, fr *Frame) (Value, error) {
	value, err := in.eval(e.Value, fr)
	if err != nil {
		return nil, err
	}
	if _, ok := asExit(value); ok {
		return value, nil
	}
	if types.CapturesCompletion(e.Name) {
		fn, ok := value.(*Closure)
		if !ok {
			return nil, fmt.Errorf("eval: completion capture requires callback")
		}
		row, err := in.argumentRow(e.Row, fr)
		if err != nil {
			return nil, err
		}
		callEvidence := cloneEvidence(fn.Evidence)
		rows := bindInvocationRow(fn.rowParam, fn.rowEffects, row, callEvidence)
		old := in.evidence
		in.evidence = callEvidence
		result, err := in.eval(fn.Body, &Frame{parent: fn.Env, vars: map[string]Value{fn.Param: struct{}{}}, rows: rows})
		in.evidence = old
		if err != nil {
			return nil, err
		}
		return detachCompletion(result), nil
	}
	if e.Name == types.CompletionDropSuspensionName || e.Name == types.CompletionDropDriveName {
		return value, nil
	}
	if e.Name == types.CompletionFromFailureName {
		failure, ok := value.(*fangort.Failure)
		if !ok || failure == nil {
			return nil, fmt.Errorf("eval: invalid detached completion failure")
		}
		return &completionValue{snapshot: failure}, nil
	}
	completion, ok := value.(*completionValue)
	if !ok {
		return nil, fmt.Errorf("eval: invalid completion representation")
	}
	if e.Name == types.CompletionFailureName {
		if completion.failure == nil && completion.snapshot == nil {
			return &CtorVal{Ctor: e.Result.Ctors[0]}, nil
		}
		if completion.snapshot != nil {
			return &CtorVal{Ctor: e.Result.Ctors[1], Fields: []Value{completion.snapshot}}, nil
		}
		return &CtorVal{Ctor: e.Result.Ctors[1], Fields: []Value{snapshotFailure(completion.failure)}}, nil
	}
	if completion.failure == nil && completion.snapshot == nil {
		return completion.value, nil
	}
	row, err := in.argumentRow(e.Row, fr)
	if err != nil {
		return nil, err
	}
	exit := detachCompletionExit(completion.failure)
	if completion.snapshot != nil {
		exit, err = in.exitFromFailure(completion.snapshot)
		if err != nil {
			return nil, err
		}
	}
	if exit.Op == nil || exit.Op.Owner == nil || len(exit.Payload) != len(exit.Op.ParamTypes) || len(exit.PayloadTypes) != len(exit.Payload) {
		return nil, fmt.Errorf("eval: stale completion operation proof")
	}
	target := resolveEvidence(fangort.RowEvidence[*evidence](row, strconv.Itoa(exit.Op.Owner.Unique), fangort.MachineEvidence))
	if target == nil || target.handler == nil || target.handler.Effect.Unique != exit.Op.Owner.Unique {
		return nil, fmt.Errorf("eval: completion has no matching target")
	}
	sub := map[int]types.Type{}
	for i, param := range exit.Op.Owner.Params {
		sub[param.ID] = target.handler.Effect.Args[i]
	}
	for i, param := range exit.Op.ParamTypes {
		descriptor, err := in.typeDescriptor(types.SubstRigid(param, sub), target.frame)
		if err != nil {
			return nil, err
		}
		// The evaluator uses one Value representation; descriptor equality is
		// the same complete nominal proof checked by generated typed adapters.
		_ = fangort.CompletionPayload[Value](snapshotFailure(exit), i, descriptor)
	}
	exit.Target = target
	return exit, nil
}

func (in *interp) exitFromFailure(failure *fangort.Failure) (*ExitRequest, error) {
	detached := fangort.DetachedFailureExit(failure)
	if detached == nil {
		return nil, fmt.Errorf("eval: missing detached failure")
	}
	var operation *types.EffectOp
	for _, effect := range in.env.effects {
		if effect.Name == detached.Effect && detached.Operation >= 0 && detached.Operation < len(effect.Ops) {
			candidate := effect.Ops[detached.Operation]
			if candidate.Name == detached.OperationName {
				operation = candidate
				break
			}
		}
	}
	if operation == nil {
		return nil, fmt.Errorf("eval: stale detached failure operation proof")
	}
	result := &ExitRequest{Op: operation, Payload: detached.Payload, PayloadTypes: detached.PayloadTypes}
	var convert func(*fangort.ExitRequest) *ExitRequest
	convert = func(value *fangort.ExitRequest) *ExitRequest {
		if value == nil {
			return nil
		}
		var op *types.EffectOp
		for _, effect := range in.env.effects {
			if effect.Name == value.Effect && value.Operation >= 0 && value.Operation < len(effect.Ops) && effect.Ops[value.Operation].Name == value.OperationName {
				op = effect.Ops[value.Operation]
				break
			}
		}
		out := &ExitRequest{Op: op, Payload: value.Payload, PayloadTypes: value.PayloadTypes}
		for _, child := range value.Suppressed {
			out.Suppressed = append(out.Suppressed, convert(child))
		}
		return out
	}
	for _, child := range detached.Suppressed {
		result.Suppressed = append(result.Suppressed, convert(child))
	}
	return result, nil
}
