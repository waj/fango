package infer

import (
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/modules"
)

// InstallPrelude shares the batch resolver and retains executable definitions.
//
// The batch path reaches the default scope by merging Prelude.fango's imports
// into every module's name resolution, and the REPL runs that same resolver
// over each prompt. The checker's own tests have no name resolver, so their
// names arrive at inference with their bare spelling and have to be bound
// unqualified here, on top of the canonical installation. All three read the
// same list, so none can drift from the file.
func (ck *Checker) InstallPrelude() []diag.Error {
	p, errs := modules.Prelude()
	if len(errs) > 0 {
		return errs
	}
	if errs := ck.InstallPreludeModule(p); len(errs) > 0 {
		return errs
	}
	// A type name reaches one of three tables depending on what it names, and
	// the prelude may expose any of them: `Maybe` is an ADT, `Show` a class,
	// `IO` an effect.
	for surface, canonical := range p.Scope.Types {
		if t, ok := ck.TypeNames[canonical]; ok {
			ck.TypeNames[surface] = t
		}
		if class, ok := ck.Classes[canonical]; ok {
			ck.Classes[surface] = class
		}
		if eff, ok := ck.Effects[canonical]; ok {
			ck.Effects[surface] = eff
		}
	}
	for surface, canonical := range p.Scope.Ctors {
		if info, ok := ck.Ctors[canonical]; ok {
			ck.Ctors[surface] = info
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

// InstallPreludeModule checks the resolved prelude closure into the checker
// under canonical names only. The REPL uses this form: its resolver
// canonicalizes every prompt, so a surface spelling in the tables would only
// let an unresolved name slip past the scope the resolver enforces.
func (ck *Checker) InstallPreludeModule(p *modules.PreludeResult) []diag.Error {
	ck.Fixity = p.Fixities
	ck.PreludeOwners = p.Owners
	infos, errs := ck.Module(p.Module)
	if len(errs) > 0 {
		return errs
	}
	ck.PreludeInfos = infos
	ck.CurrentOwner = ""
	return nil
}
