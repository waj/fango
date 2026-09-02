// Package codegen lowers Core to Go source. It builds go/ast values and
// prints them with go/format.Node — never string templating — so emitted
// code is gofmt-clean by construction.
package codegen

import (
	goast "go/ast"
	gotoken "go/token"
	"strconv"
)

// Small builder helpers over go/ast. These stay dumb: all decisions live in
// gen.go; these only spell Go syntax.

func ident(name string) *goast.Ident { return goast.NewIdent(name) }

func intLit(v int64) goast.Expr {
	if v < 0 {
		return &goast.UnaryExpr{
			Op: gotoken.SUB,
			X:  &goast.BasicLit{Kind: gotoken.INT, Value: strconv.FormatInt(-v, 10)},
		}
	}
	return &goast.BasicLit{Kind: gotoken.INT, Value: strconv.FormatInt(v, 10)}
}

func binExpr(op gotoken.Token, l, r goast.Expr) goast.Expr {
	return &goast.BinaryExpr{X: l, Op: op, Y: r}
}

func parenIf(cond bool, e goast.Expr) goast.Expr {
	if cond {
		return &goast.ParenExpr{X: e}
	}
	return e
}

func callExpr(fn goast.Expr, args ...goast.Expr) goast.Expr {
	return &goast.CallExpr{Fun: fn, Args: args}
}

func selector(pkg, name string) goast.Expr {
	return &goast.SelectorExpr{X: ident(pkg), Sel: ident(name)}
}

func varDecl(name string, typ goast.Expr, value goast.Expr) goast.Decl {
	return &goast.GenDecl{
		Tok: gotoken.VAR,
		Specs: []goast.Spec{&goast.ValueSpec{
			Names:  []*goast.Ident{ident(name)},
			Type:   typ,
			Values: []goast.Expr{value},
		}},
	}
}

func funcDecl(name string, body ...goast.Stmt) goast.Decl {
	return &goast.FuncDecl{
		Name: ident(name),
		Type: &goast.FuncType{Params: &goast.FieldList{}},
		Body: &goast.BlockStmt{List: body},
	}
}

func importDecl(path string) goast.Decl {
	return &goast.GenDecl{
		Tok: gotoken.IMPORT,
		Specs: []goast.Spec{&goast.ImportSpec{
			Path: &goast.BasicLit{Kind: gotoken.STRING, Value: strconv.Quote(path)},
		}},
	}
}

func assignBlank(rhs goast.Expr) goast.Stmt {
	return &goast.AssignStmt{
		Lhs: []goast.Expr{ident("_")},
		Tok: gotoken.ASSIGN,
		Rhs: []goast.Expr{rhs},
	}
}

func exprStmt(e goast.Expr) goast.Stmt { return &goast.ExprStmt{X: e} }
