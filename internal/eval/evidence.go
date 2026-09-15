package eval

import "github.com/waj/fango/internal/core"

func (in *interp) closureEvidence(lam *core.Lambda) map[int]*evidence {
	result := map[int]*evidence{}
	for id := range core.FreeEvidence(lam) {
		if ev := in.evidence[id]; ev != nil {
			result[id] = ev
		}
	}
	return result
}
