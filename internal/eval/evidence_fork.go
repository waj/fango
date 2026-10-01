package eval

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

func installEvidenceOrigin(ev *evidence) {
	ev.origin = &fangort.EvidenceOrigin{Name: ev.handler.Effect.Name, Arguments: ev.typeArgs}
	if len(ev.handler.Clauses) > 0 && ev.handler.Clauses[0].Op.Abort {
		return
	}
	deps := map[types.EffectKey]core.EffectInstance{}
	rows := map[types.CaptureVar]bool{}
	for _, clause := range ev.handler.Clauses {
		for id, dependency := range core.FreeEvidence(clause.Body) {
			deps[id] = dependency
		}
		for id := range core.FreeRows(clause.Body) {
			rows[id] = true
		}
	}
	ev.origin.Share = func() {
		ev.state.Share()
		for id := range deps {
			if parent := resolveEvidence(ev.outer[id]); parent != nil {
				fangort.ShareOrigin(parent.origin)
			}
		}
		for id := range rows {
			if row, ok := ev.frame.row(id); ok {
				fangort.ShareEvidenceRow(row)
			}
		}
	}
	ev.origin.Rebuild = func(fork *fangort.EvidenceFork) fangort.EvidenceFamily {
		child := *ev
		child.outer = make(map[types.EffectKey]*evidence, len(deps))
		for id := range deps {
			parent := resolveEvidence(ev.outer[id])
			child.outer[id] = fangort.ForkEvidence[*evidence](fork, parent.origin, fangort.DirectEvidence)
		}
		if len(rows) > 0 {
			bindings := rowEnv{}
			for id := range rows {
				row, ok := ev.frame.row(id)
				if !ok {
					panic("eval: missing captured handler row")
				}
				bindings[id] = fork.Row(row)
			}
			child.frame = &Frame{parent: ev.frame, rows: bindings}
		}
		installEvidenceOrigin(&child)
		return fangort.EvidenceFamily{Origin: child.origin, Direct: &child, Exit: &child}
	}
}

func projectedEvidenceOrigin(ev *evidence, name string) *fangort.EvidenceOrigin {
	if ev.origin != nil {
		return ev.origin
	}
	return &fangort.EvidenceOrigin{Name: name, Resolve: func() *fangort.EvidenceOrigin {
		return resolveEvidence(ev).origin
	}}
}
