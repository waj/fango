package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"

	"github.com/waj/fango/internal/types"
)

// Derived-operation synthesis at generic types (doc/design.md, "Go backend and runtime"): a generic ADT's
// derived eq/show take one element operation per type parameter
// (`eqT_List[A0 any](eq0 func(A0, A0) bool, a, b T_List[A0]) bool`), and
// call sites synthesize those arguments from the ground instantiation —
// scalars via tiny shared helpers, nested ADTs via func-lits wrapping the
// inner derived function (composing to any depth), type parameters (inside
// another derived function) via the enclosing element-op parameter.

func eqParamName(i int) string   { return fmt.Sprintf("eq%d", i) }
func showParamName(i int) string { return fmt.Sprintf("show%d", i) }

// eqCall builds `eqT_X[…](eqArgs…, a, b)` for an ADT-typed comparison,
// marking every ADT in the ground type as needing derived eq.
func (g *gen) eqCall(t types.Type, a, b goast.Expr) goast.Expr {
	g.needEqType(t)
	con := t.(*types.TCon)
	adt := g.adts[con.Unique]
	fn := indexExpr(ident(eqFunc(adt.Con.Name)), g.goTypes(con.Args))
	args := make([]goast.Expr, 0, len(con.Args)+2)
	for _, ta := range con.Args {
		args = append(args, g.eqArg(ta))
	}
	return callExpr(fn, append(args, a, b)...)
}

// showCall builds `showT_X[…](showArgs…, v, nested)`.
func (g *gen) showCall(t types.Type, v, nested goast.Expr) goast.Expr {
	g.needShowType(t)
	con := t.(*types.TCon)
	adt := g.adts[con.Unique]
	fn := indexExpr(ident(showFunc(adt.Con.Name)), g.goTypes(con.Args))
	args := make([]goast.Expr, 0, len(con.Args)+2)
	for _, ta := range con.Args {
		args = append(args, g.showArg(ta))
	}
	return callExpr(fn, append(args, v, nested)...)
}

// eqArg synthesizes the `func(T, T) bool` element operation for type t.
func (g *gen) eqArg(t types.Type) goast.Expr {
	if v, ok := t.(*types.TVar); ok && v.Rigid {
		if name, ok := g.eqParamNames[v.ID]; ok {
			return ident(name) // inside a derived eq: the element-op param
		}
		// A Number-kinded type param at a call site (`xs == ys : List a`
		// with a Number-generic element): native == on the type-set param.
		return funcLitParams(
			[]paramSpec{{name: "x", typ: g.goType(t)}, {name: "y", typ: g.goType(t)}},
			ident("bool"),
			[]goast.Stmt{returnStmt(binExpr(gotoken.EQL, ident("x"), ident("y")))})
	}
	con := t.(*types.TCon)
	if adt, ok := g.adts[con.Unique]; ok {
		if len(con.Args) == 0 {
			return ident(eqFunc(adt.Con.Name)) // signature matches exactly
		}
		return funcLitParams(
			[]paramSpec{{name: "x", typ: g.goType(t)}, {name: "y", typ: g.goType(t)}},
			ident("bool"),
			[]goast.Stmt{returnStmt(g.eqCall(t, ident("x"), ident("y")))})
	}
	g.scalarEq[con.Unique] = true
	return ident(g.scalarName("eq", con.Unique))
}

// showArg synthesizes the `func(T, bool) string` element operation for t.
func (g *gen) showArg(t types.Type) goast.Expr {
	if v, ok := t.(*types.TVar); ok && v.Rigid {
		if name, ok := g.showParamNames[v.ID]; ok {
			return ident(name)
		}
		panic("codegen: show at a type variable outside a derived show")
	}
	con := t.(*types.TCon)
	if adt, ok := g.adts[con.Unique]; ok {
		if len(con.Args) == 0 {
			return ident(showFunc(adt.Con.Name))
		}
		return funcLitParams(
			[]paramSpec{{name: "x", typ: g.goType(t)}, {name: "nested", typ: ident("bool")}},
			ident("string"),
			[]goast.Stmt{returnStmt(g.showCall(t, ident("x"), ident("nested")))})
	}
	g.scalarShow[con.Unique] = true
	return ident(g.scalarName("show", con.Unique))
}

