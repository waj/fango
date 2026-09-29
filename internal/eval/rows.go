package eval

import (
	"fmt"
	"strconv"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

type rowEnv map[types.CaptureVar]*fangort.EvidenceRow

func (f *Frame) row(id types.CaptureVar) (*fangort.EvidenceRow, bool) {
	for frame := f; frame != nil; frame = frame.parent {
		if row, ok := frame.rows[id]; ok {
			return row, true
		}
	}
	return nil, false
}

func (in *interp) argumentRow(argument *core.RowArgument, fr *Frame) (*fangort.EvidenceRow, error) {
	if argument == nil {
		return nil, nil
	}
	var tail *fangort.EvidenceRow
	if argument.From != 0 {
		var found bool
		tail, found = fr.row(argument.From)
		if !found {
			return nil, fmt.Errorf("eval: missing residual row %d", argument.From)
		}
	}
	bindings := make([]fangort.EvidenceBinding, len(argument.Effects))
	for i, effect := range argument.Effects {
		ev := in.evidence[effect.Key()]
		if ev == nil {
			return nil, fmt.Errorf("eval: missing residual evidence %s", effect.Name)
		}
		args := make([]*fangort.TypeDescriptor, len(effect.Args))
		for j, arg := range effect.Args {
			var err error
			args[j], err = in.typeDescriptor(arg, fr)
			if err != nil {
				return nil, err
			}
		}
		bindings[i] = fangort.EvidenceBinding{Name: strconv.Itoa(effect.Unique), Arguments: args, Family: fangort.EvidenceFamily{Origin: projectedEvidenceOrigin(ev, effect.Name), Direct: ev, Exit: ev}}
	}
	return fangort.ExtendEvidenceRow(tail, bindings...), nil
}

func (in *interp) bindInvocationRow(param types.CaptureVar, effects []core.EffectInstance, row *fangort.EvidenceRow, evidenceMap map[types.EffectKey]*evidence, fr *Frame) (rowEnv, error) {
	if param == 0 {
		return nil, nil
	}
	for _, effect := range effects {
		args := make([]*fangort.TypeDescriptor, len(effect.Args))
		for i, arg := range effect.Args {
			var err error
			args[i], err = in.typeDescriptor(arg, fr)
			if err != nil {
				return nil, err
			}
		}
		evidenceMap[effect.Key()] = &evidence{row: row, rowEffect: strconv.Itoa(effect.Unique), rowArgs: args}
	}
	return rowEnv{param: row}, nil
}

func resolveEvidence(ev *evidence) *evidence {
	for ev != nil && ev.rowEffect != "" {
		ev = fangort.RowEvidence[*evidence](ev.row, ev.rowEffect, fangort.DirectEvidence, ev.rowArgs...)
	}
	return ev
}

func (in *interp) plainClosure(lam *core.Lambda, fr *Frame) *Closure {
	return &Closure{Param: lam.Param, Body: lam.Body, Env: fr, Evidence: in.closureEvidence(lam), effectParams: lam.EffectParams,
		control: types.FunctionControl(lam.Ty.(*types.TFun)), rowParam: lam.RowParam, rowEffects: lam.RowEffects}
}
