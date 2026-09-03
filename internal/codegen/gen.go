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
// use-after-define rule). main's shape follows §8.4: a Unit-typed main has
// its effect forced inside func main() in statement context (prints happen
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

	decls := g.adtDecls(p.ADTs)
	for i := range p.Defs {
		d := &p.Defs[i]
		if d == mainDef && mainIsUnit {
			continue // no package var: the effect runs inside func main()
		}
		if d.IsWorker() {
			// Includes nullary generic workers — polymorphic values emit as
			// zero-parameter generic functions (§8.4).
			decls = append(decls, g.workerDef(d))
			continue
		}
		g.tyParamNames = nil
		decls = append(decls, varDecl(mangleValue(d.Name), g.goType(d.Type), g.expr(d.Body, 0)))
	}

	switch {
	case mainIsUnit:
		decls = append(decls, funcDecl("main", g.stmts(mainDef.Body)...))
	case printMain:
		decls = append(decls, funcDecl("main",
			exprStmt(g.printCall(ident(mangleValue("main")), mainDef.Type))))
	default:
		decls = append(decls, funcDecl("main", assignBlank(ident(mangleValue("main")))))
	}

	// Derived eq/show, discovered during emission (on demand, §8.6), plus
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
	// operation parameters of the derived eq/show being emitted (§8.6).
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
// (§8.2 item 1): the parameter types peel off the curried fango type, the
// body emits in return-position statement context. A generic definition's
// TyParams become Go type parameters — `any` for General vars,
// fangort.Number for Number-kinded ones (§7.3, §8.4).
func (g *gen) workerDef(d *core.Def) goast.Decl {
	g.tyParamNames = tyParamNames(d.TyParams)
	argTys, ret := core.PeelFun(d.Type, len(d.Params))
	params := make([]paramSpec, len(d.Params))
	for i, name := range d.Params {
		params[i] = paramSpec{name: mangleValue(name), typ: g.goType(argTys[i])}
	}
	decl := workerDecl(mangleValue(d.Name), params, g.goType(ret), g.retStmts(d.Body)).(*goast.FuncDecl)
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

// goType maps a fango type to its unboxed Go representation (DESIGN.md
// §8.1). Int is int64, not int: identical overflow behavior on every GOARCH.
// Rigid type variables map to the enclosing definition's Go type parameters;
// parameterized ADTs to instantiated generic types (§8.4).
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
		return funcType(g.goType(t.Arg), g.goType(t.Ret))
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
	"++": gotoken.ADD, // String concat is Go's + on strings (§8.6)
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
		// §9.6 semantics — at a float64 instantiation the constant rounds,
		// exactly like the interpreter's numeric promotion.
		if v, ok := e.Ty.(*types.TVar); ok && v.Rigid {
			return callExpr(ident(g.tyParamNames[v.ID]), intLit(e.Val))
		}
		return intLit(e.Val)
	case *core.FloatLit:
		return g.floatLit(e.Val)
	case *core.StringLit:
		return stringLit(e.Val)
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
		return funcLitParams(
			[]paramSpec{{name: mangleValue(e.Param), typ: g.goType(fn.Arg)}},
			g.goType(fn.Ret),
			g.retStmts(e.Body))
	case *core.App:
		switch e.CalleeKind {
		case core.Worker:
			ref := e.Callee.(*core.VarRef)
			args := make([]goast.Expr, len(e.Args))
			for i, a := range e.Args {
				args[i] = g.expr(a, 0)
			}
			// Explicit instantiation, always — never Go's own inference (§8.4).
			return callExpr(indexExpr(ident(mangleValue(ref.Name)), g.goTypes(e.TyArgs)), args...)
		case core.Value:
			// One typed indirect call per application; chains render
			// e(a)(b). Call is a Go primary expression — no parens needed,
			// and a func-literal callee called in place is legal Go.
			return callExpr(g.expr(e.Callee, 0), g.expr(e.Args[0], 0))
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
		// Equality at an ADT type calls the derived eq (§8.6); everything
		// else — including Number-kinded type params (§7.3) — compiles to a
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
		// preserves branch laziness and stays gofmt-clean. §8.5's ANF
		// hoisting (S4) will bypass this inside function bodies; it
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
	case *core.Print:
		panic("codegen: core.Print is statement-only — a Unit value reached expression context")
	default:
		panic(fmt.Sprintf("codegen: node %T arrives in a later slice", e))
	}
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
// from S3) emit the bindings as plain Go statements instead.
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
		// §8.5's ANF target shape: declare, then assign inside real Go
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
	case *core.Print:
		return []goast.Stmt{exprStmt(g.printCall(g.expr(e.Arg, 0), e.Arg.Type()))}
	case *core.If:
		return []goast.Stmt{ifStmt(g.expr(e.Cond, 0), g.stmts(e.Then), g.stmts(e.Else))}
	case *core.Case:
		return g.caseStmts(e, g.stmts)
	case *core.Let:
		return append(g.letBindingStmts(e), g.stmts(e.Body)...)
	default:
		// Unit-typed but effect-free — unreachable in S1 (Unit is only
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
