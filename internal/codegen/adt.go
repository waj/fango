package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// ADT lowering (DESIGN.md §8.1): one marker interface per declared type, one
// struct per constructor with typed fields, construction by pointer,
// discrimination by type switch (no tag field), derived eq/show emitted on
// demand only.

func mangleType(name string) string   { return "T_" + name }
func mangleCtor(name string) string   { return "C_" + name }
func markerMethod(name string) string { return "isT_" + name }
func eqFunc(name string) string       { return "eqT_" + name }
func showFunc(name string) string     { return "showT_" + name }

func fieldName(i int) string { return fmt.Sprintf("F%d", i) }

// adtDecls emits the marker interface, constructor structs, and marker
// methods for every declared type, in declaration order. Parameterized types
// emit as Go generics (`type T_List[A0 any] interface{ isT_List() }`,
// `func (C_Cons[A0]) isT_List() {}`), §8.4.
func (g *gen) adtDecls(adts []*types.ADTInfo) []goast.Decl {
	var decls []goast.Decl
	for _, adt := range adts {
		g.tyParamNames = tyParamNames(adt.Params)
		paramIdents := make([]goast.Expr, len(adt.Params))
		for i, v := range adt.Params {
			paramIdents[i] = ident(g.tyParamNames[v.ID])
		}
		iface := mangleType(adt.Con.Name)
		marker := markerMethod(adt.Con.Name)
		decls = append(decls, &goast.GenDecl{
			Tok: gotoken.TYPE,
			Specs: []goast.Spec{&goast.TypeSpec{
				Name:       ident(iface),
				TypeParams: g.typeParamFields(adt.Params),
				Type: &goast.InterfaceType{Methods: &goast.FieldList{List: []*goast.Field{{
					Names: []*goast.Ident{ident(marker)},
					Type:  &goast.FuncType{Params: &goast.FieldList{}},
				}}}},
			}},
		})
		for _, c := range adt.Ctors {
			fields := make([]*goast.Field, len(c.Fields))
			for i, ft := range c.Fields {
				fields[i] = &goast.Field{Names: []*goast.Ident{ident(fieldName(i))}, Type: g.goType(ft)}
			}
			decls = append(decls,
				&goast.GenDecl{
					Tok: gotoken.TYPE,
					Specs: []goast.Spec{&goast.TypeSpec{
						Name:       ident(mangleCtor(c.Name)),
						TypeParams: g.typeParamFields(adt.Params),
						Type:       &goast.StructType{Fields: &goast.FieldList{List: fields}},
					}},
				},
				&goast.FuncDecl{
					Recv: &goast.FieldList{List: []*goast.Field{{
						Type: indexExpr(ident(mangleCtor(c.Name)), paramIdents),
					}}},
					Name: ident(marker),
					Type: &goast.FuncType{Params: &goast.FieldList{}},
					Body: &goast.BlockStmt{},
				})
		}
	}
	g.tyParamNames = nil
	return decls
}

// ctorLit is a saturated constructor application: `&C_Name{args…}` —
// construction by pointer (§8.1: value receivers make the pointer implement
// the marker; zero-field constructors hit Go's zerobase). Parameterized
// constructors instantiate at the result type's arguments.
func (g *gen) ctorLit(e *core.App) goast.Expr {
	args := make([]goast.Expr, len(e.Args))
	for i, a := range e.Args {
		args[i] = g.expr(a, 0)
	}
	litType := indexExpr(ident(mangleCtor(e.Ctor.Name)), g.goTypes(e.TyArgs))
	return &goast.UnaryExpr{
		Op: gotoken.AND,
		X:  &goast.CompositeLit{Type: litType, Elts: args},
	}
}

// ---------------------------------------------------------------------------
// Case emission. caseStmts is the one driver; the leaf callback decides what
// a matched branch does with its body (return it, assign it, run it).

func (g *gen) caseStmts(e *core.Case, leaf func(core.Expr) []goast.Stmt) []goast.Stmt {
	bind := mangleValue(e.Bind)
	// Record the binder's (instantiated) type: nested constructor switches
	// read their column's type arguments from here.
	g.caseVarTys[e.Bind] = e.Scrut.Type()
	stmts := []goast.Stmt{varDeclStmt(bind, g.goType(e.Scrut.Type()), g.expr(e.Scrut, 0))}
	return append(stmts, g.treeStmts(e.Tree, leaf)...)
}

