package codegen

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/format"
	gotoken "go/token"
	"math"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Emit lowers a Core program to Go source. Definitions become package vars
// in source order (dependency-ordered by construction — fango's
// use-after-define rule). main's shape follows doc/design.md, "Go backend and runtime": a Unit-typed main has
// its function form invoked inside func main() in statement context (prints happen
// at run time, in order — never in package init); any other main stays a
// package var whose value func main() discards, or — in the test-internal
// print-main mode — prints through fangort, the differential harness's
// observation channel.
//
// Codegen is type-directed and deterministic: same Core in, byte-identical
// Go out.
func Emit(p *core.Prog, b *types.Builtins, printMain bool) ([]byte, error) {
	g := &gen{
		b:          b,
		adts:       map[int]*types.ADTInfo{},
		neededEq:   map[int]bool{},
		neededShow: map[int]bool{},
		scalarEq:   map[int]bool{},
		scalarShow: map[int]bool{},
		caseVarTys: map[string]types.Type{},
		evidence:   map[int][]goast.Expr{},
	}
	for _, adt := range p.ADTs {
		g.adts[adt.Con.Unique] = adt
	}

	var mainDef *core.Def
	for i := range p.Defs {
		if p.Defs[i].Name == "main" {
			mainDef = &p.Defs[i]
		}
	}
	mainIsUnit := mainDef != nil && g.unique(mainDef.Type) == b.Unit.Unique
	mainIsFn := mainDef != nil && len(mainDef.Params) == 1

	decls := append(g.effectDecls(p.Effects), g.adtDecls(p.ADTs)...)
	for i := range p.Defs {
		d := &p.Defs[i]
		if d == mainDef && mainIsUnit {
			continue // no package var: the effect runs inside func main()
		}
		if d.IsWorker() {
			// Includes nullary generic workers — polymorphic values emit as
			// zero-parameter generic functions (doc/design.md, "Go backend and runtime").
			decls = append(decls, g.workerDef(d))
			continue
		}
		g.tyParamNames = nil
		decls = append(decls, varDecl(mangleValue(d.Name), g.goType(d.Type), g.expr(d.Body, 0)))
	}

	switch {
	case mainIsUnit:
		decls = append(decls, funcDecl("main", g.stmts(mainDef.Body)...))
	case mainIsFn:
		decls = append(decls, funcDecl("main", assignBlank(callExpr(ident(mangleValue("main")), unitLit()))))
	case printMain:
		decls = append(decls, funcDecl("main",
			exprStmt(g.printCall(ident(mangleValue("main")), mainDef.Type))))
	default:
		decls = append(decls, funcDecl("main", assignBlank(ident(mangleValue("main")))))
	}

	// Derived eq/show, discovered during emission (on demand, doc/design.md, "Go backend and runtime"), plus
	// the scalar element-op helpers their synthesis demanded.
	decls = append(decls, g.derivedDecls(p.ADTs)...)
	decls = append(decls, g.scalarHelperDecls()...)

	// Imports come from emission (fangort for prints, math for float
	// specials), so they are prepended last — in a fixed order, for
	// deterministic output.
	var paths []string
	if g.usesFangort {
		paths = append(paths, "fangobuild/fangort")
	}
	if g.usesMath {
		paths = append(paths, "math")
	}
	if len(paths) > 0 {
		decls = append([]goast.Decl{importDecl(paths...)}, decls...)
	}

	file := &goast.File{Name: ident("main"), Decls: decls}
	var buf bytes.Buffer
	if err := format.Node(&buf, gotoken.NewFileSet(), file); err != nil {
		return nil, fmt.Errorf("codegen: printing generated Go: %w", err)
	}
	return buf.Bytes(), nil
}

