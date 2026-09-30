package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// ADT lowering uses value products, pointer products for recursive layouts,
// tagged bundled sums, and marker interfaces for other unions. See
// doc/design/backend.md, "Representations and ABI".

func mangleType(name string) string   { return "T_" + linkName(name) }
func mangleCtor(name string) string   { return "C_" + linkName(name) }
func markerMethod(name string) string { return "isT_" + linkName(name) }

func fieldName(i int) string { return fmt.Sprintf("F%d", i) }

// adtDecls emits module-owned generic types in declaration order. Transport
// variants alias the same value layout.
func (g *gen) adtDecls(adts []*types.ADTInfo) []goast.Decl {
	var decls []goast.Decl
	for _, adt := range adts {
		if adt.Repr != types.ReprADT {
			continue
		}
		runtimeParams := runtimeADTParams(adt)
		modes := []types.Transport{types.Direct}
		if g.controlledType(adt.Con, nil) {
			modes = append(modes, types.Exit)
		}
		for _, mode := range modes {
			oldControl, oldABI := g.control, g.abi
			g.control, g.abi = mode, mode
			g.tyParamNames = tyParamNames(runtimeParams)
			var args []goast.Expr
			for _, v := range runtimeParams {
				args = append(args, ident(g.tyParamNames[v.ID]))
			}
			name, marker := mangleType(adt.Con.Name), markerMethod(adt.Con.Name)
			if mode == types.Exit {
				names := []string{name}
				for _, ctor := range adt.Ctors {
					names = append(names, mangleCtor(ctor.Name))
				}
				for _, n := range names {
					decls = append(decls, &goast.GenDecl{Tok: gotoken.TYPE, Specs: []goast.Spec{&goast.TypeSpec{Name: ident(n + "_exit"), TypeParams: g.typeParamFields(runtimeParams), Assign: 1, Type: indexExpr(ident(n), args)}}})
				}
			} else if taggedADT(adt) {
				decls = append(decls, g.taggedDecl(adt, args)...)
			} else {
				var nominal goast.Expr
				alias := gotoken.Pos(0)
				if productADT(adt) {
					nominal = indexExpr(ident(mangleCtor(adt.Ctors[0].Name)), args)
					if !g.valueProduct(adt) {
						nominal = &goast.StarExpr{X: nominal}
					}
					alias = 1
				} else {
					nominal = &goast.InterfaceType{Methods: &goast.FieldList{List: []*goast.Field{{Names: []*goast.Ident{ident(marker)}, Type: &goast.FuncType{Params: &goast.FieldList{}}}}}}
				}
				decls = append(decls, &goast.GenDecl{Tok: gotoken.TYPE, Specs: []goast.Spec{&goast.TypeSpec{Name: ident(name), TypeParams: g.typeParamFields(runtimeParams), Assign: alias, Type: nominal}}})
				for _, ctor := range adt.Ctors {
					var fields []*goast.Field
					for i, f := range ctor.Fields {
						fields = append(fields, &goast.Field{Names: []*goast.Ident{ident(fieldName(i))}, Type: g.goType(f)})
					}
					ctorName := mangleCtor(ctor.Name)
					decls = append(decls, &goast.GenDecl{Tok: gotoken.TYPE, Specs: []goast.Spec{&goast.TypeSpec{Name: ident(ctorName), TypeParams: g.typeParamFields(runtimeParams), Type: &goast.StructType{Fields: &goast.FieldList{List: fields}}}}})
					if !productADT(adt) {
						decls = append(decls, &goast.FuncDecl{Name: ident(marker), Recv: &goast.FieldList{List: []*goast.Field{{Type: indexExpr(ident(ctorName), args)}}}, Type: &goast.FuncType{Params: &goast.FieldList{}}, Body: &goast.BlockStmt{}})
					}
				}
			}
			g.control, g.abi = oldControl, oldABI
		}
	}
	g.tyParamNames = nil
	return decls
}

