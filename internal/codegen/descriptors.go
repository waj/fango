package codegen

import (
	"fmt"
	"github.com/waj/fango/internal/types"
	goast "go/ast"
)

func descriptorParamName(name string) string { return "type_" + name }
func (g *gen) descriptorType() goast.Expr {
	g.usesFangort = true
	return &goast.StarExpr{X: selector("fangort", "TypeDescriptor")}
}
func (g *gen) typeDescriptor(t types.Type) goast.Expr {
	g.usesFangort = true
	switch t := t.(type) {
	case *types.TVar:
		name, ok := g.tyParamNames[t.ID]
		if !ok {
			panic(fmt.Sprintf("codegen: missing descriptor for type parameter %d", t.ID))
		}
		return ident(descriptorParamName(name))
	case *types.TFun:
		return callExpr(selector("fangort", "NominalType"), stringLit("<function>"), ident("false"))
	case *types.TCon:
		args := []goast.Expr{stringLit(t.Name), ident(fmt.Sprint(types.InspectionShapeSafe(t, g.adts)))}
		args = append(args, g.typeDescriptorArgs(t.Args)...)
		return callExpr(selector("fangort", "NominalType"), args...)
	default:
		panic(fmt.Sprintf("codegen: invalid descriptor type %T", t))
	}
}
func (g *gen) typeDescriptorArgs(types_ []types.Type) []goast.Expr {
	args := make([]goast.Expr, len(types_))
	for i, t := range types_ {
		args[i] = g.typeDescriptor(t)
	}
	return args
}
func (g *gen) descriptorParams(params []*types.TVar) []paramSpec {
	var result []paramSpec
	for _, param := range params {
		result = append(result, paramSpec{name: descriptorParamName(g.tyParamNames[param.ID]), typ: g.descriptorType()})
	}
	return result
}
func (g *gen) descriptorParamArgs(params []*types.TVar) []goast.Expr {
	var result []goast.Expr
	for _, param := range params {
		result = append(result, g.typeDescriptor(param))
	}
	return result
}