type gen struct {
	b           *types.Builtins
	adts        map[int]*types.ADTInfo
	neededEq    map[int]bool
	neededShow  map[int]bool
	tmp         int // type-switch binding counter (ts0, ts1, …)
	usesFangort bool
	usesMath    bool

	// tyParamNames maps the rigid vars of the definition (or derived
	// function) currently being emitted to their Go type-parameter names
	// (positional: A0, A1, …). Reset per definition.
	tyParamNames map[int]string

	// eqParamNames/showParamNames map an ADT's rigid params to the element-
	// operation parameters of the derived eq/show being emitted (doc/design.md, "Go backend and runtime").
	eqParamNames   map[int]string
	showParamNames map[int]string

	// scalarEq/scalarShow track which scalar element-op helpers (eqInt,
	// showInt, …) call-site synthesis demanded.
	scalarEq   map[int]bool
	scalarShow map[int]bool

	// caseVarTys records the (instantiated) types of case scrutinee binders
	// and field temporaries, so nested constructor switches know their
	// column's type arguments.
	caseVarTys map[string]types.Type
	evidence   map[int][]goast.Expr
}

func (g *gen) unique(t types.Type) int {
	if con, ok := t.(*types.TCon); ok && len(con.Args) == 0 {
		return con.Unique
	}
	return -1
}

// printCall builds the print of a value: fangort.PrintX for scalars, the
// derived show piped through fangort.PrintString for ADTs.
func (g *gen) printCall(arg goast.Expr, t types.Type) goast.Expr {
	g.usesFangort = true
	if g.adtOf(t) != nil {
		return callExpr(selector("fangort", "PrintString"),
			g.showCall(t, arg, ident("false")))
	}
	return callExpr(selector("fangort", g.printFn(t)), arg)
}

// printFn picks the fangort printer for a ground scalar type.
func (g *gen) printFn(t types.Type) string {
	switch g.unique(t) {
	case g.b.Int.Unique:
		return "PrintInt"
	case g.b.Float.Unique:
		return "PrintFloat"
	case g.b.String.Unique:
		return "PrintString"
	case g.b.Bool.Unique:
		return "PrintBool"
	default:
		panic("codegen: no printer for type " + types.Show(t))
	}
}

// workerDef emits a top-level function definition as an uncurried Go func
// (doc/design.md, "Go backend and runtime" item 1): the parameter types peel off the curried fango type, the
// body emits in return-position statement context. A generic definition's
// TyParams become Go type parameters — `any` for General vars,
// fangort.Number for Number-kinded ones (doc/design.md, "Type inference", doc/design.md, "Go backend and runtime").
func (g *gen) workerDef(d *core.Def) goast.Decl {
	g.tyParamNames = tyParamNames(d.TyParams)
	argTys, ret := core.PeelFun(d.Type, len(d.Params))
	params := make([]paramSpec, 0, len(d.EffectParams)+len(d.Params))
	for _, ev := range d.EffectParams {
		name := fmt.Sprintf("ev_%d", ev.Unique)
		params = append(params, paramSpec{name: name, typ: g.effectType(ev)})
		g.evidence[ev.Unique] = append(g.evidence[ev.Unique], ident(name))
	}
	for i, name := range d.Params {
		if name != "_" {
			name = mangleValue(name)
		}
		params = append(params, paramSpec{name: name, typ: g.goType(argTys[i])})
	}
	decl := workerDecl(mangleValue(d.Name), params, g.goType(ret), g.retStmts(d.Body)).(*goast.FuncDecl)
	for _, ev := range d.EffectParams {
		g.evidence[ev.Unique] = g.evidence[ev.Unique][:len(g.evidence[ev.Unique])-1]
	}
	decl.Type.TypeParams = g.typeParamFields(d.TyParams)
	return decl
}

// tyParamNames assigns positional Go names (A0, A1, …) to a definition's
// rigid type variables.
func tyParamNames(vars []*types.TVar) map[int]string {
	if len(vars) == 0 {
		return nil
	}
	m := make(map[int]string, len(vars))
	for i, v := range vars {
		m[v.ID] = fmt.Sprintf("A%d", i)
	}
	return m
}

