package infer

import "maps"

// Checkpoint protects the persistent declaration environment. The fresh-name
// supply intentionally advances across failures; identities are never reused.
func (ck *Checker) Checkpoint() func() {
	classes, methods := maps.Clone(ck.Classes), maps.Clone(ck.Methods)
	adts, ctors, types := maps.Clone(ck.ADTs), maps.Clone(ck.Ctors), maps.Clone(ck.TypeNames)
	vars, workers, sub := maps.Clone(ck.Env.vars), maps.Clone(ck.Workers), maps.Clone(ck.Sub)
	instances, order, pending := ck.Instances, ck.ADTOrder, ck.PendingPreds
	// Checked is the prefix the compile-time evaluator elaborates on demand.
	// Restoring it without telling that evaluator would leave it holding
	// definitions the checker has forgotten, so the two move together.
	derivers, checked := maps.Clone(ck.Derivers), ck.Checked
	return func() {
		ck.Classes, ck.Methods = classes, methods
		ck.ADTs, ck.Ctors, ck.TypeNames = adts, ctors, types
		ck.Env.vars, ck.Workers, ck.Sub = vars, workers, sub
		ck.Instances, ck.ADTOrder, ck.PendingPreds = instances, order, pending
		ck.Derivers, ck.Checked = derivers, checked
		if ck.CompileTimeRollback != nil {
			ck.CompileTimeRollback(len(checked), len(instances))
		}
	}
}
