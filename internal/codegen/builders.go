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
		// -v overflows for MinInt64; negate via uint64 instead. The
		// resulting `-9223372036854775808` is a legal Go untyped constant.
		return &goast.UnaryExpr{
			Op: gotoken.SUB,
			X:  &goast.BasicLit{Kind: gotoken.INT, Value: strconv.FormatUint(uint64(-(v+1))+1, 10)},
		}
	}
	return &goast.BasicLit{Kind: gotoken.INT, Value: strconv.FormatInt(v, 10)}
}

func stringLit(s string) goast.Expr {
	return &goast.BasicLit{Kind: gotoken.STRING, Value: strconv.Quote(s)}
}

func funcLit(result goast.Expr, body []goast.Stmt) goast.Expr {
	return &goast.FuncLit{
		Type: &goast.FuncType{
			Params:  &goast.FieldList{},
			Results: &goast.FieldList{List: []*goast.Field{{Type: result}}},
		},
		Body: &goast.BlockStmt{List: body},
	}
}

type paramSpec struct {
	name string
	typ  goast.Expr
}

func paramFields(params []paramSpec) *goast.FieldList {
	fields := make([]*goast.Field, len(params))
	for i, p := range params {
		var names []*goast.Ident
		if p.name != "" {
			names = []*goast.Ident{ident(p.name)}
		}
		fields[i] = &goast.Field{Names: names, Type: p.typ}
	}
	return &goast.FieldList{List: fields}
}

// funcType is the curried arrow mapping T⟦a->b⟧ = func(A) B (doc/design.md, "Go backend and runtime").
func funcType(param, result goast.Expr) goast.Expr {
	return &goast.FuncType{
		Params:  &goast.FieldList{List: []*goast.Field{{Type: param}}},
		Results: &goast.FieldList{List: []*goast.Field{{Type: result}}},
	}
}

func funcLitParams(params []paramSpec, result goast.Expr, body []goast.Stmt) goast.Expr {
	return &goast.FuncLit{
		Type: &goast.FuncType{
			Params:  paramFields(params),
			Results: &goast.FieldList{List: []*goast.Field{{Type: result}}},
		},
		Body: &goast.BlockStmt{List: body},
	}
}

// workerDecl is a top-level uncurried worker: func v_f(v_x T, …) R { … }.
func workerDecl(name string, params []paramSpec, result goast.Expr, body []goast.Stmt) goast.Decl {
	return &goast.FuncDecl{
		Name: ident(name),
		Type: &goast.FuncType{
			Params:  paramFields(params),
			Results: &goast.FieldList{List: []*goast.Field{{Type: result}}},
		},
		Body: &goast.BlockStmt{List: body},
	}
}

// varDeclNoValue is `var name T` — the declare half of the letrec idiom.
func varDeclNoValue(name string, typ goast.Expr) goast.Stmt {
	return &goast.DeclStmt{Decl: &goast.GenDecl{
		Tok: gotoken.VAR,
		Specs: []goast.Spec{&goast.ValueSpec{
			Names: []*goast.Ident{ident(name)},
			Type:  typ,
		}},
	}}
}

func assignStmt(name string, rhs goast.Expr) goast.Stmt {
	return &goast.AssignStmt{
		Lhs: []goast.Expr{ident(name)},
		Tok: gotoken.ASSIGN,
		Rhs: []goast.Expr{rhs},
	}
}

func ifStmt(cond goast.Expr, then, els []goast.Stmt) goast.Stmt {
	return &goast.IfStmt{
		Cond: cond,
		Body: &goast.BlockStmt{List: then},
		Else: &goast.BlockStmt{List: els},
	}
}

func returnStmt(e goast.Expr) goast.Stmt {
	return &goast.ReturnStmt{Results: []goast.Expr{e}}
}

// varDeclStmt is `var name T = value` as a statement — always `var`, never
// `:=`: short declarations infer Go types from untyped constants (`x := 2`
// is an int, not int64) and would silently mistype fango locals.
func varDeclStmt(name string, typ, value goast.Expr) goast.Stmt {
	return &goast.DeclStmt{Decl: varDecl(name, typ, value)}
}

func unitLit() goast.Expr {
	return &goast.CompositeLit{Type: &goast.StructType{Fields: &goast.FieldList{}}}
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

// indexExpr is an explicit generic instantiation: `x[A]` / `x[A, B]`.
func indexExpr(x goast.Expr, args []goast.Expr) goast.Expr {
	if len(args) == 0 {
		return x
	}
	if len(args) == 1 {
		return &goast.IndexExpr{X: x, Index: args[0]}
	}
	return &goast.IndexListExpr{X: x, Indices: args}
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

func importDecl(paths ...string) goast.Decl {
	specs := make([]goast.Spec, len(paths))
	for i, p := range paths {
		specs[i] = &goast.ImportSpec{
			Path: &goast.BasicLit{Kind: gotoken.STRING, Value: strconv.Quote(p)},
		}
	}
	return &goast.GenDecl{Tok: gotoken.IMPORT, Specs: specs}
}

func assignBlank(rhs goast.Expr) goast.Stmt {
	return &goast.AssignStmt{
		Lhs: []goast.Expr{ident("_")},
		Tok: gotoken.ASSIGN,
		Rhs: []goast.Expr{rhs},
	}
}

func exprStmt(e goast.Expr) goast.Stmt { return &goast.ExprStmt{X: e} }
