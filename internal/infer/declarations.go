package infer

import (
	"maps"

	"github.com/waj/fango/internal/types"
)

// Declarations exposes the persistent declaration tables, independently of
// transient inference workspaces. The returned tables must be copied before
// editing; this is the same immutability contract as ModuleState.
func (ck *Checker) Declarations() *ModuleState {
	return &ModuleState{Schemes: ck.Env.vars, Types: ck.TypeNames, Aliases: ck.Aliases,
		Ctors: ck.Ctors, ADTs: ck.ADTOrder, Effects: ck.Effects, Operations: ck.Operations,
		Classes: ck.Classes, Methods: ck.Methods, Instances: ck.Instances, Workers: ck.Workers,
		Natives: ck.Natives, Intrinsics: ck.Intrinsics, Derivers: ck.Derivers, Captures: ck.CaptureSummaries}
}

// ReplaceDeclarations adopts a prepared declaration view. Historical nominal
// metadata may remain in ADTs/EffectsByUnique without being source-visible.
func (ck *Checker) ReplaceDeclarations(s *ModuleState) {
	boolADT := ck.ADTs[ck.B.Bool.Unique]
	ck.Env = &Env{vars: s.Schemes}
	ck.TypeNames, ck.Aliases, ck.Ctors = s.Types, s.Aliases, s.Ctors
	ck.ADTOrder, ck.ADTs = s.ADTs, map[int]*types.ADTInfo{}
	ck.ADTs[ck.B.Bool.Unique] = boolADT
	for _, adt := range s.ADTs {
		ck.ADTs[adt.Con.Unique] = adt
	}
	ck.Effects, ck.EffectsByUnique = s.Effects, map[int]*types.EffectInfo{}
	for _, effect := range s.Effects {
		ck.EffectsByUnique[effect.Unique] = effect
	}
	ck.Operations, ck.Classes, ck.Methods = s.Operations, s.Classes, s.Methods
	ck.Instances, ck.Workers, ck.Natives = s.Instances, s.Workers, s.Natives
	ck.Intrinsics, ck.Derivers, ck.CaptureSummaries = s.Intrinsics, s.Derivers, s.Captures
	if ck.IO != nil {
		if effect := ck.Effects[ck.IO.Name]; effect != nil {
			ck.IO = effect
		}
	}
}

// PromptDeclarations selects entries not owned by the installed modules.
// Nominal metadata remains complete: retained prompt types can mention a
// module generation that is no longer visible by source name.
func (ck *Checker) PromptDeclarations(modules []*ModuleState) *ModuleState {
	s := *ck.Declarations()
	s.Schemes = maps.Clone(s.Schemes)
	s.Types = maps.Clone(s.Types)
	s.Aliases = maps.Clone(s.Aliases)
	s.Ctors = maps.Clone(s.Ctors)
	s.Effects = maps.Clone(s.Effects)
	s.Operations = maps.Clone(s.Operations)
	s.Classes = maps.Clone(s.Classes)
	s.Methods = maps.Clone(s.Methods)
	s.Workers = maps.Clone(s.Workers)
	s.Natives = maps.Clone(s.Natives)
	s.Intrinsics = maps.Clone(s.Intrinsics)
	s.Derivers = maps.Clone(s.Derivers)
	s.Captures = maps.Clone(s.Captures)
	owned := map[*InstanceInfo]bool{}
	type ownedRef struct {
		owner string
		ref   DeclRef
	}
	refs := map[ownedRef]bool{}
	for _, m := range modules {
		for k := range m.Schemes {
			delete(s.Schemes, k)
		}
		for k := range m.Types {
			delete(s.Types, k)
		}
		for k := range m.Aliases {
			delete(s.Aliases, k)
		}
		for k := range m.Ctors {
			delete(s.Ctors, k)
		}
		for k := range m.Effects {
			delete(s.Effects, k)
		}
		for k := range m.Operations {
			delete(s.Operations, k)
		}
		for k := range m.Classes {
			delete(s.Classes, k)
		}
		for k := range m.Methods {
			delete(s.Methods, k)
		}
		for k := range m.Workers {
			delete(s.Workers, k)
		}
		for k := range m.Natives {
			delete(s.Natives, k)
		}
		for k := range m.Intrinsics {
			delete(s.Intrinsics, k)
		}
		for k := range m.Derivers {
			delete(s.Derivers, k)
		}
		for k := range m.Captures {
			delete(s.Captures, k)
		}
		for _, in := range m.Instances {
			owned[in] = true
			refs[ownedRef{in.Owner, in.Ref}] = true
		}
	}
	s.Instances = nil
	for _, in := range ck.Instances {
		if !owned[in] && (in.Owner == "" || !refs[ownedRef{in.Owner, in.Ref}]) {
			s.Instances = append(s.Instances, in)
		}
	}
	return &s
}
