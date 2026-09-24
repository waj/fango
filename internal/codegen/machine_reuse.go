package codegen

import (
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
)

// Re-entry uses the same concrete frame type. A fresh Step activation preserves
// snapshots captured by closures from earlier iterations; a Go local loop would
// need a separate capture proof. All evidence and arguments are still evaluated.
func (g *gen) reusesMachineFrame(w *machineir.Worker, call *machineir.Call) bool {
	if !w.Optimized || !call.Tail || call.Capture || call.Operation != nil || call.Callee != w.Name || w.StateToken || len(call.TyArgs) != len(w.TyParams) {
		return false
	}
	for i, arg := range call.TyArgs {
		v, ok := arg.(*types.TVar)
		if !ok || v.ID != w.TyParams[i].ID {
			return false
		}
	}
	return true
}
