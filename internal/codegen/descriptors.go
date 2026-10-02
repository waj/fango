package codegen

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/format"
	"go/token"

	"github.com/waj/fango/internal/types"
)

func descriptorParamName(name string) string { return "type_" + name }
func (g *gen) descriptorType() goast.Expr {
	g.usesFangort = true
	return &goast.StarExpr{X: selector("fangort", "TypeDescriptor")}
}
func (g *gen) typeDescriptor(t types.Type) goast.Expr {
	expr := g.typeDescriptorExpr(t)
	if !closedDescriptor(t) {
		return expr
	}
	// Closed descriptors are immutable and independent of the invocation.
	// Share them within the generated module, including nested arguments.
	var key bytes.Buffer
	if err := format.Node(&key, token.NewFileSet(), expr); err != nil {
		panic(err)
	}
	if name, ok := g.descriptorNames[key.String()]; ok {
		return ident(name)
	}
	if g.descriptorNames == nil {
		g.descriptorNames = make(map[string]string)
	}
	name := fmt.Sprintf("typeDescriptor%d", len(g.descriptorNames))
	g.descriptorNames[key.String()] = name
	g.descriptorDecls = append(g.descriptorDecls, &goast.GenDecl{Tok: token.VAR, Specs: []goast.Spec{
		&goast.ValueSpec{Names: []*goast.Ident{ident(name)}, Values: []goast.Expr{expr}},
	}})
	return ident(name)
}

func closedDescriptor(t types.Type) bool {
	switch t := t.(type) {
	case *types.TFun:
		_, parts := types.FunctionDescriptorShape(t)
		for _, part := range parts {
			if !closedDescriptor(part) {
				return false
			}
		}
		return true
	case *types.TCon:
		for _, arg := range t.Args {
			if !closedDescriptor(arg) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (g *gen) typeDescriptorExpr(t types.Type) goast.Expr {
	g.usesFangort = true
	switch t := t.(type) {
	case *types.TVar:
		if name, ok := g.polyDescriptorNames[t.ID]; ok {
			return ident(name)
		}
		name, ok := g.tyParamNames[t.ID]
		if !ok {
			panic(fmt.Sprintf("codegen: missing descriptor for type parameter %d", t.ID))
		}
		return ident(descriptorParamName(name))
	case *types.TFun:
		name, parts := types.FunctionDescriptorShape(t)
		args := []goast.Expr{stringLit(name), ident("false")}
		args = append(args, g.typeDescriptorArgs(parts)...)
		return callExpr(selector("fangort", "NominalType"), args...)
	case *types.TCon:
		safe := types.InspectionShapeSafe(t, g.adts)
		if safe {
			if rebuild := g.descriptorRebuilder(t); rebuild != nil {
				args := append([]goast.Expr{stringLit(t.Name), rebuild}, g.typeDescriptorArgs(t.Args)...)
				return callExpr(selector("fangort", "RebuildableType"), args...)
			}
		}
		args := []goast.Expr{stringLit(t.Name), ident(fmt.Sprint(safe))}
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
