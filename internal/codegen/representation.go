package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func productADT(adt *types.ADTInfo) bool {
	return adt != nil && adt.Repr == types.ReprADT && len(adt.Ctors) == 1
}

// Bundled wrapper sums and payload-free enums have compact fixed layouts.
// They remain ordinary nominal ADTs to every Core pass.
func taggedADT(adt *types.ADTInfo) bool {
	if adt == nil || adt.Repr != types.ReprADT {
		return false
	}
	switch adt.Con.Name {
	case "Maybe.Maybe":
		return len(adt.Params) == 1 && len(adt.Ctors) == 2 && len(adt.Ctors[0].Fields) == 0 && len(adt.Ctors[1].Fields) == 1
	case "Result.Result":
		return len(adt.Params) == 2 && len(adt.Ctors) == 2 && len(adt.Ctors[0].Fields) == 1 && len(adt.Ctors[1].Fields) == 1
	}
	if len(adt.Ctors) < 2 || len(adt.Ctors) > 256 {
		return false
	}
	for _, ctor := range adt.Ctors {
		if len(ctor.Fields) != 0 {
			return false
		}
	}
	return true
}

func representationField(adt *types.ADTInfo, ctor *types.CtorInfo, i int) string {
	if taggedADT(adt) {
		return fmt.Sprintf("F%d_%d", ctor.Index, i)
	}
	return fieldName(i)
}

// Value products must have a finite Go layout. Expand generic fields (rather
// than just nominal arguments) so Box (Pair Int Self) detects Self, whereas
// Box (List Self) stops at the immutable List's indirection. Cyclic products
// keep a pointer alias; function and ordinary union fields break layout cycles.
func (g *gen) valueProduct(adt *types.ADTInfo) bool {
	if !productADT(adt) {
		return false
	}
	// Reader-like capability records are constructed once and passed often.
	// Sharing larger function bundles avoids copying every callable member
	// through each enclosing cursor and Outcome.
	functions := 0
	for _, field := range adt.Ctors[0].Fields {
		if _, ok := field.(*types.TFun); ok {
			functions++
		}
	}
	if functions >= 3 {
		return false
	}
	visiting := map[int]bool{}
	var reaches func(types.Type) bool
	reaches = func(t types.Type) bool {
		con, ok := t.(*types.TCon)
		if !ok {
			return false
		}
		child := g.adts[con.Unique]
		if !productADT(child) && !taggedADT(child) {
			return false
		}
		if con.Unique == adt.Con.Unique {
			return true
		}
		if visiting[con.Unique] {
			return false
		}
		visiting[con.Unique] = true
		defer delete(visiting, con.Unique)
		for _, ctor := range child.Ctors {
			for _, f := range child.InstFields(ctor, con.Args) {
				if reaches(f) {
					return true
				}
			}
		}
		return false
	}
	for _, f := range adt.Ctors[0].Fields {
		if reaches(f) {
			return false
		}
	}
	return true
}

func (g *gen) productSwitch(t *core.SwitchCtor, leaf func(core.Expr) []goast.Stmt) []goast.Stmt {
	if len(t.Cases) == 0 {
		return g.treeStmts(t.Default, leaf)
	}
	c := t.Cases[0]
	con := g.caseVarTys[t.Scrut].(*types.TCon)
	fields := t.ADT.InstFields(c.Ctor, con.Args)
	var body []goast.Stmt
	if !anyBind(c.Binds) {
		body = append(body, assignBlank(ident(mangleValue(t.Scrut))))
	}
	for i, b := range c.Binds {
		if b == "" {
			continue
		}
		g.caseVarTys[b] = fields[i]
		body = append(body, varDeclStmt(mangleValue(b), g.goType(fields[i]), selector(mangleValue(t.Scrut), fieldName(i))))
		if g.isUnit(fields[i]) {
			body = append(body, assignBlank(ident(mangleValue(b))))
		}
	}
	return append(body, g.treeStmts(c.Tree, leaf)...)
}

