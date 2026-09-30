package codegen

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/format"
	gotoken "go/token"
	"sort"
)

// Aliases retain structural compatibility across modules while spelling each
// repeated callback family only once. They never introduce a new nominal type.
func (g *gen) callableAlias(shape *goast.StructType) goast.Expr {
	var key bytes.Buffer
	if err := format.Node(&key, gotoken.NewFileSet(), shape); err != nil {
		panic(err)
	}
	if g.callableNames == nil {
		g.callableNames = map[string]string{}
	}
	name := g.callableNames[key.String()]
	used := map[string]bool{}
	available := map[string]bool{}
	for _, n := range g.tyParamNames {
		if n != "any" {
			available[n] = true
		}
	}
	goast.Inspect(shape, func(node goast.Node) bool {
		if id, ok := node.(*goast.Ident); ok && available[id.Name] {
			used[id.Name] = true
		}
		return true
	})
	var params []string
	for n := range used {
		params = append(params, n)
	}
	sort.Strings(params)
	var args []goast.Expr
	var fields []*goast.Field
	for _, n := range params {
		args = append(args, ident(n))
		fields = append(fields, &goast.Field{Names: []*goast.Ident{ident(n)}, Type: ident("any")})
	}
	if name == "" {
		name = fmt.Sprintf("Fn%d", len(g.callableNames))
		g.callableNames[key.String()] = name
		spec := &goast.TypeSpec{Name: ident(name), Assign: 1, Type: shape}
		if len(fields) != 0 {
			spec.TypeParams = &goast.FieldList{List: fields}
		}
		g.callableDecls = append(g.callableDecls, &goast.GenDecl{Tok: gotoken.TYPE, Specs: []goast.Spec{spec}})
	}
	return indexExpr(ident(name), args)
}
