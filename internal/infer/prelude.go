package infer

import (
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/modules"
)

// InstallPrelude shares the batch resolver and retains executable definitions.
func (ck *Checker) InstallPrelude() []diag.Error {
	m, errs := modules.Prelude()
	if len(errs) > 0 {
		return errs
	}
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
	if ck.IO != nil {
		ck.Effects["IO"] = ck.IO
	}
	ck.CurrentOwner = ""
	return nil
}