// typeParamFields builds the [A0 any, A1 fangort.Number] type-parameter list.
func (g *gen) typeParamFields(vars []*types.TVar) *goast.FieldList {
	if len(vars) == 0 {
		return nil
	}
	fields := make([]*goast.Field, len(vars))
	for i, v := range vars {
		var constraint goast.Expr = ident("any")
		if v.Kind == types.Number {
			g.usesFangort = true
			constraint = selector("fangort", "Number")
		}
		fields[i] = &goast.Field{
			Names: []*goast.Ident{ident(g.tyParamNames[v.ID])},
			Type:  constraint,
		}
	}
	return &goast.FieldList{List: fields}
}

// retStmts emits an expression in return-position statement context —
// worker and lambda bodies. Lets become locals, ifs become real Go
// if/return (fib's hot path must not pay an IIFE closure), everything else
// returns directly.
func (g *gen) retStmts(e core.Expr) []goast.Stmt {
	switch e := e.(type) {
	case *core.Let:
		return append(g.letBindingStmts(e), g.retStmts(e.Body)...)
	case *core.If:
		stmts := []goast.Stmt{&goast.IfStmt{
			Cond: g.expr(e.Cond, 0),
			Body: &goast.BlockStmt{List: g.retStmts(e.Then)},
		}}
		return append(stmts, g.retStmts(e.Else)...)
	case *core.Case:
		return g.caseStmts(e, g.retStmts)
	default:
		return []goast.Stmt{returnStmt(g.expr(e, 0))}
	}
}

// goType maps a fango type to its unboxed Go representation (see
// doc/design.md, "Go backend and runtime"). Int is int64, not int: identical
// overflow behavior on every GOARCH.
// Rigid type variables map to the enclosing definition's Go type parameters;
// parameterized ADTs to instantiated generic types (doc/design.md, "Go backend and runtime").
func (g *gen) goType(t types.Type) goast.Expr {
	switch t := t.(type) {
	case *types.TVar:
		if t.Rigid {
			if name, ok := g.tyParamNames[t.ID]; ok {
				return ident(name)
			}
		}
		panic("codegen: type variable outside its definition's type parameters")
	case *types.TFun:
		params := make([]paramSpec, 0, len(t.Eff.Labels)+1)
		for _, l := range types.SortedRow(t.Eff).Labels {
			if l.Name == "IO" {
				continue
			}
			params = append(params, paramSpec{typ: g.effectType(core.EffectInstance{Unique: l.Unique, Name: l.Name, Args: l.Args})})
		}
		params = append(params, paramSpec{typ: g.goType(t.Arg)})
		return &goast.FuncType{Params: paramFields(params), Results: &goast.FieldList{List: []*goast.Field{{Type: g.goType(t.Ret)}}}}
	case *types.TCon:
		switch t.Unique {
		case g.b.Int.Unique:
			return ident("int64")
		case g.b.Float.Unique:
			return ident("float64")
		case g.b.String.Unique:
			return ident("string")
		case g.b.Bool.Unique:
			return ident("bool")
		case g.b.Unit.Unique:
			return &goast.StructType{Fields: &goast.FieldList{}}
		default:
			if adt, ok := g.adts[t.Unique]; ok {
				return indexExpr(ident(mangleType(adt.Con.Name)), g.goTypes(t.Args))
			}
			panic(fmt.Sprintf("codegen: unknown type constructor %s", t.Name))
		}
	default:
		panic(fmt.Sprintf("codegen: unhandled type %s", types.Show(t)))
	}
}

func (g *gen) goTypes(ts []types.Type) []goast.Expr {
	if len(ts) == 0 {
		return nil
	}
	out := make([]goast.Expr, len(ts))
	for i, t := range ts {
		out[i] = g.goType(t)
	}
	return out
}

