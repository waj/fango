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
		b.WriteString(" (exposing")
		if m.Header.Exposing.All {
			b.WriteString(" ..")
		}
		for _, item := range m.Header.Exposing.Items {
			fmt.Fprintf(&b, " %s", item.Name)
			if item.All {
				b.WriteString("(..)")
			}
		}
		b.WriteString(")")
	}
	for _, im := range m.Imports {
		fmt.Fprintf(&b, "\n  (import %s", im.Module)
		if im.Alias != "" {
			fmt.Fprintf(&b, " (as %s)", im.Alias)
		}
		if im.Exposing != nil {
			b.WriteString(" (exposing")
			if im.Exposing.All {
				b.WriteString(" ..")
			}
			for _, item := range im.Exposing.Items {
				fmt.Fprintf(&b, " %s", item.Name)
				if item.All {
					b.WriteString("(..)")
				}
			}
			b.WriteString(")")
		}
		b.WriteString(")")
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
	case *ClassDecl:
		parts := []string{"(class", d.Name, d.Param.Name}
		for _, m := range d.Methods {
			parts = append(parts, "("+m.Name+" "+DumpTypeExpr(m.Type)+")")
		}
		return strings.Join(parts, " ") + ")"
	case *InstanceDecl:
		parts := []string{"(instance", dumpPreds(d.Preds), d.Head.Class, DumpTypeExpr(d.Head.Ty)}
		for _, m := range d.Methods {
			parts = append(parts, dumpDecl(m))
		}
		return strings.Join(parts, " ") + ")"
	case *ValueDecl:
		var b strings.Builder
		fmt.Fprintf(&b, "(def %s", d.Name)
		if p := dumpParams(d.Params); p != "" {
			fmt.Fprintf(&b, " %s", p)
		}
		if d.Ann != nil {
			fmt.Fprintf(&b, " (ann %s%s)", dumpPreds(d.Ann.Preds), DumpTypeExpr(d.Ann.Type))
		}
		if d.Native != nil {
			b.WriteString(" (native")
			if d.Native.Template != nil {
				fmt.Fprintf(&b, " %q", *d.Native.Template)
			}
			b.WriteString(")")
		} else {
			fmt.Fprintf(&b, " %s", DumpExpr(d.Body))
		}
		b.WriteString(")")
		return b.String()
	case *TypeDecl:
		var b strings.Builder
		fmt.Fprintf(&b, "(type %s", d.Name)
		if p := dumpParams(d.Params); p != "" {
			fmt.Fprintf(&b, " %s", p)
		}
		if len(d.Deriving) > 0 {
			b.WriteString(" (deriving")
			for _, c := range d.Deriving {
				fmt.Fprintf(&b, " %s", c.Name)
			}
			b.WriteString(")")
		}
		for _, c := range d.Ctors {
			fmt.Fprintf(&b, " (ctor %s", c.Name)
			for _, a := range c.Args {
				fmt.Fprintf(&b, " %s", DumpTypeExpr(a))
			}
			b.WriteString(")")
		}
		b.WriteString(")")
		return b.String()
	case *EffectDecl:
		var b strings.Builder
		fmt.Fprintf(&b, "(effect %s", d.Name)
		if p := dumpParams(d.Params); p != "" {
			fmt.Fprintf(&b, " %s", p)
		}
		for _, op := range d.Ops {
			fmt.Fprintf(&b, " (op %s %s", op.Name, DumpTypeExpr(op.Type))
			if op.Native != nil {
				b.WriteString(" (native")
				if op.Native.Template != nil {
					fmt.Fprintf(&b, " %q", *op.Native.Template)
				}
				b.WriteString(")")
			}
			b.WriteString(")")
		}
		b.WriteString(")")
		return b.String()
	case *InfixDecl:
		return fmt.Sprintf("(infix %s %s)", d.Op, d.Target)
	default:
		panic(fmt.Sprintf("ast.dumpDecl: unhandled %T", d))
	}
}