func (g *gen) treeStmts(t core.Tree, leaf func(core.Expr) []goast.Stmt) []goast.Stmt {
	switch t := t.(type) {
	case *core.Leaf:
		return leaf(t.Body)
	case *core.SwitchCtor:
		if t.ADT.Con.Unique == g.b.Bool.Unique {
			return g.boolSwitch(t, leaf)
		}
		return g.ctorSwitch(t, leaf)
	case *core.SwitchLit:
		return g.litSwitch(t, leaf)
	default:
		panic(fmt.Sprintf("codegen: unhandled tree node %T", t))
	}
}

// boolSwitch: `case` on Bool compiles to if/else (§8.1) — Bool stays native.
func (g *gen) boolSwitch(t *core.SwitchCtor, leaf func(core.Expr) []goast.Stmt) []goast.Stmt {
	pick := func(name string) core.Tree {
		for _, c := range t.Cases {
			if c.Ctor.Name == name {
				return c.Tree
			}
		}
		return t.Default
	}
	return []goast.Stmt{ifStmt(ident(mangleValue(t.Scrut)),
		g.treeStmts(pick("True"), leaf),
		g.treeStmts(pick("False"), leaf))}
}

// ctorSwitch emits a Go type switch. Full coverage turns the LAST case into
// `default:` with a checked assertion instead of a type-switch binding —
// exhaustiveness proved there is no other constructor, so no panic path is
// reachable (§8.5).
func (g *gen) ctorSwitch(t *core.SwitchCtor, leaf func(core.Expr) []goast.Stmt) []goast.Stmt {
	scrut := ident(mangleValue(t.Scrut))
	full := t.Default == nil

	// The scrutinee's instantiation drives the case tags (`*C_Cons[int64]`)
	// and the field types — declaration-level Fields are over the ADT's own
	// params and must be instantiated per occurrence.
	scrutTy, ok := g.caseVarTys[t.Scrut].(*types.TCon)
	if !ok {
		panic("codegen: SwitchCtor scrutinee type unknown")
	}
	tagArgs := g.goTypes(scrutTy.Args)
	ctorTag := func(name string) goast.Expr {
		return &goast.StarExpr{X: indexExpr(ident(mangleCtor(name)), tagArgs)}
	}

	// The type-switch binding is only legal Go if some clause uses it.
	tsVar := fmt.Sprintf("ts%d", g.tmp)
	g.tmp++
	tsUsed := false

	var clauses []goast.Stmt
	clause := func(c core.CtorCase, isDefault bool) {
		var body []goast.Stmt
		src := tsVar
		if isDefault {
			// Go's default clause leaves the binding at interface type;
			// assert to the (only possible) constructor.
			src = fmt.Sprintf("ts%d", g.tmp)
			g.tmp++
			if anyBind(c.Binds) {
				body = append(body, &goast.AssignStmt{
					Lhs: []goast.Expr{ident(src)},
					Tok: gotoken.DEFINE,
					Rhs: []goast.Expr{&goast.TypeAssertExpr{X: scrut, Type: ctorTag(c.Ctor.Name)}},
				})
			}
		} else if anyBind(c.Binds) {
			tsUsed = true
		}
		fields := t.ADT.InstFields(c.Ctor, scrutTy.Args)
		for i, b := range c.Binds {
			if b == "" {
				continue
			}
			g.caseVarTys[b] = fields[i]
			body = append(body, varDeclStmt(mangleValue(b), g.goType(fields[i]),
				&goast.SelectorExpr{X: ident(src), Sel: ident(fieldName(i))}))
		}
		body = append(body, g.treeStmts(c.Tree, leaf)...)
		var list []goast.Expr
		if !isDefault {
			list = []goast.Expr{ctorTag(c.Ctor.Name)}
		}
		clauses = append(clauses, &goast.CaseClause{List: list, Body: body})
	}

	if full {
		for _, c := range t.Cases[:len(t.Cases)-1] {
			clause(c, false)
		}
		clause(t.Cases[len(t.Cases)-1], true)
	} else {
		for _, c := range t.Cases {
			clause(c, false)
		}
		clauses = append(clauses, &goast.CaseClause{Body: g.treeStmts(t.Default, leaf)})
	}

	var tag goast.Stmt
	if tsUsed {
		tag = &goast.AssignStmt{
			Lhs: []goast.Expr{ident(tsVar)},
			Tok: gotoken.DEFINE,
			Rhs: []goast.Expr{&goast.TypeAssertExpr{X: scrut, Type: nil}},
		}
	} else {
		tag = exprStmt(&goast.TypeAssertExpr{X: scrut, Type: nil})
	}
	return []goast.Stmt{&goast.TypeSwitchStmt{
		Assign: tag,
		Body:   &goast.BlockStmt{List: clauses},
	}}
}