var goOps = map[string]gotoken.Token{
	"+": gotoken.ADD, "-": gotoken.SUB, "*": gotoken.MUL, "/": gotoken.QUO,
	"++": gotoken.ADD, // String concat is Go's + on strings (doc/design.md, "Go backend and runtime")
	"==": gotoken.EQL, "/=": gotoken.NEQ,
	"<": gotoken.LSS, ">": gotoken.GTR, "<=": gotoken.LEQ, ">=": gotoken.GEQ,
}

// goPrec mirrors Go's binary precedence for the operators fango emits
// (comparisons 3, additive 4, multiplicative 5), so we parenthesize only
// where Go's grammar needs it. Unary minus uses 6: above every binary op.
func goPrec(op string) int {
	switch op {
	case "*", "/":
		return 5
	case "+", "-", "++":
		return 4
	case "==", "/=", "<", ">", "<=", ">=":
		return 3
	default:
		return 0
	}
}

const unaryPrec = 6

// expr emits e in expression context; parentPrec is the precedence of the
// enclosing operator (0 = none) for minimal parenthesization.
func (g *gen) expr(e core.Expr, parentPrec int) goast.Expr {
	switch e := e.(type) {
	case *core.IntLit:
		// At a Number-kinded type parameter, convert explicitly: Go does not
		// implicitly convert untyped constants in operations whose other
		// operand has a type-parameter type. The conversion also pins the
		// doc/design.md, "Testing and performance" semantics — at a float64 instantiation the constant rounds,
		// exactly like the interpreter's numeric promotion.
		if v, ok := e.Ty.(*types.TVar); ok && v.Rigid {
			return callExpr(ident(g.tyParamNames[v.ID]), intLit(e.Val))
		}
		return intLit(e.Val)
	case *core.FloatLit:
		return g.floatLit(e.Val)
	case *core.StringLit:
		return stringLit(e.Val)
	case *core.UnitLit:
		return unitLit()
	case *core.BoolLit:
		return ident(strconv.FormatBool(e.Val))
	case *core.VarRef:
		// Unit is a singleton and Unit-typed locals are never emitted
		// (their effects ran at binding time) — materialize the value.
		if g.unique(e.Ty) == g.b.Unit.Unique {
			return unitLit()
		}
		return ident(mangleValue(e.Name))
	case *core.Let:
		return g.letIIFE(e)
	case *core.Lambda:
		// A typed func literal. Go captures variables by reference, but
		// fango bindings are immutable (the single letrec assignment
		// happens-before any call), so by-reference and by-value are
		// indistinguishable.
		fn := e.Ty.(*types.TFun)
		params := make([]paramSpec, 0, len(fn.Eff.Labels)+1)
		var pushed []int
		for _, l := range types.SortedRow(fn.Eff).Labels {
			if l.Name == "IO" {
				continue
			}
			name := fmt.Sprintf("ev_%d", l.Unique)
			inst := core.EffectInstance{Unique: l.Unique, Name: l.Name, Args: l.Args}
			params = append(params, paramSpec{name: name, typ: g.effectType(inst)})
			g.evidence[l.Unique] = append(g.evidence[l.Unique], ident(name))
			pushed = append(pushed, l.Unique)
		}
		params = append(params, paramSpec{name: func() string {
			if e.Param == "_" {
				return "_"
			}
			return mangleValue(e.Param)
		}(), typ: g.goType(fn.Arg)})
		body := g.retStmts(e.Body)
		for _, unique := range pushed {
			g.evidence[unique] = g.evidence[unique][:len(g.evidence[unique])-1]
		}
		return funcLitParams(params, g.goType(fn.Ret), body)
	case *core.App:
		switch e.CalleeKind {
		case core.Worker:
			ref := e.Callee.(*core.VarRef)
			args := make([]goast.Expr, 0, len(e.EvidenceArgs)+len(e.Args))
			for _, ev := range e.EvidenceArgs {
				stack := g.evidence[ev.Unique]
				if len(stack) == 0 {
					panic("codegen: missing lexical evidence")
				}
				args = append(args, stack[len(stack)-1])
			}
			for _, a := range e.Args {
				args = append(args, g.expr(a, 0))
			}
			// Explicit instantiation, always — never Go's own inference (doc/design.md, "Go backend and runtime").
			return callExpr(indexExpr(ident(mangleValue(ref.Name)), g.goTypes(e.TyArgs)), args...)
		case core.Value:
			// One typed indirect call per application; chains render
			// e(a)(b). Call is a Go primary expression — no parens needed,
			// and a func-literal callee called in place is legal Go.
			args := make([]goast.Expr, 0, len(e.EvidenceArgs)+1)
			for _, ev := range e.EvidenceArgs {
				stack := g.evidence[ev.Unique]
				if len(stack) == 0 {
					panic("codegen: missing lexical evidence")
				}
				args = append(args, stack[len(stack)-1])
			}
			args = append(args, g.expr(e.Args[0], 0))
			return callExpr(g.expr(e.Callee, 0), args...)
		case core.Ctor:
			return g.ctorLit(e)
		default:
			panic("codegen: App with unknown CalleeKind")
		}
	case *core.Neg:
		operand := g.expr(e.Operand, unaryPrec)
		// Guard `--x` (invalid Go) and precedence: parenthesize any
		// non-atomic operand.
		switch operand.(type) {
		case *goast.UnaryExpr, *goast.BinaryExpr:
			operand = &goast.ParenExpr{X: operand}
		}
		return parenIf(parentPrec > 0, &goast.UnaryExpr{Op: gotoken.SUB, X: operand})
	case *core.BinOp:
		// Equality at an ADT type calls the derived eq (doc/design.md, "Go backend and runtime"); everything
		// else — including Number-kinded type params (doc/design.md, "Type inference") — compiles to a
		// native Go operator.
		if e.Op == "==" || e.Op == "/=" {
			if g.adtOf(e.L.Type()) != nil {
				call := g.eqCall(e.L.Type(), g.expr(e.L, 0), g.expr(e.R, 0))
				if e.Op == "/=" {
					return &goast.UnaryExpr{Op: gotoken.NOT, X: call}
				}
				return call
			}
		}
		op, ok := goOps[e.Op]
		if !ok {
			panic(fmt.Sprintf("codegen: unhandled operator %q", e.Op))
		}
		prec := goPrec(e.Op)
		// Left child may share our precedence (left associativity);
		// right child needs parens at equal precedence.
		l := g.expr(e.L, prec)
		r := g.expr(e.R, prec+1)
		return parenIf(prec < parentPrec, binExpr(op, l, r))
	case *core.If:
		// Go has no expression-if: an immediately-invoked typed closure
		// preserves branch laziness and stays gofmt-clean. ANF hoisting, as
		// documented in doc/design.md, "Core and evidence invariants", bypasses this inside function bodies; it
		// remains the top-level-initializer fallback.
		body := []goast.Stmt{
			&goast.IfStmt{
				Cond: g.expr(e.Cond, 0),
				Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.expr(e.Then, 0))}},
			},
			returnStmt(g.expr(e.Else, 0)),
		}
		return callExpr(funcLit(g.goType(e.Ty), body))
	case *core.Case:
		// Expression-context fallback (top-level initializers): an
		// immediately-invoked typed closure, exactly like If above. Inside
		// function bodies the elaborator's ANF hoisting bypasses this.
		return callExpr(funcLit(g.goType(e.Ty), g.caseStmts(e, g.retStmts)))
	case *core.Perform:
		if e.Op.Owner.Name == "IO" {
			g.usesFangort = true
			if e.Op.Name == "print" {
				return callExpr(funcLit(g.goType(e.Ty), []goast.Stmt{exprStmt(g.printCall(g.expr(e.Args[0], 0), e.Args[0].Type())), returnStmt(unitLit())}))
			}
			if e.Op.Name == "readLine" {
				return callExpr(selector("fangort", "ReadLine"))
			}
		}
		stack := g.evidence[e.Effect.Unique]
		if len(stack) == 0 {
			panic("codegen: custom Perform without evidence")
		}
		args := make([]goast.Expr, len(e.Args))
		for i, a := range e.Args {
			args[i] = g.expr(a, 0)
		}
		return callExpr(&goast.SelectorExpr{X: stack[len(stack)-1], Sel: ident("Op_" + e.Op.Name)}, args...)
	case *core.Resume:
		return g.expr(e.Value, parentPrec)
	case *core.Seq:
		return callExpr(funcLit(g.goType(e.Ty), append(g.stmts(e.First), returnStmt(g.expr(e.Then, 0)))))
	case *core.Handle:
		return g.handleExpr(e)
	default:
		panic(fmt.Sprintf("codegen: node %T arrives in a later slice", e))
	}
}