// ctorLit is a saturated constructor application. Parameterized constructors
// instantiate at the result type's runtime arguments.
func (g *gen) ctorLit(e *core.App) goast.Expr {
	args := make([]goast.Expr, len(e.Args))
	for i, a := range e.Args {
		args[i] = g.expr(a, 0)
	}
	adt := g.adts[e.Ctor.Result.Unique]
	typeArgs := e.TyArgs
	if adt != nil {
		typeArgs = runtimeADTArgs(adt, typeArgs)
	}
	if e.Ctor.Repr == types.ReprBytes {
		// The declaration's one constructor is the empty sequence
		// (internal/infer/bytes.go); it carries no fields to evaluate.
		g.usesFangort = true
		return callExpr(selector("fangort", "BytesEmpty"))
	}
	if e.Ctor.Repr == types.ReprList {
		// Go evaluates call arguments left to right, exactly as it does
		// composite-literal elements, so the source evaluation order a cons
		// guarantees survives the change of form. The instantiation is
		// explicit because an argument may be an untyped constant or an
		// erased Unit, and because emission has to stay deterministic.
		g.usesFangort = true
		fn := "ListNil"
		if e.Ctor.Index == listConsIndex {
			fn = "ListCons"
		}
		return callExpr(indexExpr(selector("fangort", fn), g.goTypes(typeArgs)), args...)
	}
	return g.ctorValue(e.Ctor, typeArgs, args...)
}

