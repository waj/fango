// Package check owns the dependency-first semantic compilation session shared
// by batch clients and fresh REPL import sessions.
package check

import (
	"github.com/waj/fango/internal/compilecache"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
)

type Observer func(stage, owner string)

type Session struct {
	Observe            Observer
	Cache              ObjectCache
	DisableObjectCache bool
}

type Result struct {
	Program *core.Prog
	Checker *infer.Checker
	Graph   *modules.Result
	States  []*infer.ModuleState
	Objects []*ModuleObject
}

// Compile discovers a graph and checks, elaborates, and semantically lints one
// owner at a time. Diagnostic errors are source-facing; internalErr denotes a
// violated compiler invariant and is kept separate from rendering.
func (s *Session) Compile(entry string) (*Result, []diag.Error, error) {
	loaded, errs := modules.LoadWithOptions(entry, modules.LoadOptions{Observe: modules.StageObserver(s.Observe)})
	if len(errs) != 0 {
		return nil, errs, nil
	}
	sup := &types.Supply{}
	ck := infer.NewChecker(sup, types.NewBuiltins(sup), infer.NewEnv())
	ck.Fixity, ck.EntryName = loaded.Fixity, loaded.Entry
	stageSession := staging.Install(ck)
	stageSession.Observe(staging.Observer(s.Observe))
	objectCache := s.Cache
	if objectCache == nil && !s.DisableObjectCache {
		objectCache = compilecache.NewModuleStore(entry)
	}
	installer := NewInstaller(ck, stageSession, objectCache, s.Observe)
	defs, diagnostics, err := installer.Install(loaded.Modules, loaded.FixityHash)
	if len(diagnostics) != 0 || err != nil {
		return nil, diagnostics, err
	}
	prog, entryErrs := elaborate.AssembleModuleProgram(defs, installer.Infos(), loaded.Entry, ck)
	if len(entryErrs) != 0 {
		return nil, entryErrs, nil
	}
	return &Result{Program: prog, Checker: ck, Graph: loaded, States: installer.States(), Objects: installer.Objects()}, nil, nil
}

func nominalNames(ck *infer.Checker) map[int]string {
	out := map[int]string{}
	for _, adt := range ck.ADTs {
		out[adt.Con.Unique] = adt.Con.Name
	}
	for _, ty := range ck.TypeNames {
		if con, ok := ty.(*types.TCon); ok {
			out[con.Unique] = con.Name
		}
	}
	return out
}

func effectNames(ck *infer.Checker) map[int]string {
	out := map[int]string{}
	for id, effect := range ck.EffectsByUnique {
		out[id] = effect.Name
	}
	return out
}
