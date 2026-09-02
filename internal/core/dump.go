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
	for _, d := range p.Defs {
		fmt.Fprintf(&b, "\n  (def %s %s %s)", d.Name, types.Show(d.Type), DumpExpr(d.Body))
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
	default:
		panic(fmt.Sprintf("core.DumpExpr: unhandled %T", e))
	}
}