func (g *gen) handleExpr(e *core.Handle) goast.Expr {
	fields := make([]*goast.Field, len(e.Clauses))
	elts := make([]goast.Expr, len(e.Clauses))
	for i, c := range e.Clauses {
		params := make([]paramSpec, len(c.Params))
		for j, p := range c.Params {
			if p == "()" || p == "_" {
				p = "_"
			} else {
				p = mangleValue(p)
			}
			params[j] = paramSpec{name: p, typ: g.goType(c.ParamTypes[j])}
		}
		ft := &goast.FuncType{Params: paramFields(params), Results: &goast.FieldList{List: []*goast.Field{{Type: g.goType(c.ResultType)}}}}
		fields[i] = &goast.Field{Names: []*goast.Ident{ident("Op_" + c.Op.Name)}, Type: ft}
		fn := &goast.FuncLit{Type: ft, Body: &goast.BlockStmt{List: g.resumeStmts(c.Body)}}
		elts[i] = &goast.KeyValueExpr{Key: ident("Op_" + c.Op.Name), Value: fn}
	}
	_ = fields
	st := g.effectType(e.Effect)
	name := fmt.Sprintf("ev%d", g.tmp)
	g.tmp++
	decl := varDeclStmt(name, st, &goast.CompositeLit{Type: st, Elts: elts})
	g.evidence[e.Effect.Unique] = append(g.evidence[e.Effect.Unique], ident(name))
	body := g.expr(e.Body, 0)
	g.evidence[e.Effect.Unique] = g.evidence[e.Effect.Unique][:len(g.evidence[e.Effect.Unique])-1]
	// A handler whose subject does not perform the handled effect still
	// constructs valid lexical evidence; keep the local legal in Go even
	// when no generated operation call refers to it.
	stmts := []goast.Stmt{decl, assignBlank(ident(name))}
	if e.Return == nil {
		stmts = append(stmts, returnStmt(body))
		return callExpr(funcLit(g.goType(e.Ty), stmts))
	}
	p := e.Return.Param
	if p == "_" || p == "()" {
		stmts = append(stmts, assignBlank(body))
	} else {
		stmts = append(stmts, varDeclStmt(mangleValue(p), g.goType(e.Body.Type()), body))
	}
	stmts = append(stmts, returnStmt(g.expr(e.Return.Body, 0)))
	return callExpr(funcLit(g.goType(e.Ty), stmts))
}

