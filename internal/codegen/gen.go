package codegen

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/format"
	gotoken "go/token"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Emit lowers a Core program to Go source: one package var per definition
// (dependency-ordered by construction — fango's use-after-define rule), and
// a main that either discards v_main or, in the test-internal print-main
// mode, prints it through fangort — the differential harness's observation
// channel for programs that produce no output yet.
//
// Codegen is type-directed and deterministic: same Core in, byte-identical
// Go out.
func Emit(p *core.Prog, b *types.Builtins, printMain bool) ([]byte, error) {
	g := &gen{b: b}

	var decls []goast.Decl
	if printMain {
		decls = append(decls, importDecl("fangobuild/fangort"))
	}
	for _, d := range p.Defs {
		decls = append(decls, varDecl(mangleValue(d.Name), g.goType(d.Type), g.expr(d.Body, 0)))
	}
	if printMain {
		decls = append(decls, funcDecl("main",
			exprStmt(callExpr(selector("fangort", "PrintInt"), ident(mangleValue("main"))))))
	} else {
		decls = append(decls, funcDecl("main", assignBlank(ident(mangleValue("main")))))
	}

	file := &goast.File{Name: ident("main"), Decls: decls}
	var buf bytes.Buffer
	if err := format.Node(&buf, gotoken.NewFileSet(), file); err != nil {
		return nil, fmt.Errorf("codegen: printing generated Go: %w", err)
	}
	return buf.Bytes(), nil
}

type gen struct {
	b *types.Builtins
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
}

// binPrec mirrors Go's precedence for the operators fango emits, so we can
// parenthesize only where Go's grammar needs it.
func goPrec(op string) int {
	switch op {
	case "*", "/":
		return 5
	case "+", "-":
		return 4
	default:
		return 0
	}
}

// expr emits e; parentPrec is the precedence of the enclosing operator
// context (0 = none) for minimal parenthesization.
func (g *gen) expr(e core.Expr, parentPrec int) goast.Expr {
	switch e := e.(type) {
	case *core.IntLit:
		return intLit(e.Val)
	case *core.VarRef:
		return ident(mangleValue(e.Name))
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
	default:
		panic(fmt.Sprintf("codegen: node %T arrives in a later slice", e))
	}
}

// mangleValue maps a fango value name into the generated package's `v_`
// namespace (constructors use C_, types T_).
func mangleValue(name string) string { return "v_" + name }
