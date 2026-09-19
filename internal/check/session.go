// Package check owns the dependency-first semantic compilation session shared
// by batch clients and, eventually, fresh REPL import sessions.
package check

import (
	"fmt"
	"sort"
	"strings"

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
	Observe Observer
}

type Result struct {
	Program *core.Prog
	Checker *infer.Checker
	Graph   *modules.Result
	States  []*infer.ModuleState
	Objects []*ModuleObject
}

func (s *Session) event(stage, owner string) {
	if s != nil && s.Observe != nil {
		s.Observe(stage, owner)
	}
}

// Compile discovers a graph and checks, elaborates, and semantically lints one
// owner at a time. Diagnostic errors are source-facing; internalErr denotes a
// violated compiler invariant and is kept separate from rendering.
func (s *Session) Compile(entry string) (*Result, []diag.Error, error) {
	loaded, errs := modules.LoadWithOptions(entry, modules.LoadOptions{Observe: modules.StageObserver(s.Observe), Parsed: compilecache.NewParsedStore(entry)})
	if len(errs) != 0 {
		return nil, errs, nil
	}
	sup := &types.Supply{}
	ck := infer.NewChecker(sup, types.NewBuiltins(sup), infer.NewEnv())
	ck.Fixity, ck.EntryName = loaded.Fixity, loaded.Entry
	stageSession := staging.Install(ck)
	var defs, installed []core.Def
	var infos []infer.DeclInfo
	states := make([]*infer.ModuleState, 0, len(loaded.Modules))
	objects := make([]*ModuleObject, 0, len(loaded.Modules))
	for _, module := range loaded.Modules {
		templateStart := ck.Templates.Len()
		owner := module.Name
		if owner == "" {
			owner = "<entry>"
		}
		role := infer.DependencyModule
		if module.Role == modules.EntryRole {
			role = infer.EntryModule
		}
		s.event("check", owner)
		checked, checkErrs := ck.CheckModule(module.Module, infer.ModuleOptions{Name: module.Name, Role: role, Entry: module.Entry})
		if len(checkErrs) != 0 {
			return nil, checkErrs, nil
		}
		infos = append(infos, checked.Infos...)
		intrinsics := make([]string, 0, len(checked.State.Intrinsics))
		for name := range checked.State.Intrinsics {
			intrinsics = append(intrinsics, name)
		}
		sort.Strings(intrinsics)
		s.event("elaborate", owner)
		owned, elabErrs := elaborate.Increment(checked.Infos, checked.State.Instances, intrinsics, installed, ck)
		if len(elabErrs) != 0 {
			return nil, elabErrs, nil
		}
		state := ck.CompleteModuleState(checked.State)
		stageObject, stageErrs := stageSession.Snapshot()
		if len(stageErrs) != 0 {
			return nil, stageErrs, nil
		}
		if compatibilityErrs := infer.ValidateModuleStates(append(states, state)); len(compatibilityErrs) != 0 {
			return nil, compatibilityErrs, nil
		}
		s.event("semantic-lint", owner)
		if lintErrs := elaborate.LintProgIn(owned, installed, ck); len(lintErrs) != 0 {
			parts := make([]string, len(lintErrs))
			for i, err := range lintErrs {
				parts[i] = err.Error()
			}
			return nil, nil, fmt.Errorf("Core invariants violated in module %s:\n  %s", owner, strings.Join(parts, "\n  "))
		}
		states = append(states, state)
		objects = append(objects, &ModuleObject{State: state, Resolver: module.Interface, Nominals: nominalNames(ck), EffectNames: effectNames(ck), Runtime: append([]core.Def(nil), owned...), Stage: stageObject.Defs, StageGroups: stageObject.Groups, TemplateBase: templateStart, Templates: ck.Templates.Snapshot(templateStart)})
		defs = append(defs, owned...)
		installed = append(installed, owned...)
	}
	prog, entryErrs := elaborate.AssembleModuleProgram(defs, infos, loaded.Entry, ck)
	if len(entryErrs) != 0 {
		return nil, entryErrs, nil
	}
	return &Result{Program: prog, Checker: ck, Graph: loaded, States: states, Objects: objects}, nil, nil
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