func (g *gen) taggedDecl(adt *types.ADTInfo, args []goast.Expr) []goast.Decl {
	fields := []*goast.Field{{Names: []*goast.Ident{ident("Tag")}, Type: ident("uint8")}}
	for _, ctor := range adt.Ctors {
		for i, f := range ctor.Fields {
			fields = append(fields, &goast.Field{Names: []*goast.Ident{ident(representationField(adt, ctor, i))}, Type: g.goType(f)})
		}
	}
	params := runtimeADTParams(adt)
	name := mangleType(adt.Con.Name)
	decls := []goast.Decl{&goast.GenDecl{Tok: gotoken.TYPE, Specs: []goast.Spec{&goast.TypeSpec{Name: ident(name), TypeParams: g.typeParamFields(params), Type: &goast.StructType{Fields: &goast.FieldList{List: fields}}}}}}
	for _, ctor := range adt.Ctors {
		decls = append(decls, &goast.GenDecl{Tok: gotoken.TYPE, Specs: []goast.Spec{&goast.TypeSpec{Name: ident(mangleCtor(ctor.Name)), TypeParams: g.typeParamFields(params), Assign: 1, Type: indexExpr(ident(name), args)}}})
	}
	return decls
}

func (g *gen) taggedSwitch(t *core.SwitchCtor, leaf func(core.Expr) []goast.Stmt) []goast.Stmt {
	scrut := mangleValue(t.Scrut)
	con := g.caseVarTys[t.Scrut].(*types.TCon)
	var cases []goast.Stmt
	for _, c := range t.Cases {
		var body []goast.Stmt
		fields := t.ADT.InstFields(c.Ctor, con.Args)
		for i, b := range c.Binds {
			if b == "" {
				continue
			}
			g.caseVarTys[b] = fields[i]
			body = append(body, varDeclStmt(mangleValue(b), g.goType(fields[i]), selector(scrut, representationField(t.ADT, c.Ctor, i))))
			if g.isUnit(fields[i]) {
				body = append(body, assignBlank(ident(mangleValue(b))))
			}
		}
		cases = append(cases, &goast.CaseClause{List: []goast.Expr{intLit(int64(c.Ctor.Index))}, Body: append(body, g.treeStmts(c.Tree, leaf)...)})
	}
	if t.Default != nil {
		cases = append(cases, &goast.CaseClause{Body: g.treeStmts(t.Default, leaf)})
	} else {
		// Core proves coverage; retain a terminating default in Go.
		cases[len(cases)-1].(*goast.CaseClause).List = nil
	}
	return []goast.Stmt{&goast.SwitchStmt{Tag: selector(scrut, "Tag"), Body: &goast.BlockStmt{List: cases}}}
}

func (g *gen) convertTagged(value goast.Expr, from, to *types.TCon, adt *types.ADTInfo) goast.Expr {
	name := fmt.Sprintf("t_sum%d", g.tmp)
	g.tmp++
	var cases []goast.Stmt
	for _, ctor := range adt.Ctors {
		a, b := adt.InstFields(ctor, from.Args), adt.InstFields(ctor, to.Args)
		fields := make([]goast.Expr, len(a))
		for i := range fields {
			fields[i] = g.polyConvert(selector(name, representationField(adt, ctor, i)), a[i], b[i])
		}
		cases = append(cases, &goast.CaseClause{List: []goast.Expr{intLit(int64(ctor.Index))}, Body: []goast.Stmt{returnStmt(g.ctorValue(ctor, runtimeADTArgs(adt, to.Args), fields...))}})
	}
	body := []goast.Stmt{&goast.SwitchStmt{Tag: selector(name, "Tag"), Body: &goast.BlockStmt{List: cases}}}
	body = append(body, g.zeroReturn(to)...)
	return callExpr(funcLitParams([]paramSpec{{name: name, typ: g.goType(from)}}, g.goType(to), body), value)
}

func (g *gen) convertProduct(value goast.Expr, from, to *types.TCon, adt *types.ADTInfo) goast.Expr {
	name := fmt.Sprintf("t_product%d", g.tmp)
	g.tmp++
	ctor := adt.Ctors[0]
	a, b := adt.InstFields(ctor, from.Args), adt.InstFields(ctor, to.Args)
	fields := make([]goast.Expr, len(a))
	for i := range fields {
		fields[i] = g.polyConvert(selector(name, fieldName(i)), a[i], b[i])
	}
	result := g.ctorValue(ctor, runtimeADTArgs(adt, to.Args), fields...)
	return callExpr(funcLitParams([]paramSpec{{name: name, typ: g.goType(from)}}, g.goType(to), []goast.Stmt{returnStmt(result)}), value)
}