func anyBind(binds []string) bool {
	for _, b := range binds {
		if b != "" {
			return true
		}
	}
	return false
}

// litSwitch emits a value switch (works for int64, float64, and string).
func (g *gen) litSwitch(t *core.SwitchLit, leaf func(core.Expr) []goast.Stmt) []goast.Stmt {
	var clauses []goast.Stmt
	for _, c := range t.Cases {
		clauses = append(clauses, &goast.CaseClause{
			List: []goast.Expr{g.expr(c.Lit, 0)},
			Body: g.treeStmts(c.Tree, leaf),
		})
	}
	clauses = append(clauses, &goast.CaseClause{Body: g.treeStmts(t.Default, leaf)})
	return []goast.Stmt{&goast.SwitchStmt{
		Tag:  ident(mangleValue(t.Scrut)),
		Body: &goast.BlockStmt{List: clauses},
	}}
}

// assignStmts emits e in assign-to-variable statement context: `name = …`
// at the leaves of Lets, Ifs, and Cases — §8.5's ANF target shape.
func (g *gen) assignStmts(e core.Expr, name string) []goast.Stmt {
	switch e := e.(type) {
	case *core.Let:
		return append(g.letBindingStmts(e), g.assignStmts(e.Body, name)...)
	case *core.If:
		return []goast.Stmt{ifStmt(g.expr(e.Cond, 0),
			g.assignStmts(e.Then, name),
			g.assignStmts(e.Else, name))}
	case *core.Case:
		return g.caseStmts(e, func(b core.Expr) []goast.Stmt { return g.assignStmts(b, name) })
	default:
		return []goast.Stmt{assignStmt(name, g.expr(e, 0))}
	}
}

// ---------------------------------------------------------------------------
// Derived operations (§8.6): eq and show per ADT, generated only when a
// program uses `==`/`print` at that type; needs propagate through ADT-typed
// fields.

func (g *gen) adtOf(t types.Type) *types.ADTInfo {
	if con, ok := t.(*types.TCon); ok {
		return g.adts[con.Unique]
	}
	return nil
}

// derivedDecls emits the needed eq/show functions in declaration order.
func (g *gen) derivedDecls(adts []*types.ADTInfo) []goast.Decl {
	var decls []goast.Decl
	for _, adt := range adts {
		if g.neededEq[adt.Con.Unique] {
			decls = append(decls, g.eqDecl(adt))
		}
	}
	for _, adt := range adts {
		if g.neededShow[adt.Con.Unique] {
			decls = append(decls, g.showDecl(adt))
		}
	}
	return decls
}