// scalarName is the shared element-op helper name for a scalar unique.
func (g *gen) scalarName(prefix string, unique int) string {
	switch unique {
	case g.b.Int.Unique:
		return prefix + "Int"
	case g.b.Float.Unique:
		return prefix + "Float"
	case g.b.String.Unique:
		return prefix + "String"
	case g.b.Bool.Unique:
		return prefix + "Bool"
	case g.b.Unit.Unique:
		return prefix + "Unit"
	default:
		panic("codegen: no scalar element operation for this type")
	}
}

// needEqType marks every ADT reachable from t — through its type arguments
// and, transitively, its declared fields — as needing derived eq.
func (g *gen) needEqType(t types.Type) {
	switch t := t.(type) {
	case *types.TCon:
		if adt, ok := g.adts[t.Unique]; ok && !g.neededEq[t.Unique] {
			g.neededEq[t.Unique] = true
			for _, c := range adt.Ctors {
				for _, f := range c.Fields {
					g.needEqType(f)
				}
			}
		}
		for _, a := range t.Args {
			g.needEqType(a)
		}
	case *types.TFun:
		// Unreachable at equated types (ContainsFunction rejected), but a
		// declared field may be a function under a never-equated ctor.
	}
}

func (g *gen) needShowType(t types.Type) {
	switch t := t.(type) {
	case *types.TCon:
		if adt, ok := g.adts[t.Unique]; ok && !g.neededShow[t.Unique] {
			g.neededShow[t.Unique] = true
			for _, c := range adt.Ctors {
				for _, f := range c.Fields {
					g.needShowType(f)
				}
			}
		}
		for _, a := range t.Args {
			g.needShowType(a)
		}
	}
}

// scalarHelperDecls emits the demanded scalar element-op helpers, in a fixed
// order for determinism.
func (g *gen) scalarHelperDecls() []goast.Decl {
	var decls []goast.Decl
	order := []struct {
		unique int
		goTy   goast.Expr
		showFn string
	}{
		{g.b.Int.Unique, ident("int64"), "ShowInt"},
		{g.b.Float.Unique, ident("float64"), "ShowFloat"},
		{g.b.String.Unique, ident("string"), "ShowStringLiteral"},
		{g.b.Bool.Unique, ident("bool"), "ShowBool"},
		{g.b.Unit.Unique, &goast.StructType{Fields: &goast.FieldList{}}, "ShowUnit"},
	}
	for _, s := range order {
		if g.scalarEq[s.unique] {
			decls = append(decls, &goast.FuncDecl{
				Name: ident(g.scalarName("eq", s.unique)),
				Type: &goast.FuncType{
					Params: &goast.FieldList{List: []*goast.Field{{
						Names: []*goast.Ident{ident("a"), ident("b")}, Type: s.goTy,
					}}},
					Results: &goast.FieldList{List: []*goast.Field{{Type: ident("bool")}}},
				},
				Body: &goast.BlockStmt{List: []goast.Stmt{
					returnStmt(binExpr(gotoken.EQL, ident("a"), ident("b"))),
				}},
			})
		}
	}
	for _, s := range order {
		if g.scalarShow[s.unique] {
			g.usesFangort = true
			var call goast.Expr
			if s.unique == g.b.Unit.Unique {
				call = callExpr(selector("fangort", s.showFn))
			} else {
				call = callExpr(selector("fangort", s.showFn), ident("v"))
			}
			decls = append(decls, &goast.FuncDecl{
				Name: ident(g.scalarName("show", s.unique)),
				Type: &goast.FuncType{
					Params: &goast.FieldList{List: []*goast.Field{
						{Names: []*goast.Ident{ident("v")}, Type: s.goTy},
						{Names: []*goast.Ident{ident("nested")}, Type: ident("bool")},
					}},
					Results: &goast.FieldList{List: []*goast.Field{{Type: ident("string")}}},
				},
				Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(call)}},
			})
		}
	}
	return decls
}
