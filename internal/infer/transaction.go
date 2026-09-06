package infer

import "maps"

// Checkpoint protects the persistent declaration environment. The fresh-name
// supply intentionally advances across failures; identities are never reused.
func (ck *Checker) Checkpoint() func() {
	classes, methods := maps.Clone(ck.Classes), maps.Clone(ck.Methods)
	adts, ctors, types := maps.Clone(ck.ADTs), maps.Clone(ck.Ctors), maps.Clone(ck.TypeNames)
	vars, workers, sub := maps.Clone(ck.Env.vars), maps.Clone(ck.Workers), maps.Clone(ck.Sub)
	instances, order, pending := ck.Instances, ck.ADTOrder, ck.PendingPreds
	return func() {
		ck.Classes, ck.Methods = classes, methods
		ck.ADTs, ck.Ctors, ck.TypeNames = adts, ctors, types
		ck.Env.vars, ck.Workers, ck.Sub = vars, workers, sub
		ck.Instances, ck.ADTOrder, ck.PendingPreds = instances, order, pending
	}
}