// eqDecl: func eqT_X[A0 any](eq0 func(A0, A0) bool, a, b T_X[A0]) bool —
// type switch on a, checked assertion on b, field-wise comparison. Type-
// parameter fields compare via the element-op parameters; monomorphic types
// take no element ops and keep the S4 shape exactly.
func (g *gen) eqDecl(adt *types.ADTInfo) goast.Decl {
	g.tyParamNames = tyParamNames(adt.Params)
	g.eqParamNames = map[int]string{}
	paramIdents := make([]goast.Expr, len(adt.Params))
	for i, v := range adt.Params {
		g.eqParamNames[v.ID] = eqParamName(i)
		paramIdents[i] = ident(g.tyParamNames[v.ID])
	}
	defer func() { g.eqParamNames = nil }()
	iface := indexExpr(ident(mangleType(adt.Con.Name)), paramIdents)
	ctorTag := func(name string) goast.Expr {
		return &goast.StarExpr{X: indexExpr(ident(mangleCtor(name)), paramIdents)}
	}

	var clauses []goast.Stmt
	usesBinding := false
	for _, c := range adt.Ctors {
		var body []goast.Stmt
		if len(c.Fields) == 0 {
			// `_, ok := b.(*C_X); return ok`
			body = []goast.Stmt{
				&goast.AssignStmt{
					Lhs: []goast.Expr{ident("_"), ident("ok")},
					Tok: gotoken.DEFINE,
					Rhs: []goast.Expr{&goast.TypeAssertExpr{X: ident("b"), Type: ctorTag(c.Name)}},
				},
				returnStmt(ident("ok")),
			}
		} else {
			usesBinding = true
			body = []goast.Stmt{&goast.AssignStmt{
				Lhs: []goast.Expr{ident("bv"), ident("ok")},
				Tok: gotoken.DEFINE,
				Rhs: []goast.Expr{&goast.TypeAssertExpr{X: ident("b"), Type: ctorTag(c.Name)}},
			}}
			cond := goast.Expr(ident("ok"))
			for i, f := range c.Fields {
				af := &goast.SelectorExpr{X: ident("av"), Sel: ident(fieldName(i))}
				bf := &goast.SelectorExpr{X: ident("bv"), Sel: ident(fieldName(i))}
				cond = binExpr(gotoken.LAND, cond, g.eqField(f, af, bf))
			}
			body = append(body, returnStmt(cond))
		}
		clauses = append(clauses, &goast.CaseClause{
			List: []goast.Expr{ctorTag(c.Name)},
			Body: body,
		})
	}
	var tag goast.Stmt
	if usesBinding {
		tag = &goast.AssignStmt{
			Lhs: []goast.Expr{ident("av")},
			Tok: gotoken.DEFINE,
			Rhs: []goast.Expr{&goast.TypeAssertExpr{X: ident("a"), Type: nil}},
		}
	} else {
		tag = exprStmt(&goast.TypeAssertExpr{X: ident("a"), Type: nil})
	}
	body := []goast.Stmt{
		&goast.TypeSwitchStmt{Assign: tag, Body: &goast.BlockStmt{List: clauses}},
		returnStmt(ident("false")), // unreachable: the switch is total
	}
	params := make([]*goast.Field, 0, len(adt.Params)+1)
	for i, v := range adt.Params {
		pv := ident(g.tyParamNames[v.ID])
		params = append(params, &goast.Field{
			Names: []*goast.Ident{ident(eqParamName(i))},
			Type:  &goast.FuncType{Params: &goast.FieldList{List: []*goast.Field{{Type: pv}, {Type: ident(g.tyParamNames[v.ID])}}}, Results: &goast.FieldList{List: []*goast.Field{{Type: ident("bool")}}}},
		})
	}
	params = append(params, &goast.Field{
		Names: []*goast.Ident{ident("a"), ident("b")}, Type: iface,
	})
	return &goast.FuncDecl{
		Name: ident(eqFunc(adt.Con.Name)),
		Type: &goast.FuncType{
			TypeParams: g.typeParamFields(adt.Params),
			Params:     &goast.FieldList{List: params},
			Results:    &goast.FieldList{List: []*goast.Field{{Type: ident("bool")}}},
		},
		Body: &goast.BlockStmt{List: body},
	}
}

// eqField compares one constructor field: element-op param at a type
// parameter, derived (possibly instantiated) eq at an ADT, native == at a
// scalar.
func (g *gen) eqField(f types.Type, af, bf goast.Expr) goast.Expr {
	if v, ok := f.(*types.TVar); ok && v.Rigid {
		return callExpr(ident(g.eqParamNames[v.ID]), af, bf)
	}
	if g.adtOf(f) != nil {
		return g.eqCall(f, af, bf)
	}
	return binExpr(gotoken.EQL, af, bf)
}

