// Package check owns the dependency-first semantic compilation session shared
// by batch clients and fresh REPL import sessions.
package check

import (
	"github.com/waj/fango/internal/compilecache"
	"github.com/waj/fango/internal/compileevent"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
)

type Observer = compileevent.Observer

type Session struct {
	Observe            Observer
	Cache              ObjectCache
	DisableObjectCache bool
	LoadOptions        modules.LoadOptions
	// FreshSources bypasses the checked-object cache for these source names,
	// retaining the checker's transient AST type and record-use information.
	FreshSources map[string]bool
	// AccumulateDiagnostics checks modules independent of a failed owner.
	// Batch commands retain their existing fail-fast behavior.
	AccumulateDiagnostics bool
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
	options := s.LoadOptions
	options.Observe = modules.StageObserver(s.Observe)
	loaded, errs := modules.LoadWithOptions(entry, options)
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
	installer.FreshSources = s.FreshSources
	var defs []core.Def
	var diagnostics []diag.Error
	var err error
	if s.AccumulateDiagnostics {
		defs, diagnostics, err = installer.InstallCollect(loaded.Modules, loaded.FixityHash)
	} else {
		defs, diagnostics, err = installer.Install(loaded.Modules, loaded.FixityHash)
	}
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