// ctorValue builds a constructor in its selected layout.
// typeArgs are already reduced to the runtime parameters.
// Native-boundary emission shares this with ctorLit so a Result or a wrapper
// built at a sidecar call has exactly the representation a literal has.
func (g *gen) ctorValue(ctor *types.CtorInfo, typeArgs []types.Type, elts ...goast.Expr) goast.Expr {
	if ctor.Repr == types.ReprNativeAny {
		return ident("nil")
	}
	adt := g.adts[ctor.Result.Unique]
	if taggedADT(adt) {
		fields := []goast.Expr{&goast.KeyValueExpr{Key: ident("Tag"), Value: intLit(int64(ctor.Index))}}
		for i, elt := range elts {
			fields = append(fields, &goast.KeyValueExpr{Key: ident(representationField(adt, ctor, i)), Value: elt})
		}
		return &goast.CompositeLit{Type: indexExpr(g.typeRef(adt), g.goTypes(typeArgs)), Elts: fields}
	}
	litType := indexExpr(g.ctorRef(ctor), g.goTypes(typeArgs))
	literal := &goast.CompositeLit{Type: litType, Elts: elts}
	if g.valueProduct(g.adts[ctor.Result.Unique]) {
		return literal
	}
	return &goast.UnaryExpr{
		Op: gotoken.AND,
		X:  literal,
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
	// A multi-column tree tests worker parameters directly, so its binder can
	// go unmentioned — and Go rejects an unused variable. Discard the value
	// instead, or emit nothing at all when the scrutinee is a bound local and
	// re-reading it could not have an effect.
	var stmts []goast.Stmt
	switch {
	case core.TreeMentions(e.Tree, e.Bind):
		stmts = append(stmts, varDeclStmt(bind, g.goType(e.Scrut.Type()), g.expr(e.Scrut, 0)))
	case !isLocalVarRef(e.Scrut):
		stmts = append(stmts, assignStmt("_", g.expr(e.Scrut, 0)))
	}
	return append(stmts, g.treeStmts(e.Tree, leaf)...)
}

func (g *gen) treeStmts(t core.Tree, leaf func(core.Expr) []goast.Stmt) []goast.Stmt {
	switch t := t.(type) {
	case *core.Unreachable:
		return []goast.Stmt{exprStmt(callExpr(ident("panic"), stringLit("unreachable pattern match")))}
	case *core.Guard:
		return []goast.Stmt{ifStmt(g.expr(t.Cond, 0), g.treeStmts(t.Then, leaf), g.treeStmts(t.Else, leaf))}
	case *core.Leaf:
		return leaf(t.Body)
	case *core.SwitchCtor:
		if t.ADT.Con.Unique == g.b.Bool.Unique {
			return g.boolSwitch(t, leaf)
		}
		if t.ADT.Repr == types.ReprList {
			return g.listSwitch(t, leaf)
		}
		if t.ADT.Repr == types.ReprBytes {
			return g.bytesSwitch(t, leaf)
		}
		if taggedADT(t.ADT) {
			return g.taggedSwitch(t, leaf)
		}
		if productADT(t.ADT) {
			return g.productSwitch(t, leaf)
		}
		return g.ctorSwitch(t, leaf)
	case *core.SwitchLit:
		return g.litSwitch(t, leaf)
	default:
		panic(fmt.Sprintf("codegen: unhandled tree node %T", t))
	}
}

// boolSwitch: `case` on Bool compiles to if/else (doc/design.md, "Go backend and runtime") — Bool stays native.
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

// Nil and Cons occupy these positions in the bundled declaration; infer's
// markListRepr verifies that before any of this runs, because head/tail
// projection is compiled against the layout rather than looked up.
const (
	listNilIndex  = 0
	listConsIndex = 1
)

// listSwitch: `case` on the bundled List compiles to an emptiness test with
// head/tail projections (doc/roadmap-list.md) — List has a runtime
// representation, so there is no interface to switch on. Neither of
// ctorSwitch's Go-imposed complications applies: no type switch means no
// full-coverage `default:` re-assertion and no binding that Go could reject as
// unused. The scrutinee is read up to three times, which is safe because
// caseStmts guarantees it is a bound local and all three accessors are pure.
func (g *gen) listSwitch(t *core.SwitchCtor, leaf func(core.Expr) []goast.Stmt) []goast.Stmt {
	scrutTy, ok := g.caseVarTys[t.Scrut].(*types.TCon)
	if !ok {
		panic("codegen: SwitchCtor scrutinee type unknown")
	}
	// Cons's fields, in declaration order.
	accessor := []string{"Head", "Tail"}

	branch := func(index int) []goast.Stmt {
		for _, c := range t.Cases {
			if c.Ctor.Index != index {
				continue
			}
			var body []goast.Stmt
			fields := t.ADT.InstFields(c.Ctor, scrutTy.Args)
			for i, b := range c.Binds {
				if b == "" {
					continue
				}
				// Nested list patterns read their column's type from here.
				g.caseVarTys[b] = fields[i]
				body = append(body, varDeclStmt(mangleValue(b), g.goType(fields[i]),
					callExpr(selector(mangleValue(t.Scrut), accessor[i]))))
				if g.isUnit(fields[i]) {
					// As in ctorSwitch: a Unit binding the branch mentions in
					// Fango still emits no use, and Go rejects that.
					body = append(body, assignBlank(ident(mangleValue(b))))
				}
			}
			return append(body, g.treeStmts(c.Tree, leaf)...)
		}
		if t.Default == nil {
			panic("codegen: list switch missing a constructor with no default — coverage is broken")
		}
		return g.treeStmts(t.Default, leaf)
	}

	return []goast.Stmt{ifStmt(callExpr(selector(mangleValue(t.Scrut), "IsEmpty")),
		branch(listNilIndex),
		branch(listConsIndex))}
}

// bytesSwitch: the bundled Bytes declares one nullary constructor, so a match
// on it discriminates nothing and binds nothing. Emit the branch, or the
// default when the tree covers the type that way.
func (g *gen) bytesSwitch(t *core.SwitchCtor, leaf func(core.Expr) []goast.Stmt) []goast.Stmt {
	if len(t.Cases) > 0 {
		return g.treeStmts(t.Cases[0].Tree, leaf)
	}
	if t.Default == nil {
		panic("codegen: Bytes switch with neither a case nor a default — coverage is broken")
	}
	return g.treeStmts(t.Default, leaf)
}

// ctorSwitch emits a Go type switch. Full coverage turns the LAST case into
// `default:` with a checked assertion instead of a type-switch binding —
// exhaustiveness proved there is no other constructor, so no panic path is
// reachable (doc/design.md, "Core and evidence invariants").
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
	tagArgs := g.goTypes(runtimeADTArgs(t.ADT, scrutTy.Args))
	ctorTag := func(name string) goast.Expr {
		ctor := t.ADT.CtorNamed(name)
		return &goast.StarExpr{X: indexExpr(g.ctorRef(ctor), tagArgs)}
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
			if g.isUnit(fields[i]) {
				// A Unit-typed binding emits as statements rather than a Go local,
				// so the branch can mention the field in Fango and still leave this
				// declaration unused, which Go rejects.
				body = append(body, assignBlank(ident(mangleValue(b))))
			}
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

// isLocalVarRef reports whether e is already a bound local, so re-evaluating
// it would be a no-op.
func isLocalVarRef(e core.Expr) bool {
	v, ok := e.(*core.VarRef)
	return ok && v.Local
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
// at the leaves of Lets, Ifs, and Cases — the ANF target shape documented in
// doc/design.md, "Core and evidence invariants".
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

// Exit-valued assignments use the enclosing function's propagation boundary.
// Branching no longer needs a function literal merely to return an Outcome.
func (g *gen) assignOutcomeStmts(e core.Expr, name string) []goast.Stmt {
	switch e := e.(type) {
	case *core.Let:
		return append(g.letBindingStmts(e), g.assignOutcomeStmts(e.Body, name)...)
	case *core.If:
		return []goast.Stmt{ifStmt(g.expr(e.Cond, 0), g.assignOutcomeStmts(e.Then, name), g.assignOutcomeStmts(e.Else, name))}
	case *core.Case:
		return g.caseStmts(e, func(b core.Expr) []goast.Stmt { return g.assignOutcomeStmts(b, name) })
	case *core.Seq:
		return append(g.stmts(e.First), g.assignOutcomeStmts(e.Then, name)...)
	default:
		if core.ExprControl(e).Resolve(g.control) == types.Exit {
			return []goast.Stmt{assignStmt(name, g.expr(e, 0))}
		}
		if g.isUnit(e.Type()) {
			return append(g.stmts(e), assignStmt(name, g.normalOutcome(e.Type(), g.unitValue())))
		}
		return []goast.Stmt{assignStmt(name, g.normalOutcome(e.Type(), g.expr(e, 0)))}
	}
}

// ---------------------------------------------------------------------------
// Derived operations (doc/design.md, "Go backend and runtime"): eq and show per ADT, generated only when a
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
		if !g.neededEq[adt.Con.Unique] {
			continue
		}
		if adt.Repr == types.ReprList {
			decls = append(decls, g.listEqDecl(adt))
			continue
		}
		if adt.Repr == types.ReprBytes {
			decls = append(decls, g.bytesEqDecl(adt))
			continue
		}
		decls = append(decls, g.eqDecl(adt))
	}
	for _, adt := range adts {
		if !g.neededShow[adt.Con.Unique] {
			continue
		}
		if adt.Repr == types.ReprList {
			decls = append(decls, g.listShowDecl(adt))
			continue
		}
		if adt.Repr == types.ReprBytes {
			decls = append(decls, g.bytesShowDecl(adt))
			continue
		}
		decls = append(decls, g.showDecl(adt))
	}
	return decls
}

// listEqDecl and listShowDecl keep the exported name, owner, and generic
// signature the emitted versions have, and only change the body to the runtime
// implementation. That is what lets the element-op synthesis in generic.go
// stay untouched: a call site builds the same instantiated call and the same
// element operations whatever the type's representation is.
func (g *gen) listEqDecl(adt *types.ADTInfo) goast.Decl {
	runtimeParams := runtimeADTParams(adt)
	g.tyParamNames = tyParamNames(runtimeParams)
	defer func() { g.tyParamNames = nil }()
	g.usesFangort = true
	elem := ident(g.tyParamNames[runtimeParams[0].ID])
	eq := eqParamName(0)
	return &goast.FuncDecl{
		Name: ident(g.eqName(adt)),
		Type: &goast.FuncType{
			TypeParams: g.typeParamFields(runtimeParams),
			Params: &goast.FieldList{List: []*goast.Field{
				{Names: []*goast.Ident{ident(eq)}, Type: &goast.FuncType{
					Params:  &goast.FieldList{List: []*goast.Field{{Type: elem}, {Type: elem}}},
					Results: &goast.FieldList{List: []*goast.Field{{Type: ident("bool")}}},
				}},
				{Names: []*goast.Ident{ident("a"), ident("b")}, Type: indexExpr(selector("fangort", "List"), []goast.Expr{elem})},
			}},
			Results: &goast.FieldList{List: []*goast.Field{{Type: ident("bool")}}},
		},
		Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(
			callExpr(selector("fangort", "ListEq"), ident(eq), ident("a"), ident("b")))}},
	}
}

