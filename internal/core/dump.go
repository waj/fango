package core

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/types"
)

// Dump renders Core as S-expressions with explicit types — the golden
// format for testdata/core. Frozen: it is what makes defaulting, lifting,
// instantiation plumbing, and decision trees visible in review. Each type
// declaration and each definition dumps through one shared printer, so its
// rigid type variables get consistent positional names (a, b, …) across
// every type in that definition.
func Dump(p *Prog) string {
	var b strings.Builder
	b.WriteString("(core")
	for _, adt := range p.ADTs {
		pr := types.NewPrinter()
		fmt.Fprintf(&b, "\n  (type %s", adt.Con.Name)
		if len(adt.Params) > 0 {
			names := make([]string, len(adt.Params))
			for i, v := range adt.Params {
				names[i] = pr.Type(v)
			}
			fmt.Fprintf(&b, " (params %s)", strings.Join(names, " "))
		}
		if adt.IsRecord() {
			b.WriteString(" (record")
			for _, f := range adt.RecordFields {
				fmt.Fprintf(&b, " (field %s %s)", f.Name, pr.Atom(f.Type))
			}
			b.WriteString(")")
			b.WriteString(")")
			continue
		}
		for _, c := range adt.Ctors {
			fmt.Fprintf(&b, " (ctor %s", c.Name)
			for _, f := range c.Fields {
				fmt.Fprintf(&b, " %s", pr.Atom(f))
			}
			b.WriteString(")")
		}
		b.WriteString(")")
	}
	for _, eff := range p.Effects {
		if types.SurfaceName(eff.Name) == "IO" {
			continue
		}
		pr := types.NewPrinter()
		fmt.Fprintf(&b, "\n  (effect %s", eff.Name)
		if len(eff.Params) > 0 {
			params := make([]string, len(eff.Params))
			for i, p := range eff.Params {
				params[i] = pr.Type(p)
			}
			fmt.Fprintf(&b, " (params %s)", strings.Join(params, " "))
		}
		for _, op := range eff.Ops {
			kind := "op"
			if op.Abort {
				kind = "abort-op"
			}
			fmt.Fprintf(&b, " (%s %s %s)", kind, op.Name, pr.Type(op.Scheme.Body))
		}
		b.WriteString(")")
	}
	for i := range p.Defs {
		d := &p.Defs[i]
		pr := types.NewPrinter()
		fmt.Fprintf(&b, "\n  (def %s", d.Name)
		if len(d.TyParams) > 0 {
			names := make([]string, len(d.TyParams))
			for i, v := range d.TyParams {
				names[i] = pr.Type(v)
			}
			fmt.Fprintf(&b, " (typarams %s)", strings.Join(names, " "))
		}
		if len(d.Params) > 0 {
			fmt.Fprintf(&b, " (params %s)", strings.Join(d.Params, " "))
		}
		if len(d.EffectParams) > 0 {
			b.WriteString(" (effects")
			for _, e := range d.EffectParams {
				fmt.Fprintf(&b, " %s", dumpEffect(e, pr))
			}
			b.WriteString(")")
		}
		if d.Control != (types.Control{}) {
			fmt.Fprintf(&b, " (control %s)", ControlName(d.Control))
		}
		fmt.Fprintf(&b, " %s %s)", pr.Type(d.Type), dumpExpr(d.Body, pr))
	}
	b.WriteString(")\n")
	return b.String()
}

// DumpExpr renders one expression with a fresh printer — single-expression
// contexts (tests, literal dedup keys).
func DumpExpr(e Expr) string { return dumpExpr(e, types.NewPrinter()) }

// DumpTree renders a decision tree with a fresh printer.
func DumpTree(t Tree) string { return dumpTree(t, types.NewPrinter()) }

