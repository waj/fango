package codegen

import (
	goast "go/ast"
	gotoken "go/token"

	"github.com/waj/fango/internal/types"
)

// A polymorphic handler clause erases its operation-local type variables to
// Go any, so a payload it builds can reach a typed reader at a different Go
// instantiation of the same checked type. Descriptors of parameterized
// inspectable types therefore carry a zero-size rebuilder for the
// instantiation in scope where they are constructed, and constructors expose
// their fields independently of instantiation (runtime/fangort/rebuild.go).

func rebuilderName(name string) string { return "R_" + linkName(name) }

// rebuildableADT reports whether a declaration's values may need a different
// Go instantiation after a descriptor match.
func (g *gen) rebuildableADT(adt *types.ADTInfo) bool {
	if adt == nil || adt.Repr != types.ReprADT || len(adt.Params) == 0 || adt.Resource || adt.NativeIndexed {
		return false
	}
	args := make([]types.Type, len(adt.Params))
	for i, param := range adt.Params {
		if param.Kind == types.RowVar {
			return false
		}
		args[i] = param
	}
	con := &types.TCon{Unique: adt.Con.Unique, Name: adt.Con.Name, Args: args}
	return types.InspectionShapeSafe(con, g.adts) && !g.controlledType(con, nil)
}

// descriptorRebuilder is the rebuilder attached to an inspectable descriptor,
// or nil when every value of the type has one Go representation.
func (g *gen) descriptorRebuilder(t *types.TCon) goast.Expr {
	adt := g.adts[t.Unique]
	if adt == nil || len(t.Args) == 0 {
		return nil
	}
	switch {
	case adt.Repr == types.ReprList:
		return &goast.CompositeLit{Type: indexExpr(selector("fangort", "ListRebuilder"), g.goTypes(t.Args))}
	case g.rebuildableADT(adt):
		return &goast.CompositeLit{Type: indexExpr(g.qualified(symbolOwner(adt.Con.Name), rebuilderName(adt.Con.Name)), g.goTypes(t.Args))}
	}
	return nil
}

