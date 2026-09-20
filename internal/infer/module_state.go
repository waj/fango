package infer

import (
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

// ModuleRole controls obligations that belong to the selected program entry,
// rather than to a declaration merely spelled main.
type ModuleRole uint8

const (
	DependencyModule ModuleRole = iota
	EntryModule
)

type ModuleOptions struct {
	Name  string
	Role  ModuleRole
	Entry string
}

// ValidateModuleStates performs compatibility checks whose validity belongs
// to the assembled graph rather than to any one module object. It is also the
// installation-time gate for individually valid objects in M4.
func ValidateModuleStates(states []*ModuleState) []diag.Error {
	var errs []diag.Error
	derivers := map[string]*DeriverInfo{}
	var instances []*InstanceInfo
	for _, state := range states {
		for class, deriver := range state.Derivers {
			if old := derivers[class]; old != nil {
				errs = append(errs, diag.Errorf(deriver.Span, "DUPLICATE DERIVER", "Class `%s` already has a deriver, declared at %v.", types.SurfaceName(class), old.Span.StartPos()))
			} else {
				derivers[class] = deriver
			}
		}
		for _, in := range state.Instances {
			for _, old := range instances {
				if old.Class.Name != in.Class.Name {
					continue
				}
				if why := instanceOverlap(old, in.Head, in.Preds); why != "" {
					errs = append(errs, diag.Errorf(in.Span, "OVERLAPPING INSTANCE", "Instance `%s` %s", types.ShowPred(in.Class.Name, in.Head), why))
				}
			}
			if _, blanket := in.Head.(*types.TVar); blanket {
				ck := &Checker{Instances: instances}
				if cycle := ck.blanketCycle(in.Class.Name, in.Preds); len(cycle) != 0 {
					errs = append(errs, diag.Errorf(in.Span, "INSTANCE CONTEXT", "Circular blanket instance requirements: %s.", strings.Join(cycle, " -> ")))
				}
			}
			instances = append(instances, in)
		}
	}
	return errs
}

// DeclRef is stable across checker sessions. It names a declaration by module
// and source-order position instead of by an index into a session-global table.
type DeclRef struct {
	Module string
	Index  int
}

// ModuleState is the immutable declaration-state delta produced by checking
// one module. AST-keyed inference workspaces and the current substitution are
// deliberately absent.
type ModuleState struct {
	Name       string
	Role       ModuleRole
	Entry      string
	Schemes    map[string]types.Scheme
	Types      map[string]types.Type
	Aliases    map[string]string
	Ctors      map[string]*types.CtorInfo
	ADTs       []*types.ADTInfo
	Effects    map[string]*types.EffectInfo
	Operations map[string]*types.EffectOp
	Classes    map[string]*types.ClassInfo
	Methods    map[string]*types.MethodInfo
	Instances  []*InstanceInfo
	Workers    map[string]int
	Natives    map[string]*types.NativeInfo
	Intrinsics map[string]types.Scheme
	Derivers   map[string]*DeriverInfo
	Captures   map[string]types.CaptureSummary
	Visibility map[string]bool
}

// CheckedModule keeps the transient typed-AST handoff separate from the
// installable declaration state. M4 replaces Infos with decoded stage/runtime
// Core when loading a cached object.
type CheckedModule struct {
	State *ModuleState
	Infos []DeclInfo
}

type moduleSnapshot struct {
	env, types, aliases, ctors, effects, operations, classes, methods map[string]bool
	workers, natives, intrinsics, derivers, captures                  map[string]bool
	adts, instances                                                   int
}

func keys[T any](m map[string]T) map[string]bool {
	r := make(map[string]bool, len(m))
	for k := range m {
		r[k] = true
	}
	return r
}

func (ck *Checker) moduleSnapshot() moduleSnapshot {
	return moduleSnapshot{keys(ck.Env.vars), keys(ck.TypeNames), keys(ck.Aliases), keys(ck.Ctors), keys(ck.Effects), keys(ck.Operations), keys(ck.Classes), keys(ck.Methods), keys(ck.Workers), keys(ck.Natives), keys(ck.Intrinsics), keys(ck.Derivers), keys(ck.CaptureSummaries), len(ck.ADTOrder), len(ck.Instances)}
}

func delta[T any](m map[string]T, before map[string]bool) map[string]T {
	r := map[string]T{}
	for k, v := range m {
		if !before[k] {
			r[k] = v
		}
	}
	return r
}

// CheckModule checks one module against declaration state already installed in
// ck and snapshots only the solved state it contributed.
func (ck *Checker) CheckModule(m *ast.Module, options ModuleOptions) (*CheckedModule, []diag.Error) {
	before := ck.moduleSnapshot()
	oldEntry, oldModule := ck.EntryName, ck.moduleName
	ck.moduleName = options.Name
	if options.Role == EntryModule {
		ck.EntryName = options.Entry
	} else {
		ck.EntryName = ""
	}
	infos, errs := ck.Module(m)
	ck.EntryName, ck.moduleName = oldEntry, oldModule
	if len(errs) > 0 {
		return nil, errs
	}
	schemes := delta(ck.Env.vars, before.env)
	for name, scheme := range schemes {
		scheme.Body = ck.Sub.Apply(scheme.Body)
		for i := range scheme.Preds {
			scheme.Preds[i].Ty = ck.Sub.Apply(scheme.Preds[i].Ty)
		}
		schemes[name] = scheme
	}
	visibility := map[string]bool{}
	for name, visible := range ck.InstanceImports[options.Name] {
		visibility[name] = visible
	}
	instances := make([]*InstanceInfo, 0, len(ck.Instances)-before.instances)
	for _, instance := range ck.Instances[before.instances:] {
		copyInstance := *instance
		copyInstance.Limit = 0
		copyInstance.Cutoff = append([]DeclRef(nil), instance.Cutoff...)
		instances = append(instances, &copyInstance)
	}
	state := &ModuleState{
		Name: options.Name, Role: options.Role, Entry: options.Entry,
		Schemes: schemes, Types: delta(ck.TypeNames, before.types), Aliases: delta(ck.Aliases, before.aliases),
		Ctors: delta(ck.Ctors, before.ctors), ADTs: append([]*types.ADTInfo(nil), ck.ADTOrder[before.adts:]...),
		Effects: delta(ck.Effects, before.effects), Operations: delta(ck.Operations, before.operations),
		Classes: delta(ck.Classes, before.classes), Methods: delta(ck.Methods, before.methods),
		Instances: instances, Workers: delta(ck.Workers, before.workers),
		Natives: delta(ck.Natives, before.natives), Intrinsics: delta(ck.Intrinsics, before.intrinsics),
		Derivers: delta(ck.Derivers, before.derivers), Captures: delta(ck.CaptureSummaries, before.captures),
		Visibility: visibility,
	}
	return &CheckedModule{State: state, Infos: append([]DeclInfo(nil), infos...)}, nil
}

// CompleteModuleState returns a new state value containing the capture
// summaries installed by owner-scoped elaboration.
func (ck *Checker) CompleteModuleState(state *ModuleState) *ModuleState {
	copyState := *state
	copyState.Captures = make(map[string]types.CaptureSummary, len(state.Captures))
	for name, summary := range state.Captures {
		copyState.Captures[name] = summary
	}
	for name := range state.Schemes {
		if summary, ok := ck.CaptureSummaries[name]; ok {
			copyState.Captures[name] = summary
		}
	}
	return &copyState
}
