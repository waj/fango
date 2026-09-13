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
	captures := maps.Clone(ck.CaptureSummaries)
	scopeSpans := maps.Clone(ck.ScopeSpans)
	// Effects, natives, operator fixities, and instance visibility change
	// when a prompt declares an effect or imports a module graph; a failed
	// input must leave none of it behind.
	aliases, effects, effectsByUnique := maps.Clone(ck.Aliases), maps.Clone(ck.Effects), maps.Clone(ck.EffectsByUnique)
	operations, natives, fixity := maps.Clone(ck.Operations), maps.Clone(ck.Natives), maps.Clone(ck.Fixity)
	instanceImports := make(map[string]map[string]bool, len(ck.InstanceImports))
	for owner, visible := range ck.InstanceImports {
		instanceImports[owner] = maps.Clone(visible)
	}
	return func() {
		ck.Classes, ck.Methods = classes, methods
		ck.ADTs, ck.Ctors, ck.TypeNames = adts, ctors, types
		ck.Env.vars, ck.Workers, ck.Sub = vars, workers, sub
		ck.Instances, ck.ADTOrder, ck.PendingPreds = instances, order, pending
		ck.Derivers, ck.Checked = derivers, checked
		ck.CaptureSummaries = captures
		ck.ScopeSpans = scopeSpans
		ck.Aliases, ck.Effects, ck.EffectsByUnique = aliases, effects, effectsByUnique
		ck.Operations, ck.Natives = operations, natives
		// The fixity table is shared with the session's module graph by
		// identity, so it is restored in place rather than replaced.
		for op := range ck.Fixity {
			delete(ck.Fixity, op)
		}
		maps.Copy(ck.Fixity, fixity)
		ck.InstanceImports = instanceImports
		if ck.CompileTimeRollback != nil {
			ck.CompileTimeRollback(len(checked), len(instances))
		}
	}
}