// resumeStmts lowers a proven tail-resumptive clause. A tail `resume v`
// becomes a direct return of v from the evidence operation field; the
// caller's ordinary Go continuation then proceeds with that operation
// result. No continuation object or non-local control transfer is needed.
func (g *gen) resumeStmts(e core.Expr) []goast.Stmt {
	switch e := e.(type) {
	case *core.Resume:
		return []goast.Stmt{returnStmt(g.expr(e.Value, 0))}
	case *core.Let:
		return append(g.letBindingStmts(e), g.resumeStmts(e.Body)...)
	case *core.Seq:
		return append(g.stmts(e.First), g.resumeStmts(e.Then)...)
	case *core.If:
		return []goast.Stmt{ifStmt(g.expr(e.Cond, 0), g.resumeStmts(e.Then), g.resumeStmts(e.Else))}
	case *core.Case:
		return g.caseStmts(e, g.resumeStmts)
	default:
		panic(fmt.Sprintf("codegen: non-tail-resumptive clause node %T", e))
	}
}

func (g *gen) effectType(e core.EffectInstance) goast.Expr {
	return indexExpr(ident("Eff_"+e.Name), g.goTypes(e.Args))
}

func (g *gen) effectDecls(effects []*types.EffectInfo) []goast.Decl {
	var out []goast.Decl
	for _, eff := range effects {
		if eff.Name == "IO" {
			continue
		}
		old := g.tyParamNames
		g.tyParamNames = map[int]string{}
		fields := make([]*goast.Field, len(eff.Ops))
		for i, p := range eff.Params {
			g.tyParamNames[p.ID] = fmt.Sprintf("E%d", i)
		}
		// Operation-local polymorphism is rejected at every runtime use in
		// the current tail-resumptive handler runtime. Keeping its otherwise-unrepresentable field slots as
		// any lets unused declarations still have deterministic named structs.
		for _, op := range eff.Ops {
			for _, v := range op.LocalVars {
				g.tyParamNames[v.ID] = "any"
			}
		}
		for i, op := range eff.Ops {
			ps := make([]paramSpec, len(op.ParamTypes))
			for j, t := range op.ParamTypes {
				ps[j] = paramSpec{typ: g.goType(t)}
			}
			fields[i] = &goast.Field{Names: []*goast.Ident{ident("Op_" + op.Name)}, Type: &goast.FuncType{Params: paramFields(ps), Results: &goast.FieldList{List: []*goast.Field{{Type: g.goType(op.ResultType)}}}}}
		}
		spec := &goast.TypeSpec{Name: ident("Eff_" + eff.Name), Type: &goast.StructType{Fields: &goast.FieldList{List: fields}}}
		if len(eff.Params) > 0 {
			fs := make([]*goast.Field, len(eff.Params))
			for i := range fs {
				fs[i] = &goast.Field{Names: []*goast.Ident{ident(fmt.Sprintf("E%d", i))}, Type: ident("any")}
			}
			spec.TypeParams = &goast.FieldList{List: fs}
		}
		out = append(out, &goast.GenDecl{Tok: gotoken.TYPE, Specs: []goast.Spec{spec}})
		g.tyParamNames = old
	}
	return out
}

