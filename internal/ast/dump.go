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
		var b strings.Builder
		fmt.Fprintf(&b, "(def %s", d.Name)
		if p := dumpParams(d.Params); p != "" {
			fmt.Fprintf(&b, " %s", p)
		}
		if d.Ann != nil {
			fmt.Fprintf(&b, " (ann %s)", DumpTypeExpr(d.Ann.Type))
		}
		fmt.Fprintf(&b, " %s)", DumpExpr(d.Body))
		return b.String()
	default:
		panic(fmt.Sprintf("ast.dumpDecl: unhandled %T", d))
	}
}

// dumpParams renders "(params x y)" or "" — the clause appears only when
// non-empty, so every pre-S3 golden stays byte-identical.
func dumpParams(ps []Param) string {
	if len(ps) == 0 {
		return ""
	}
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	return "(params " + strings.Join(names, " ") + ")"
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
			fmt.Fprintf(&b, " (bind %s", bind.Name)
			if p := dumpParams(bind.Params); p != "" {
				fmt.Fprintf(&b, " %s", p)
			}
			if bind.Ann != nil {
				fmt.Fprintf(&b, " (ann %s)", DumpTypeExpr(bind.Ann.Type))
			}
			fmt.Fprintf(&b, " %s)", DumpExpr(bind.Body))
		}
		fmt.Fprintf(&b, " %s)", DumpExpr(e.Result))
		return b.String()
	case *Lambda:
		names := make([]string, len(e.Params))
		for i, p := range e.Params {
			names[i] = p.Name
		}
		return fmt.Sprintf("(lambda (%s) %s)", strings.Join(names, " "), DumpExpr(e.Body))
	default:
		panic(fmt.Sprintf("ast.DumpExpr: unhandled %T", e))
	}
}
