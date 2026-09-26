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
		ev := in.evidence[effect.Unique]
		if ev == nil {
			return nil, fmt.Errorf("eval: missing residual evidence %s", effect.Name)
		}
		bindings[i] = fangort.EvidenceBinding{Name: strconv.Itoa(effect.Unique), Family: fangort.EvidenceFamily{Direct: ev, Exit: ev}}
	}
	return fangort.ExtendEvidenceRow(tail, bindings...), nil
}

func bindInvocationRow(param types.CaptureVar, effects []core.EffectInstance, row *fangort.EvidenceRow, evidenceMap map[int]*evidence) rowEnv {
	if param == 0 {
		return nil
	}
	for _, effect := range effects {
		evidenceMap[effect.Unique] = &evidence{row: row, rowEffect: effect.Unique}
	}
	return rowEnv{param: row}
}

func resolveEvidence(ev *evidence) *evidence {
	for ev != nil && ev.rowEffect != 0 {
		ev = fangort.RowEvidence[*evidence](ev.row, strconv.Itoa(ev.rowEffect), fangort.DirectEvidence)
	}
	return ev
}

func (in *interp) plainClosure(lam *core.Lambda, fr *Frame) *Closure {
	return &Closure{Param: lam.Param, Body: lam.Body, Env: fr, Evidence: in.closureEvidence(lam),
		control: types.FunctionControl(lam.Ty.(*types.TFun)), rowParam: lam.RowParam, rowEffects: lam.RowEffects}
}