// floatLit emits a Float literal. Finite non-negative-zero values round-trip
// exactly through the shortest 'g' form; specials cannot be written as Go
// constants and go through math (imported on demand).
func (g *gen) floatLit(v float64) goast.Expr {
	switch {
	case math.IsNaN(v):
		g.usesMath = true
		return callExpr(selector("math", "NaN"))
	case math.IsInf(v, 1):
		g.usesMath = true
		return callExpr(selector("math", "Inf"), intLit(1))
	case math.IsInf(v, -1):
		g.usesMath = true
		return callExpr(selector("math", "Inf"), intLit(-1))
	case v == 0 && math.Signbit(v):
		g.usesMath = true
		return callExpr(selector("math", "Copysign"), intLit(0), intLit(-1))
	case v < 0:
		return &goast.UnaryExpr{
			Op: gotoken.SUB,
			X:  &goast.BasicLit{Kind: gotoken.FLOAT, Value: strconv.FormatFloat(-v, 'g', -1, 64)},
		}
	default:
		return &goast.BasicLit{Kind: gotoken.FLOAT, Value: strconv.FormatFloat(v, 'g', -1, 64)}
	}
}

// letIIFE collapses a Let chain into one immediately-invoked closure:
// `func() T { var v_r float64 = …; …; return result }()`. The expression-
// context fallback; statement contexts (main's body, and function bodies
// emit the bindings as plain Go statements instead.
func (g *gen) letIIFE(e *core.Let) goast.Expr {
	var body []goast.Stmt
	var cur core.Expr = e
	for {
		let, ok := cur.(*core.Let)
		if !ok {
			break
		}
		body = append(body, g.letBindingStmts(let)...)
		cur = let.Body
	}
	body = append(body, returnStmt(g.expr(cur, 0)))
	return callExpr(funcLit(g.goType(e.Ty), body))
}

