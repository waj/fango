package eval

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func (in *interp) closureEvidence(lam *core.Lambda) map[types.EffectKey]*evidence {
	result := map[types.EffectKey]*evidence{}
	for id := range core.FreeEvidence(lam) {
		if ev := in.evidence[id]; ev != nil {
			result[id] = ev
		}
	}
	return result
}
