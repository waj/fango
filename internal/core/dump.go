package core

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/types"
)

// Dump renders Core as S-expressions with explicit types — the golden
// format for testdata/core. Frozen: it is what makes defaulting (and later
// lifting and decision trees) visible in review.
func Dump(p *Prog) string {
	var b strings.Builder
	b.WriteString("(core")
	for _, adt := range p.ADTs {
		fmt.Fprintf(&b, "\n  (type %s", adt.Con.Name)
		for _, c := range adt.Ctors {
			fmt.Fprintf(&b, " (ctor %s", c.Name)
			for _, f := range c.Fields {
				fmt.Fprintf(&b, " %s", types.Show(f))
			}
			b.WriteString(")")
		}
		b.WriteString(")")
	}
	for _, d := range p.Defs {
		if len(d.Params) > 0 {
			fmt.Fprintf(&b, "\n  (def %s (params %s) %s %s)",
				d.Name, strings.Join(d.Params, " "), types.Show(d.Type), DumpExpr(d.Body))
		} else {
			fmt.Fprintf(&b, "\n  (def %s %s %s)", d.Name, types.Show(d.Type), DumpExpr(d.Body))
		}
	}
	b.WriteString(")\n")
	return b.String()
}

func DumpExpr(e Expr) string {
	switch e := e.(type) {
	case *IntLit:
		return fmt.Sprintf("(int %d %s)", e.Val, types.Show(e.Ty))
	case *FloatLit:
		return fmt.Sprintf("(float %s %s)", strconv.FormatFloat(e.Val, 'g', -1, 64), types.Show(e.Ty))
	case *StringLit:
		return fmt.Sprintf("(string %q %s)", e.Val, types.Show(e.Ty))
	case *BoolLit:
		return fmt.Sprintf("(bool %t %s)", e.Val, types.Show(e.Ty))
	case *Neg:
		return fmt.Sprintf("(neg %s %s)", types.Show(e.Ty), DumpExpr(e.Operand))
	case *If:
		return fmt.Sprintf("(if %s %s %s %s)", types.Show(e.Ty), DumpExpr(e.Cond), DumpExpr(e.Then), DumpExpr(e.Else))
	case *Print:
		return fmt.Sprintf("(print %s)", DumpExpr(e.Arg))
	case *Let:
		form := "let"
		if e.Rec {
			form = "letrec"
		}
		return fmt.Sprintf("(%s %s %s %s %s)", form, e.Name, types.Show(e.Ty), DumpExpr(e.Rhs), DumpExpr(e.Body))
	case *Lambda:
		return fmt.Sprintf("(lam %s %s %s)", e.Param, types.Show(e.Ty), DumpExpr(e.Body))
	case *VarRef:
		if len(e.TyArgs) > 0 {
			args := make([]string, len(e.TyArgs))
			for i, t := range e.TyArgs {
				args[i] = types.Show(t)
			}
			return fmt.Sprintf("(var %s @[%s] %s)", e.Name, strings.Join(args, " "), types.Show(e.Ty))
		}
		return fmt.Sprintf("(var %s %s)", e.Name, types.Show(e.Ty))
	case *BinOp:
		return fmt.Sprintf("(binop %s %s %s %s)", e.Op, types.Show(e.Ty), DumpExpr(e.L), DumpExpr(e.R))
	case *App:
		kinds := map[CalleeKind]string{Worker: "worker", Ctor: "ctor", Value: "value"}
		parts := []string{fmt.Sprintf("(app/%s %s", kinds[e.CalleeKind], DumpExpr(e.Callee))}
		for _, a := range e.Args {
			parts = append(parts, DumpExpr(a))
		}
		return strings.Join(parts, " ") + fmt.Sprintf(" %s)", types.Show(e.Ty))
	case *Case:
		return fmt.Sprintf("(case %s %s %s %s)",
			types.Show(e.Ty), DumpExpr(e.Scrut), e.Bind, DumpTree(e.Tree))
	default:
		panic(fmt.Sprintf("core.DumpExpr: unhandled %T", e))
	}
}

// DumpTree renders a decision tree in the golden S-expression format.
func DumpTree(t Tree) string {
	switch t := t.(type) {
	case *Leaf:
		return fmt.Sprintf("(leaf %s)", DumpExpr(t.Body))
	case *SwitchCtor:
		var b strings.Builder
		fmt.Fprintf(&b, "(switchctor %s", t.Scrut)
		for _, c := range t.Cases {
			fmt.Fprintf(&b, " (%s", c.Ctor.Name)
			for _, bind := range c.Binds {
				if bind == "" {
					bind = "_"
				}
				fmt.Fprintf(&b, " %s", bind)
			}
			fmt.Fprintf(&b, " %s)", DumpTree(c.Tree))
		}
		if t.Default != nil {
			fmt.Fprintf(&b, " (default %s)", DumpTree(t.Default))
		}
		b.WriteString(")")
		return b.String()
	case *SwitchLit:
		var b strings.Builder
		fmt.Fprintf(&b, "(switchlit %s", t.Scrut)
		for _, c := range t.Cases {
			fmt.Fprintf(&b, " (%s %s)", DumpExpr(c.Lit), DumpTree(c.Tree))
		}
		fmt.Fprintf(&b, " (default %s))", DumpTree(t.Default))
		return b.String()
	default:
		panic(fmt.Sprintf("core.DumpTree: unhandled %T", t))
	}
}