func (g *gen) listShowDecl(adt *types.ADTInfo) goast.Decl {
	runtimeParams := runtimeADTParams(adt)
	g.tyParamNames = tyParamNames(runtimeParams)
	defer func() { g.tyParamNames = nil }()
	g.usesFangort = true
	elem := ident(g.tyParamNames[runtimeParams[0].ID])
	show := showParamName(0)
	return &goast.FuncDecl{
		Name: ident(g.showName(adt)),
		Type: &goast.FuncType{
			TypeParams: g.typeParamFields(runtimeParams),
			Params: &goast.FieldList{List: []*goast.Field{
				{Names: []*goast.Ident{ident(show)}, Type: &goast.FuncType{
					Params:  &goast.FieldList{List: []*goast.Field{{Type: elem}, {Type: ident("bool")}}},
					Results: &goast.FieldList{List: []*goast.Field{{Type: ident("string")}}},
				}},
				{Names: []*goast.Ident{ident("v")}, Type: indexExpr(selector("fangort", "List"), []goast.Expr{elem})},
				{Names: []*goast.Ident{ident("nested")}, Type: ident("bool")},
			}},
			Results: &goast.FieldList{List: []*goast.Field{{Type: ident("string")}}},
		},
		Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(
			callExpr(selector("fangort", "ListShow"), ident(show), ident("v"), ident("nested")))}},
	}
}