// dumpParams renders "(params x y)" or "" — the clause appears only when
// non-empty, keeping nullary declarations' golden form compact.
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
		if t.Eff != nil {
			var row strings.Builder
			row.WriteString("(effects")
			for _, label := range t.Eff.Labels {
				fmt.Fprintf(&row, " (%s", label.Name)
				for _, arg := range label.Args {
					fmt.Fprintf(&row, " %s", DumpTypeExpr(arg))
				}
				row.WriteString(")")
			}
			if t.Eff.Tail != "" {
				fmt.Fprintf(&row, " (tail %s)", t.Eff.Tail)
			}
			row.WriteString(")")
			return fmt.Sprintf("(-> %s %s %s)", DumpTypeExpr(t.Arg), row.String(), DumpTypeExpr(t.Ret))
		}
		return fmt.Sprintf("(-> %s %s)", DumpTypeExpr(t.Arg), DumpTypeExpr(t.Ret))
	case *TApp:
		var b strings.Builder
		fmt.Fprintf(&b, "(%s", t.Name)
		for _, a := range t.Args {
			fmt.Fprintf(&b, " %s", DumpTypeExpr(a))
		}
		b.WriteString(")")
		return b.String()
	default:
		panic(fmt.Sprintf("ast.DumpTypeExpr: unhandled %T", t))
	}
}

// DumpPattern renders a pattern: `_`, `(pvar x)`, `(pint 3)`, and
// `(pctor Just (pvar x))`.
func DumpPattern(p Pattern) string {
	switch p := p.(type) {
	case *PWildcard:
		return "_"
	case *PVar:
		return fmt.Sprintf("(pvar %s)", p.Name)
	case *PInt:
		return fmt.Sprintf("(pint %d)", p.Value)
	case *PFloat:
		return fmt.Sprintf("(pfloat %s)", strconv.FormatFloat(p.Value, 'g', -1, 64))
	case *PString:
		return fmt.Sprintf("(pstring %q)", p.Value)
	case *PCtor:
		var b strings.Builder
		fmt.Fprintf(&b, "(pctor %s", p.Name)
		for _, a := range p.Args {
			fmt.Fprintf(&b, " %s", DumpPattern(a))
		}
		b.WriteString(")")
		return b.String()
	default:
		panic(fmt.Sprintf("ast.DumpPattern: unhandled %T", p))
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
	case *UnitLit:
		return "(unit)"
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
		if len(e.Items) > 0 {
			for _, item := range e.Items {
				if item.Expr != nil {
					fmt.Fprintf(&b, " (expr %s)", DumpExpr(item.Expr))
					continue
				}
				bind := e.Binds[item.BindIndex]
				fmt.Fprintf(&b, " (bind %s %s)", bind.Name, DumpExpr(bind.Body))
			}
		} else {
			for _, bind := range e.Binds {
				fmt.Fprintf(&b, " (bind %s", bind.Name)
				if p := dumpParams(bind.Params); p != "" {
					fmt.Fprintf(&b, " %s", p)
				}
				if bind.Ann != nil {
					fmt.Fprintf(&b, " (ann %s%s)", dumpPreds(bind.Ann.Preds), DumpTypeExpr(bind.Ann.Type))
				}
				fmt.Fprintf(&b, " %s)", DumpExpr(bind.Body))
			}
		}
		fmt.Fprintf(&b, " %s)", DumpExpr(e.Result))
		return b.String()
	case *Case:
		var b strings.Builder
		fmt.Fprintf(&b, "(case %s", DumpExpr(e.Scrutinee))
		for _, br := range e.Branches {
			fmt.Fprintf(&b, " (branch %s %s)", DumpPattern(br.Pattern), DumpExpr(br.Body))
		}
		b.WriteString(")")
		return b.String()
	case *Lambda:
		names := make([]string, len(e.Params))
		for i, p := range e.Params {
			names[i] = p.Name
		}
		return fmt.Sprintf("(lambda (%s) %s)", strings.Join(names, " "), DumpExpr(e.Body))
	case *Handle:
		var b strings.Builder
		fmt.Fprintf(&b, "(handle %s", DumpExpr(e.Body))
		for _, clause := range e.Clauses {
			fmt.Fprintf(&b, " (clause %s", clause.Op)
			if p := dumpParams(clause.Params); p != "" {
				fmt.Fprintf(&b, " %s", p)
			}
			fmt.Fprintf(&b, " %s)", DumpExpr(clause.Body))
		}
		if e.Return != nil {
			fmt.Fprintf(&b, " (return %s %s)", e.Return.Param.Name, DumpExpr(e.Return.Body))
		}
		b.WriteString(")")
		return b.String()
	case *Resume:
		return "(resume)"
	default:
		panic(fmt.Sprintf("ast.DumpExpr: unhandled %T", e))
	}
}
