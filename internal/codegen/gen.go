package codegen

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/format"
	gotoken "go/token"
	"math"
	"strconv"

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
	g := &gen{b: b}

	var mainDef *core.Def
	for i := range p.Defs {
		if p.Defs[i].Name == "main" {
			mainDef = &p.Defs[i]
		}
	}
	mainIsUnit := mainDef != nil && g.unique(mainDef.Type) == b.Unit.Unique

	var decls []goast.Decl
	for i := range p.Defs {
		d := &p.Defs[i]
		if d == mainDef && mainIsUnit {
			continue // no package var: the effect runs inside func main()
		}
		decls = append(decls, varDecl(mangleValue(d.Name), g.goType(d.Type), g.expr(d.Body, 0)))
	}

	switch {
	case mainIsUnit:
		decls = append(decls, funcDecl("main", g.stmts(mainDef.Body)...))
	case printMain:
		g.usesFangort = true
		decls = append(decls, funcDecl("main",
			exprStmt(callExpr(selector("fangort", g.printFn(mainDef.Type)), ident(mangleValue("main"))))))
	default:
		decls = append(decls, funcDecl("main", assignBlank(ident(mangleValue("main")))))
	}

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
	usesFangort bool
	usesMath    bool
}

func (g *gen) unique(t types.Type) int {
	if con, ok := t.(*types.TCon); ok && len(con.Args) == 0 {
		return con.Unique
	}
	return -1
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

// goType maps a fango type to its unboxed Go representation (DESIGN.md
// §8.1). Int is int64, not int: identical overflow behavior on every GOARCH.
func (g *gen) goType(t types.Type) goast.Expr {
	switch t := t.(type) {
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
			panic(fmt.Sprintf("codegen: ADT types arrive in S4: %s", t.Name))
		}
	default:
		panic(fmt.Sprintf("codegen: unhandled type %s", types.Show(t)))
	}
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
	if g.unique(let.Rhs.Type()) == g.b.Unit.Unique {
		return g.stmts(let.Rhs)
	}
	stmts := []goast.Stmt{varDeclStmt(mangleValue(let.Name), g.goType(let.Rhs.Type()), g.expr(let.Rhs, 0))}
	if !mentions(let.Body, let.Name) {
		stmts = append(stmts, assignBlank(ident(mangleValue(let.Name))))
	}
	return stmts
}

// mentions reports whether name occurs free-ish in e. No-shadowing makes a
// plain occurrence check exact: an inner Let can never rebind name.
func mentions(e core.Expr, name string) bool {
	switch e := e.(type) {
	case *core.VarRef:
		return e.Name == name
	case *core.Neg:
		return mentions(e.Operand, name)
	case *core.BinOp:
		return mentions(e.L, name) || mentions(e.R, name)
	case *core.If:
		return mentions(e.Cond, name) || mentions(e.Then, name) || mentions(e.Else, name)
	case *core.Print:
		return mentions(e.Arg, name)
	case *core.Let:
		return mentions(e.Rhs, name) || mentions(e.Body, name)
	case *core.App:
		if mentions(e.Callee, name) {
			return true
		}
		for _, a := range e.Args {
			if mentions(a, name) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// stmts emits a Unit-typed expression in statement context — func main()'s
// body. Prints become fangort calls; ifs become genuine Go if statements;
// Let bindings become plain locals.
func (g *gen) stmts(e core.Expr) []goast.Stmt {
	switch e := e.(type) {
	case *core.Print:
		g.usesFangort = true
		return []goast.Stmt{exprStmt(callExpr(
			selector("fangort", g.printFn(e.Arg.Type())), g.expr(e.Arg, 0)))}
	case *core.If:
		return []goast.Stmt{ifStmt(g.expr(e.Cond, 0), g.stmts(e.Then), g.stmts(e.Else))}
	case *core.Let:
		return append(g.letBindingStmts(e), g.stmts(e.Body)...)
	default:
		// Unit-typed but effect-free — unreachable in S1 (Unit is only
		// constructible via print); discard defensively.
		return []goast.Stmt{assignBlank(g.expr(e, 0))}
	}
}

// mangleValue maps a fango value name into the generated package's `v_`
// namespace (constructors use C_, types T_).
func mangleValue(name string) string { return "v_" + name }
