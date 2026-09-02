package ast

import (
	"fmt"
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
		return fmt.Sprintf("(def %s %s)", d.Name, DumpExpr(d.Body))
	default:
		panic(fmt.Sprintf("ast.dumpDecl: unhandled %T", d))
	}
}

func DumpExpr(e Expr) string {
	switch e := e.(type) {
	case *IntLit:
		return fmt.Sprintf("(int %d)", e.Value)
	case *Var:
		return fmt.Sprintf("(var %s)", e.Name)
	case *BinOp:
		return fmt.Sprintf("(binop %s %s %s)", e.Op, DumpExpr(e.L), DumpExpr(e.R))
	default:
		panic(fmt.Sprintf("ast.DumpExpr: unhandled %T", e))
	}
}
