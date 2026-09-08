package infer

import (
	"strings"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/modules"
)

// InstallPrelude shares the batch resolver and retains executable definitions.
func (ck *Checker) InstallPrelude() []diag.Error {
	m, fixities, errs := modules.Prelude()
	if len(errs) > 0 {
		return errs
	}
	ck.Fixity = fixities
	infos, errs := ck.Module(m)
	if len(errs) > 0 {
		return errs
	}
	ck.PreludeInfos = infos
	for _, name := range []string{"Num", "Eq", "Ord", "Show"} {
		ck.Classes[name] = ck.Classes["Basics."+name]
	}
	for _, name := range []string{"print", "readLine"} {
		canonical := "IO." + name
		ck.Aliases[name] = canonical
		if sch, ok := ck.Env.Lookup(canonical); ok {
			ck.Env.Bind(name, sch)
		}
		if op := ck.Operations[canonical]; op != nil {
			ck.Operations[name] = op
		}
		if arity, ok := ck.Workers[canonical]; ok {
			ck.Workers[name] = arity
		}
	}
	if sch, ok := ck.Env.Lookup("Basics.show"); ok {
		ck.Env.Bind("show", sch)
		ck.Methods["show"] = ck.Methods["Basics.show"]
	}
	// Every operator Basics declares is ambient, matching what the batch
	// resolver injects into each module. The REPL and the checker's unit
	// tests have no name resolver, so an operator reaches inference with
	// its bare spelling and has to be bound unqualified here.
	for _, name := range ck.Env.Names() {
		surface, ok := strings.CutPrefix(name, "Basics.")
		if !ok || !isOperatorName(surface) {
			continue
		}
		sch, found := ck.Env.Lookup(name)
		if !found {
			continue
		}
		ck.Aliases[surface] = name
		ck.Env.Bind(surface, sch)
		if m := ck.Methods[name]; m != nil {
			ck.Methods[surface] = m
		}
		if arity, has := ck.Workers[name]; has {
			ck.Workers[surface] = arity
		}
	}
	if ck.IO != nil {
		ck.Effects["IO"] = ck.IO
	}
	ck.CurrentOwner = ""
	return nil
}