// bytesEqDecl and bytesShowDecl do for Bytes what listEqDecl and
// listShowDecl do for List: keep the exported name and signature the emitted
// derivations have, and only change the body to the runtime implementation.
// Bytes takes no type parameters, so neither takes an element operation.
func (g *gen) bytesEqDecl(adt *types.ADTInfo) goast.Decl {
	g.usesFangort = true
	return &goast.FuncDecl{
		Name: ident(g.eqName(adt)),
		Type: &goast.FuncType{
			Params: &goast.FieldList{List: []*goast.Field{
				{Names: []*goast.Ident{ident("a"), ident("b")}, Type: selector("fangort", "Bytes")},
			}},
			Results: &goast.FieldList{List: []*goast.Field{{Type: ident("bool")}}},
		},
		Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(
			callExpr(selector("fangort", "BytesEq"), ident("a"), ident("b")))}},
	}
}

// The `nested` parameter is accepted and ignored: the quoted escaped form
// needs no parentheses at any depth.
func (g *gen) bytesShowDecl(adt *types.ADTInfo) goast.Decl {
	g.usesFangort = true
	return &goast.FuncDecl{
		Name: ident(g.showName(adt)),
		Type: &goast.FuncType{
			Params: &goast.FieldList{List: []*goast.Field{
				{Names: []*goast.Ident{ident("v")}, Type: selector("fangort", "Bytes")},
				{Names: []*goast.Ident{ident("_")}, Type: ident("bool")},
			}},
			Results: &goast.FieldList{List: []*goast.Field{{Type: ident("string")}}},
		},
		Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(
			callExpr(selector("fangort", "BytesShow"), ident("v")))}},
	}
}

