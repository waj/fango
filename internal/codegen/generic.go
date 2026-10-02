package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"

	"github.com/waj/fango/internal/types"
)

// Derived-operation synthesis at generic types (doc/design.md, "Go backend and runtime"): a generic ADT's
// derived eq takes one element operation per type parameter
// (`eqT_List[A0 any](eq0 func(A0, A0) bool, a, b T_List[A0]) bool`), and
// call sites synthesize those arguments from the ground instantiation —
// scalars via tiny shared helpers, nested ADTs via func-lits wrapping the
// inner derived function (composing to any depth), type parameters (inside
// another derived function) via the enclosing element-op parameter.

func eqParamName(i int) string { return fmt.Sprintf("eq%d", i) }

// eqCall builds `eqT_X[…](eqArgs…, a, b)` for an ADT-typed comparison,
// marking every ADT in the ground type as needing derived eq.
func (g *gen) eqCall(t types.Type, a, b goast.Expr) goast.Expr {
	g.needEqType(t)
	con := t.(*types.TCon)
	adt := g.adts[con.Unique]
	fn := indexExpr(g.eqRef(adt), g.goTypes(runtimeADTArgs(adt, con.Args)))
	args := make([]goast.Expr, 0, len(con.Args)+2)
	for _, ta := range runtimeADTArgs(adt, con.Args) {
		args = append(args, g.eqArg(ta))
	}
	return callExpr(fn, append(args, a, b)...)
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
			return g.eqRef(adt) // signature matches exactly
		}
		return funcLitParams(
			[]paramSpec{{name: "x", typ: g.goType(t)}, {name: "y", typ: g.goType(t)}},
			ident("bool"),
			[]goast.Stmt{returnStmt(g.eqCall(t, ident("x"), ident("y")))})
	}
	g.scalarEq[con.Unique] = true
	return ident(g.scalarName("eq", con.Unique))
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
	case g.b.Char.Unique:
		return prefix + "Char"
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

// scalarHelperDecls emits the demanded scalar element-op helpers, in a fixed
// order for determinism.
func (g *gen) scalarHelperDecls() []goast.Decl {
	var decls []goast.Decl
	order := []struct {
		unique int
		goTy   goast.Expr
	}{
		{g.b.Int.Unique, ident("int64")},
		{g.b.Float.Unique, ident("float64")},
		{g.b.String.Unique, ident("string")},
		{g.b.Char.Unique, ident("rune")},
		{g.b.Bool.Unique, ident("bool")},
		{g.b.Unit.Unique, nil},
	}
	for _, s := range order {
		if g.scalarEq[s.unique] {
			if s.unique == g.b.Unit.Unique {
				s.goTy = g.unitType()
			}
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
	return decls
}
