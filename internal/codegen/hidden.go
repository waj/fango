package codegen

import (
	"fmt"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	goast "go/ast"
	"maps"
)

func hiddenDescriptorField(i int) string { return fmt.Sprintf("HiddenType%d", i) }
func hiddenTypeArgs(vars []*types.TVar) []types.Type {
	out := make([]types.Type, len(vars))
	for i, v := range vars {
		out[i] = v
	}
	return out
}

// eraseHidden is the general representation fallback. A fresh type remains
// abstract in Core; Go stores it as any only at a surviving package boundary.
func (g *gen) eraseHidden(vars []*types.TVar) func() {
	if len(vars) == 0 {
		return func() {}
	}
	oldNames, oldDescriptors := g.tyParamNames, g.polyDescriptorNames
	g.tyParamNames = maps.Clone(oldNames)
	g.polyDescriptorNames = maps.Clone(oldDescriptors)
	if g.tyParamNames == nil {
		g.tyParamNames = map[int]string{}
	}
	if g.polyDescriptorNames == nil {
		g.polyDescriptorNames = map[int]string{}
	}
	for _, v := range vars {
		g.tyParamNames[v.ID] = "any"
	}
	return func() { g.tyParamNames, g.polyDescriptorNames = oldNames, oldDescriptors }
}

func (g *gen) openHidden(vars []*types.TVar, src string) []goast.Stmt {
	var out []goast.Stmt
	for i, v := range vars {
		name := fmt.Sprintf("t_hidden_type%d", g.tmp)
		g.tmp++
		g.polyDescriptorNames[v.ID] = name
		out = append(out, varDeclStmt(name, g.descriptorType(), selector(src, hiddenDescriptorField(i))), assignBlank(ident(name)))
	}
	return out
}

// Repacking can change abstract binder identities without changing any Go
// representation. Do not wrap callbacks or walk immutable data in that case.
func (g *gen) sameErasedRepresentation(from, to types.Type) bool {
	erased := map[int]types.Type{}
	marker := &types.TVar{ID: -1, Rigid: true, Kind: types.General}
	for id, name := range g.tyParamNames {
		if name == "any" {
			erased[id] = marker
		}
	}
	if len(erased) == 0 {
		return false
	}
	return core.EqualValueRepresentation(types.SubstRigid(from, erased), types.SubstRigid(to, erased))
}
