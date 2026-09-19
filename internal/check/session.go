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
	staging.Install(ck)
	var defs, installed []core.Def
	var infos []infer.DeclInfo
	states := make([]*infer.ModuleState, 0, len(loaded.Modules))
	for _, module := range loaded.Modules {
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
		defs = append(defs, owned...)
		installed = append(installed, owned...)
	}
	prog, entryErrs := elaborate.AssembleModuleProgram(defs, infos, loaded.Entry, ck)
	if len(entryErrs) != 0 {
		return nil, entryErrs, nil
	}
	return &Result{Program: prog, Checker: ck, Graph: loaded, States: states}, nil, nil
}