func dumpExpr(e Expr, pr *types.Printer) string {
	switch e := e.(type) {
	case *AttributeLookup:
		return fmt.Sprintf("(attributes %s %s : %s)", pr.Type(e.Requested), dumpExpr(e.Bag, pr), pr.Type(e.Ty))
	case *ParallelMap:
		return fmt.Sprintf("(parallel-map %s %s : %s)", dumpExpr(e.Function, pr), dumpExpr(e.Input, pr), pr.Type(e.Ty))
	case *AsyncLaunch:
		return fmt.Sprintf("(async-launch %s : %s)", dumpExpr(e.Call, pr), pr.Type(e.Ty))
	case *AsyncRebase:
		return fmt.Sprintf("(async-rebase %s)", dumpExpr(e.Call, pr))
	case *AsyncSupervise:
		return fmt.Sprintf("(async-supervise %s)", dumpExpr(e.Call, pr))
	case *IntLit:
		return fmt.Sprintf("(int %d %s)", e.Val, pr.Type(e.Ty))
	case *FloatLit:
		return fmt.Sprintf("(float %s %s)", strconv.FormatFloat(e.Val, 'g', -1, 64), pr.Type(e.Ty))
	case *StringLit:
		return fmt.Sprintf("(string %q %s)", e.Val, pr.Type(e.Ty))
	case *CharLit:
		return fmt.Sprintf("(char %q %s)", e.Val, pr.Type(e.Ty))
	case *UnitLit:
		return "(unit ())"
	case *BoolLit:
		return fmt.Sprintf("(bool %t %s)", e.Val, pr.Type(e.Ty))
	case *Neg:
		return fmt.Sprintf("(neg %s %s)", pr.Type(e.Ty), dumpExpr(e.Operand, pr))
	case *If:
		return fmt.Sprintf("(if %s %s %s %s)", pr.Type(e.Ty), dumpExpr(e.Cond, pr), dumpExpr(e.Then, pr), dumpExpr(e.Else, pr))
	case *Perform:
		form := "perform"
		if e.Control != (types.Control{}) {
			form += "/" + ControlName(e.Control)
		}
		parts := []string{fmt.Sprintf("(%s %s/%s", form, dumpEffect(e.Effect, pr), e.Op.Name)}
		for _, a := range e.Args {
			parts = append(parts, dumpExpr(a, pr))
		}
		return strings.Join(parts, " ") + " " + pr.Type(e.Ty) + ")"
	case *ControlExit:
		parts := []string{fmt.Sprintf("(control-exit %s/%s", dumpEffect(e.Effect, pr), e.Op.Name)}
		for _, p := range e.Payload {
			parts = append(parts, dumpExpr(p, pr))
		}
		return strings.Join(parts, " ") + " " + pr.Type(e.Ty) + ")"

	case *FailureInspect:
		parts := []string{"(failure-inspect", e.Name, pr.Type(e.Ty)}
		for _, arg := range e.Args {
			parts = append(parts, dumpExpr(arg, pr))
		}
		return strings.Join(parts, " ") + ")"

	case *ResumeTail:
		if e.NextState != nil {
			return fmt.Sprintf("(resume-tail %d %s %s (next-state %s))", e.Owner, pr.Type(e.ClauseResult), dumpExpr(e.Value, pr), dumpExpr(e.NextState, pr))
		}
		return fmt.Sprintf("(resume-tail %d %s %s)", e.Owner, pr.Type(e.ClauseResult), dumpExpr(e.Value, pr))
	case *Seq:
		return fmt.Sprintf("(seq %s %s %s)", pr.Type(e.Ty), dumpExpr(e.First, pr), dumpExpr(e.Then, pr))
	case *Handle:
		var b strings.Builder
		fmt.Fprintf(&b, "(handle %s %s", dumpEffect(e.Effect, pr), dumpExpr(e.Body, pr))
		if e.State != nil {
			fmt.Fprintf(&b, " (state %s %s %s)", e.State.Name, pr.Type(e.State.Ty), dumpExpr(e.State.Initial, pr))
		}
		for _, c := range e.Clauses {
			if c.SuppressedParam != "" {
				fmt.Fprintf(&b, " (suppressed %s %s)", c.SuppressedParam, pr.Type(c.SuppressedType))
			}
			fmt.Fprintf(&b, " (%s (%s) %s)", c.Op.Name, strings.Join(c.Params, " "), dumpExpr(c.Body, pr))
		}
		if e.Return != nil {
			fmt.Fprintf(&b, " (return %s %s)", e.Return.Param, dumpExpr(e.Return.Body, pr))
		}
		fmt.Fprintf(&b, " %s)", pr.Type(e.Ty))
		return b.String()
	case *Bracket:
		form := "bracket"
		if e.Control != (types.Control{}) {
			form += "/" + ControlName(e.Control)
		}
		return fmt.Sprintf("(%s %d %s %s %s %s %s %s)", form, e.Scope, e.Resource, pr.Type(e.ResourceTy),
			dumpExpr(e.Acquire, pr), dumpExpr(e.Release, pr), dumpExpr(e.Body, pr), pr.Type(e.Ty))
	case *Let:
		form := "let"
		if e.Rec {
			form = "letrec"
		}
		return fmt.Sprintf("(%s %s %s %s %s)", form, e.Name, pr.Type(e.Ty), dumpExpr(e.Rhs, pr), dumpExpr(e.Body, pr))
	case *Lambda:
		control := types.FunctionControl(e.Ty.(*types.TFun))
		form := "lam"
		if control != (types.Control{}) {
			form += "/" + ControlName(control)
		}
		return fmt.Sprintf("(%s %s %s %s)", form, e.Param, pr.Type(e.Ty), dumpExpr(e.Body, pr))
	case *VarRef:
		if len(e.TyArgs) > 0 {
			return fmt.Sprintf("(var %s @[%s] %s)", e.Name, dumpTypes(e.TyArgs, pr), pr.Type(e.Ty))
		}
		return fmt.Sprintf("(var %s %s)", e.Name, pr.Type(e.Ty))
	case *Quote:
		parts := []string{fmt.Sprintf("(quote %d", e.Template)}
		for _, h := range e.Holes {
			parts = append(parts, dumpExpr(h, pr))
		}
		return strings.Join(parts, " ") + " " + pr.Type(e.Ty) + ")"
	case *NativeCall:
		parts := []string{"(native " + e.Name}
		for _, a := range e.Args {
			parts = append(parts, dumpExpr(a, pr))
		}
		return strings.Join(parts, " ") + " " + pr.Type(e.Ty) + ")"
	case *App:
		kinds := map[CalleeKind]string{Worker: "worker", Ctor: "ctor", Value: "value"}
		head := "(app/" + kinds[e.CalleeKind]
		if e.Control != (types.Control{}) {
			head += "/" + ControlName(e.Control)
		}
		if len(e.TyArgs) > 0 {
			head += fmt.Sprintf(" @[%s]", dumpTypes(e.TyArgs, pr))
		}
		if len(e.EvidenceArgs) > 0 {
			head += " evidence["
			for i, v := range e.EvidenceArgs {
				if i > 0 {
					head += " "
				}
				head += dumpEffect(v, pr)
			}
			head += "]"
		}
		parts := []string{head + " " + dumpExpr(e.Callee, pr)}
		for _, a := range e.Args {
			parts = append(parts, dumpExpr(a, pr))
		}
		return strings.Join(parts, " ") + fmt.Sprintf(" %s)", pr.Type(e.Ty))
	case *Case:
		return fmt.Sprintf("(case %s %s %s %s)",
			pr.Type(e.Ty), dumpExpr(e.Scrut, pr), e.Bind, dumpTree(e.Tree, pr))
	default:
		panic(fmt.Sprintf("core.DumpExpr: unhandled %T", e))
	}
}