// showDecl: func showT_X(v T_X, nested bool) string — `Circle 2.5`, nested
// field-taking constructors parenthesized (`Just (Circle 2.5)`), String
// fields as source literals. Both backends must format identically; the
// interpreter mirrors this in eval's show.
func (g *gen) showDecl(adt *types.ADTInfo) goast.Decl {
	g.tyParamNames = tyParamNames(adt.Params)
	g.showParamNames = map[int]string{}
	paramIdents := make([]goast.Expr, len(adt.Params))
	for i, v := range adt.Params {
		g.showParamNames[v.ID] = showParamName(i)
		paramIdents[i] = ident(g.tyParamNames[v.ID])
	}
	defer func() { g.showParamNames = nil }()
	ctorTag := func(name string) goast.Expr {
		return &goast.StarExpr{X: indexExpr(ident(mangleCtor(name)), paramIdents)}
	}

	var clauses []goast.Stmt
	usesBinding := false
	for _, c := range adt.Ctors {
		if len(c.Fields) == 0 {
			clauses = append(clauses, &goast.CaseClause{
				List: []goast.Expr{ctorTag(c.Name)},
				Body: []goast.Stmt{returnStmt(stringLit(c.Name))},
			})
			continue
		}
		usesBinding = true
		s := goast.Expr(stringLit(c.Name))
		for i, f := range c.Fields {
			field := &goast.SelectorExpr{X: ident("v"), Sel: ident(fieldName(i))}
			s = binExpr(gotoken.ADD, s, stringLit(" "))
			s = binExpr(gotoken.ADD, s, g.showField(f, field))
		}
		body := []goast.Stmt{
			varDeclStmt("s", ident("string"), s),
			&goast.IfStmt{
				Cond: ident("nested"),
				Body: &goast.BlockStmt{List: []goast.Stmt{
					returnStmt(binExpr(gotoken.ADD, binExpr(gotoken.ADD, stringLit("("), ident("s")), stringLit(")"))),
				}},
			},
			returnStmt(ident("s")),
		}
		clauses = append(clauses, &goast.CaseClause{
			List: []goast.Expr{ctorTag(c.Name)},
			Body: body,
		})
	}
	var tag goast.Stmt
	if usesBinding {
		tag = &goast.AssignStmt{
			Lhs: []goast.Expr{ident("v")},
			Tok: gotoken.DEFINE,
			Rhs: []goast.Expr{&goast.TypeAssertExpr{X: ident("v"), Type: nil}},
		}
	} else {
		tag = exprStmt(&goast.TypeAssertExpr{X: ident("v"), Type: nil})
	}
	body := []goast.Stmt{
		&goast.TypeSwitchStmt{Assign: tag, Body: &goast.BlockStmt{List: clauses}},
		returnStmt(stringLit("")), // unreachable: the switch is total
	}
	params := make([]*goast.Field, 0, len(adt.Params)+2)
	for i, v := range adt.Params {
		params = append(params, &goast.Field{
			Names: []*goast.Ident{ident(showParamName(i))},
			Type: &goast.FuncType{
				Params: &goast.FieldList{List: []*goast.Field{
					{Type: ident(g.tyParamNames[v.ID])}, {Type: ident("bool")},
				}},
				Results: &goast.FieldList{List: []*goast.Field{{Type: ident("string")}}},
			},
		})
	}
	params = append(params,
		&goast.Field{Names: []*goast.Ident{ident("v")}, Type: indexExpr(ident(mangleType(adt.Con.Name)), paramIdents)},
		&goast.Field{Names: []*goast.Ident{ident("nested")}, Type: ident("bool")})
	return &goast.FuncDecl{
		Name: ident(showFunc(adt.Con.Name)),
		Type: &goast.FuncType{
			TypeParams: g.typeParamFields(adt.Params),
			Params:     &goast.FieldList{List: params},
			Results:    &goast.FieldList{List: []*goast.Field{{Type: ident("string")}}},
		},
		Body: &goast.BlockStmt{List: body},
	}
}

// showField renders one constructor field by its type: element-op param at a
// type parameter, derived (possibly instantiated) show at an ADT, fangort at
// a scalar.
func (g *gen) showField(t types.Type, field goast.Expr) goast.Expr {
	if v, ok := t.(*types.TVar); ok && v.Rigid {
		return callExpr(ident(g.showParamNames[v.ID]), field, ident("true"))
	}
	if g.adtOf(t) != nil {
		return g.showCall(t, field, ident("true"))
	}
	g.usesFangort = true
	switch g.unique(t) {
	case g.b.Int.Unique:
		return callExpr(selector("fangort", "ShowInt"), field)
	case g.b.Float.Unique:
		return callExpr(selector("fangort", "ShowFloat"), field)
	case g.b.String.Unique:
		return callExpr(selector("fangort", "ShowStringLiteral"), field)
	case g.b.Bool.Unique:
		return callExpr(selector("fangort", "ShowBool"), field)
	case g.b.Unit.Unique:
		return callExpr(selector("fangort", "ShowUnit"))
	default:
		panic("codegen: unshowable field type " + types.Show(t))
	}
}
