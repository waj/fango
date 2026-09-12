package infer

import (
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/modules"
)

// InstallPrelude shares the batch resolver and retains executable definitions.
//
// The batch path reaches the default scope by merging Prelude.fango's imports
// into every module's name resolution. The REPL and the checker's own tests
// have no name resolver, so a prompt's names arrive at inference with their
// bare spelling and have to be bound unqualified here. Both read the same
// list, so neither can drift from the file.
func (ck *Checker) InstallPrelude() []diag.Error {
	p, errs := modules.Prelude()
	if len(errs) > 0 {
		return errs
	}
	ck.Fixity = p.Fixities
	ck.PreludeOwners = p.Owners
	infos, errs := ck.Module(p.Module)
	if len(errs) > 0 {
		return errs
	}
	ck.PreludeInfos = infos
	for surface, canonical := range p.Scope.Types {
		if class, ok := ck.Classes[canonical]; ok {
			ck.Classes[surface] = class
		}
		if eff, ok := ck.Effects[canonical]; ok {
			ck.Effects[surface] = eff
		}
	}
	for surface, canonical := range p.Scope.Values {
		ck.Aliases[surface] = canonical
		if sch, ok := ck.Env.Lookup(canonical); ok {
			ck.Env.Bind(surface, sch)
		}
		if m := ck.Methods[canonical]; m != nil {
			ck.Methods[surface] = m
		}
		if op := ck.Operations[canonical]; op != nil {
			ck.Operations[surface] = op
		}
		if arity, ok := ck.Workers[canonical]; ok {
			ck.Workers[surface] = arity
		}
	}
	ck.CurrentOwner = ""
	return nil
}