// letBindingStmts emits one binding: Unit-typed right-hand sides run as
// statements (their value is the singleton; prints must still execute),
// other bindings become `var` declarations, kept alive with `_ =` when the
// rest of the chain never mentions them (Go rejects unused locals; fango
// bindings still evaluate eagerly).
func (g *gen) letBindingStmts(let *core.Let) []goast.Stmt {
	if let.Rec {
		// A Go local is not in scope inside its own initializer:
		// declare, then assign — the standard recursive-closure idiom.
		name := mangleValue(let.Name)
		return []goast.Stmt{
			varDeclNoValue(name, g.goType(let.Rhs.Type())),
			assignStmt(name, g.expr(let.Rhs, 0)),
		}
	}
	if g.unique(let.Rhs.Type()) == g.b.Unit.Unique {
		return g.stmts(let.Rhs)
	}
	name := mangleValue(let.Name)
	var stmts []goast.Stmt
	switch let.Rhs.(type) {
	case *core.If, *core.Case:
		// The ANF target shape from doc/design.md, "Core and evidence invariants": declare, then assign inside real Go
		// statements — no IIFE closure on hot paths.
		stmts = append([]goast.Stmt{varDeclNoValue(name, g.goType(let.Rhs.Type()))},
			g.assignStmts(let.Rhs, name)...)
	default:
		stmts = []goast.Stmt{varDeclStmt(name, g.goType(let.Rhs.Type()), g.expr(let.Rhs, 0))}
	}
	if !core.Mentions(let.Body, let.Name) {
		stmts = append(stmts, assignBlank(ident(name)))
	}
	return stmts
}

// stmts emits a Unit-typed expression in statement context — func main()'s
// body. Prints become fangort calls; ifs become genuine Go if statements;
// Let bindings become plain locals.
func (g *gen) stmts(e core.Expr) []goast.Stmt {
	switch e := e.(type) {
	case *core.Perform:
		if e.Op.Owner.Name == "IO" && e.Op.Name == "print" {
			return []goast.Stmt{exprStmt(g.printCall(g.expr(e.Args[0], 0), e.Args[0].Type()))}
		}
		return []goast.Stmt{assignBlank(g.expr(e, 0))}
	case *core.Seq:
		return append(g.stmts(e.First), g.stmts(e.Then)...)
	case *core.If:
		return []goast.Stmt{ifStmt(g.expr(e.Cond, 0), g.stmts(e.Then), g.stmts(e.Else))}
	case *core.Case:
		return g.caseStmts(e, g.stmts)
	case *core.Let:
		return append(g.letBindingStmts(e), g.stmts(e.Body)...)
	default:
		// Unit-typed but effect-free — normally unreachable because Unit is
		// constructible via print); discard defensively.
		return []goast.Stmt{assignBlank(g.expr(e, 0))}
	}
}

// mangleValue maps a fango value name into the generated package's `v_`
// namespace (constructors use C_, types T_). Elaboration temporaries start
// with `_` — unlexable as fango identifiers — and land in a disjoint `t`
// namespace (`_w0` → `t_w0`) so they can never collide with user names.
func mangleValue(name string) string {
	if strings.HasPrefix(name, "_") {
		return "t" + name
	}
	return "v_" + name
}