func dumpEffect(e EffectInstance, pr *types.Printer) string {
	name := e.Name
	if e.Control != (types.Control{}) {
		name += "@" + ControlName(e.Control)
	}
	if len(e.Args) == 0 {
		return name
	}
	return name + "[" + dumpTypes(e.Args, pr) + "]"
}

func dumpTypes(ts []types.Type, pr *types.Printer) string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = pr.Type(t)
	}
	return strings.Join(out, " ")
}

func dumpTree(t Tree, pr *types.Printer) string {
	switch t := t.(type) {
	case *Unreachable:
		return "(unreachable)"
	case *Guard:
		return fmt.Sprintf("(guard %s %s %s)", dumpExpr(t.Cond, pr), dumpTree(t.Then, pr), dumpTree(t.Else, pr))
	case *Leaf:
		return fmt.Sprintf("(leaf %s)", dumpExpr(t.Body, pr))
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
			fmt.Fprintf(&b, " %s)", dumpTree(c.Tree, pr))
		}
		if t.Default != nil {
			fmt.Fprintf(&b, " (default %s)", dumpTree(t.Default, pr))
		}
		b.WriteString(")")
		return b.String()
	case *SwitchLit:
		var b strings.Builder
		fmt.Fprintf(&b, "(switchlit %s", t.Scrut)
		for _, c := range t.Cases {
			fmt.Fprintf(&b, " (%s %s)", dumpExpr(c.Lit, pr), dumpTree(c.Tree, pr))
		}
		fmt.Fprintf(&b, " (default %s))", dumpTree(t.Default, pr))
		return b.String()
	default:
		panic(fmt.Sprintf("core.DumpTree: unhandled %T", t))
	}
}
