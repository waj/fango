package ast

import (
	"fmt"
	"strconv"
	"strings"
)

// Dump renders the module as indented S-expressions — the golden-test
// format. Expressions print on one line; declarations get a line each.
// This format is frozen: changing it churns every parse golden.
func Dump(m *Module) string {
	var b strings.Builder
	b.WriteString("(module")
	if m.Header != nil {
		fmt.Fprintf(&b, " %s", m.Header.Name)
	}
	for _, d := range m.Decls {
		b.WriteString("\n  ")
		b.WriteString(dumpDecl(d))
	}
	b.WriteString(")\n")
	return b.String()
}

func dumpDecl(d Decl) string {
	switch d := d.(type) {
	case *ValueDecl:
		if d.Ann != nil {
			return fmt.Sprintf("(def %s (ann %s) %s)", d.Name, DumpTypeExpr(d.Ann.Type), DumpExpr(d.Body))
		}
		return fmt.Sprintf("(def %s %s)", d.Name, DumpExpr(d.Body))
	default:
		panic(fmt.Sprintf("ast.dumpDecl: unhandled %T", d))
	}
}

func DumpTypeExpr(t TypeExpr) string {
	switch t := t.(type) {
	case *TName:
		return t.Name
	case *TVarName:
		return t.Name
	case *TFunExpr:
		return fmt.Sprintf("(-> %s %s)", DumpTypeExpr(t.Arg), DumpTypeExpr(t.Ret))
	default:
		panic(fmt.Sprintf("ast.DumpTypeExpr: unhandled %T", t))
	}
}

func DumpExpr(e Expr) string {
	switch e := e.(type) {
	case *IntLit:
		return fmt.Sprintf("(int %d)", e.Value)
	case *FloatLit:
		return fmt.Sprintf("(float %s)", strconv.FormatFloat(e.Value, 'g', -1, 64))
	case *StringLit:
		return fmt.Sprintf("(string %q)", e.Value)
	case *Var:
		return fmt.Sprintf("(var %s)", e.Name)
	case *Ctor:
		return fmt.Sprintf("(ctor %s)", e.Name)
	case *App:
		return fmt.Sprintf("(app %s %s)", DumpExpr(e.Fn), DumpExpr(e.Arg))
	case *Neg:
		return fmt.Sprintf("(neg %s)", DumpExpr(e.Operand))
	case *BinOp:
		return fmt.Sprintf("(binop %s %s %s)", e.Op, DumpExpr(e.L), DumpExpr(e.R))
	case *If:
		return fmt.Sprintf("(if %s %s %s)", DumpExpr(e.Cond), DumpExpr(e.Then), DumpExpr(e.Else))
	case *Block:
		var b strings.Builder
		b.WriteString("(block")
		for _, bind := range e.Binds {
			if bind.Ann != nil {
				fmt.Fprintf(&b, " (bind %s (ann %s) %s)", bind.Name, DumpTypeExpr(bind.Ann.Type), DumpExpr(bind.Body))
			} else {
				fmt.Fprintf(&b, " (bind %s %s)", bind.Name, DumpExpr(bind.Body))
			}
		}
		fmt.Fprintf(&b, " %s)", DumpExpr(e.Result))
		return b.String()
	default:
		panic(fmt.Sprintf("ast.DumpExpr: unhandled %T", e))
	}
}