// eqDecl: func eqT_X[A0 any](eq0 func(A0, A0) bool, a, b T_X[A0]) bool —
// type switch on a, checked assertion on b, field-wise comparison. Type-
// parameter fields compare via the element-op parameters; monomorphic types
// take no element operations and keep the monomorphic shape exactly.
func (g *gen) eqDecl(adt *types.ADTInfo) goast.Decl {
	runtimeParams := runtimeADTParams(adt)
	g.tyParamNames = tyParamNames(runtimeParams)
	g.eqParamNames = map[int]string{}
	paramIdents := make([]goast.Expr, len(runtimeParams))
	for i, v := range runtimeParams {
		g.eqParamNames[v.ID] = eqParamName(i)
		paramIdents[i] = ident(g.tyParamNames[v.ID])
	}
	defer func() { g.eqParamNames = nil }()
	iface := indexExpr(g.typeRef(adt), paramIdents)
	ctorTag := func(name string) goast.Expr {
		return &goast.StarExpr{X: indexExpr(g.ctorRef(adt.CtorNamed(name)), paramIdents)}
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
	if productADT(adt) {
		body = g.productEq(adt)
	}
	if taggedADT(adt) {
		body = g.taggedEq(adt)
	}
	params := make([]*goast.Field, 0, len(runtimeParams)+1)
	for i, v := range runtimeParams {
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
		Name: ident(g.eqName(adt)),
		Type: &goast.FuncType{
			TypeParams: g.typeParamFields(runtimeParams),
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
	runtimeParams := runtimeADTParams(adt)
	g.tyParamNames = tyParamNames(runtimeParams)
	g.showParamNames = map[int]string{}
	paramIdents := make([]goast.Expr, len(runtimeParams))
	for i, v := range runtimeParams {
		g.showParamNames[v.ID] = showParamName(i)
		paramIdents[i] = ident(g.tyParamNames[v.ID])
	}
	defer func() { g.showParamNames = nil }()
	ctorTag := func(name string) goast.Expr {
		return &goast.StarExpr{X: indexExpr(g.ctorRef(adt.CtorNamed(name)), paramIdents)}
	}

	var clauses []goast.Stmt
	usesBinding := false
	for _, c := range adt.Ctors {
		if len(c.Fields) == 0 {
			clauses = append(clauses, &goast.CaseClause{
				List: []goast.Expr{ctorTag(c.Name)},
				Body: []goast.Stmt{returnStmt(stringLit(types.SurfaceName(c.Name)))},
			})
			continue
		}
		usesBinding = true
		s := goast.Expr(stringLit(types.SurfaceName(c.Name)))
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
	if productADT(adt) {
		body = g.constructorShow(adt.Ctors[0], "v")
	}
	if taggedADT(adt) {
		body = g.taggedShow(adt)
	}
	params := make([]*goast.Field, 0, len(runtimeParams)+2)
	for i, v := range runtimeParams {
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
		&goast.Field{Names: []*goast.Ident{ident("v")}, Type: indexExpr(g.typeRef(adt), paramIdents)},
		&goast.Field{Names: []*goast.Ident{ident("nested")}, Type: ident("bool")})
	return &goast.FuncDecl{
		Name: ident(g.showName(adt)),
		Type: &goast.FuncType{
			TypeParams: g.typeParamFields(runtimeParams),
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
	case g.b.Char.Unique:
		return callExpr(selector("fangort", "ShowCharLiteral"), field)
	case g.b.Bool.Unique:
		return callExpr(selector("fangort", "ShowBool"), field)
	case g.b.Unit.Unique:
		return callExpr(selector("fangort", "ShowUnit"))
	default:
		panic("codegen: unshowable field type " + types.Show(t))
	}
}