// rebuildDecls emits the declaration's rebuilder and the constructor field
// views it consumes. g.tyParamNames names the runtime parameters.
func (g *gen) rebuildDecls(adt *types.ADTInfo, args []goast.Expr) []goast.Decl {
	g.usesFangort = true
	params := runtimeADTParams(adt)
	var decls []goast.Decl
	fieldsResult := &goast.FieldList{List: []*goast.Field{{Type: ident("int")}, {Type: &goast.ArrayType{Elt: ident("any")}}}}
	fieldsMethod := func(recv goast.Expr, body []goast.Stmt) goast.Decl {
		return &goast.FuncDecl{Name: ident("FangoFields"), Recv: &goast.FieldList{List: []*goast.Field{{Names: []*goast.Ident{ident("v")}, Type: recv}}},
			Type: &goast.FuncType{Params: &goast.FieldList{}, Results: fieldsResult}, Body: &goast.BlockStmt{List: body}}
	}
	fieldValues := func(ctor *types.CtorInfo) goast.Stmt {
		values := make([]goast.Expr, len(ctor.Fields))
		for i := range ctor.Fields {
			values[i] = selector("v", representationField(adt, ctor, i))
		}
		return &goast.ReturnStmt{Results: []goast.Expr{intLit(int64(ctor.Index)), &goast.CompositeLit{Type: &goast.ArrayType{Elt: ident("any")}, Elts: values}}}
	}
	if taggedADT(adt) {
		var cases []goast.Stmt
		for _, ctor := range adt.Ctors {
			cases = append(cases, &goast.CaseClause{List: []goast.Expr{intLit(int64(ctor.Index))}, Body: []goast.Stmt{fieldValues(ctor)}})
		}
		decls = append(decls, fieldsMethod(indexExpr(ident(mangleType(adt.Con.Name)), args), []goast.Stmt{
			&goast.SwitchStmt{Tag: selector("v", "Tag"), Body: &goast.BlockStmt{List: cases}},
			&goast.ReturnStmt{Results: []goast.Expr{callExpr(ident("int"), selector("v", "Tag")), ident("nil")}},
		}))
	} else {
		for _, ctor := range adt.Ctors {
			decls = append(decls, fieldsMethod(indexExpr(ident(mangleCtor(ctor.Name)), args), []goast.Stmt{fieldValues(ctor)}))
		}
	}

	runtimeArgs := make([]types.Type, len(params))
	for i, param := range params {
		runtimeArgs[i] = param
	}
	hasFields := false
	for _, ctor := range adt.Ctors {
		hasFields = hasFields || len(ctor.Fields) > 0
	}
	var body []goast.Stmt
	self := indexExpr(ident(mangleType(adt.Con.Name)), args)
	if taggedADT(adt) || productADT(adt) {
		body = append(body, &goast.IfStmt{
			Init: &goast.AssignStmt{Lhs: []goast.Expr{ident("value"), ident("ok")}, Tok: gotoken.DEFINE, Rhs: []goast.Expr{&goast.TypeAssertExpr{X: ident("source"), Type: self}}},
			Cond: ident("ok"), Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(ident("value"))}},
		})
	} else {
		// Every instantiation satisfies the marker interface; only the
		// constructor types identify this one.
		var ctors []goast.Expr
		for _, ctor := range adt.Ctors {
			ctors = append(ctors, &goast.StarExpr{X: indexExpr(ident(mangleCtor(ctor.Name)), args)})
		}
		body = append(body, &goast.TypeSwitchStmt{Assign: &goast.ExprStmt{X: &goast.TypeAssertExpr{X: ident("source")}},
			Body: &goast.BlockStmt{List: []goast.Stmt{&goast.CaseClause{List: ctors, Body: []goast.Stmt{returnStmt(ident("source"))}}}}})
	}
	for i, param := range params {
		name := descriptorParamName(g.tyParamNames[param.ID])
		body = append(body, varDeclStmt(name, g.descriptorType(), &goast.IndexExpr{X: ident("arguments"), Index: intLit(int64(i))}), assignBlank(ident(name)))
	}
	body = append(body, &goast.AssignStmt{Lhs: []goast.Expr{ident("ctor"), ident("fields")}, Tok: gotoken.DEFINE,
		Rhs: []goast.Expr{callExpr(&goast.SelectorExpr{X: &goast.TypeAssertExpr{X: ident("source"), Type: selector("fangort", "Constructed")}, Sel: ident("FangoFields")})}})
	if !hasFields {
		body = append(body, assignBlank(ident("fields")))
	}
	var cases []goast.Stmt
	for _, ctor := range adt.Ctors {
		values := make([]goast.Expr, len(ctor.Fields))
		for i, field := range ctor.Fields {
			values[i] = callExpr(indexExpr(selector("fangort", "Rebuild"), []goast.Expr{g.goType(field)}), g.typeDescriptor(field), &goast.IndexExpr{X: ident("fields"), Index: intLit(int64(i))})
		}
		cases = append(cases, &goast.CaseClause{List: []goast.Expr{intLit(int64(ctor.Index))}, Body: []goast.Stmt{returnStmt(callExpr(self, g.ctorValue(ctor, runtimeArgs, values...)))}})
	}
	body = append(body, &goast.SwitchStmt{Tag: ident("ctor"), Body: &goast.BlockStmt{List: cases}},
		returnStmt(callExpr(selector("fangort", "RebuildMismatch"))))

	name := rebuilderName(adt.Con.Name)
	decls = append(decls,
		&goast.GenDecl{Tok: gotoken.TYPE, Specs: []goast.Spec{&goast.TypeSpec{Name: ident(name), TypeParams: g.typeParamFields(params), Type: &goast.StructType{Fields: &goast.FieldList{}}}}},
		&goast.FuncDecl{Name: ident("Rebuild"), Recv: &goast.FieldList{List: []*goast.Field{{Type: indexExpr(ident(name), args)}}},
			Type: &goast.FuncType{
				Params: &goast.FieldList{List: []*goast.Field{
					{Names: []*goast.Ident{ident("source")}, Type: ident("any")},
					{Names: []*goast.Ident{ident("arguments")}, Type: &goast.ArrayType{Elt: g.descriptorType()}},
				}},
				Results: &goast.FieldList{List: []*goast.Field{{Type: ident("any")}}},
			},
			Body: &goast.BlockStmt{List: body}})
	return decls
}
