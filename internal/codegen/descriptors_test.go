package codegen

import (
	"go/ast"
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
