package codegen

import (
	"go/ast"
	"reflect"
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestDescriptorsShareClosedShapesOnly(t *testing.T) {
	b := types.NewBuiltins(&types.Supply{})
	g := &gen{tyParamNames: map[int]string{123: "A0"}}
	first := g.typeDescriptor(b.Int).(*ast.Ident)
	again := g.typeDescriptor(b.Int).(*ast.Ident)
	if first.Name != again.Name || len(g.descriptorDecls) != 1 {
		t.Fatal("closed type descriptor was rebuilt")
	}
	arg := &types.TVar{ID: 123}
	open := &types.TCon{Name: "Box", Args: []types.Type{arg}}
	if _, ok := g.typeDescriptor(open).(*ast.CallExpr); !ok {
		t.Fatal("open descriptor escaped its type parameter scope")
	}
	if len(g.descriptorDecls) != 1 {
		t.Fatal("open descriptor was hoisted")
	}
	closed := &types.TCon{Name: "Box", Args: []types.Type{b.Int}}
	one := g.typeDescriptor(closed).(*ast.Ident)
	two := g.typeDescriptor(closed).(*ast.Ident)
	if one.Name != two.Name || len(g.descriptorDecls) != 2 {
		t.Fatal("nested closed descriptor was rebuilt")
	}
}

func TestStorageDescriptorPreservesPublicIdentity(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	a, h := sup.FreshRigid(types.General), sup.FreshRigid(types.General)
	original := &types.ADTInfo{Con: &types.TCon{Unique: sup.NextUnique(), Name: "Module.Box"}, Params: []*types.TVar{a}}
	original.Ctors = []*types.CtorInfo{{Name: "Module.Box", Result: original.Con, Fields: []types.Type{h}, Hidden: []*types.TVar{h}}}
	mirror := &types.ADTInfo{Con: &types.TCon{Unique: sup.NextUnique(), Name: "Module._storage_Box"}, Params: []*types.TVar{a, h}, RepresentationOf: original}
	adts := map[int]*types.ADTInfo{original.Con.Unique: original, mirror.Con.Unique: mirror}
	public := &types.TCon{Unique: original.Con.Unique, Name: original.Con.Name, Args: []types.Type{b.Int}}
	// Deliberately leave the storage witness unbound. Its descriptor must
	// neither be consulted nor appear in the source nominal identity.
	storage := &types.TCon{Unique: mirror.Con.Unique, Name: mirror.Con.Name, Args: []types.Type{b.Int, h}}
	g := &gen{adts: adts}
	want := g.typeDescriptorExpr(public)
	got := g.typeDescriptorExpr(storage)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storage descriptor changed public identity: %#v versus %#v", got, want)
	}
}
