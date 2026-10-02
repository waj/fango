package llvmgen

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func (g *generator) expr(e core.Expr) string {
	switch e := e.(type) {
	case *core.IntLit:
		return g.temp(e.Ty, fmt.Sprintf("std::bit_cast<int64_t>(UINT64_C(%d))", uint64(e.Val)))
	case *core.FloatLit:
		return g.temp(e.Ty, fmt.Sprintf("std::bit_cast<double>(UINT64_C(%d))", math.Float64bits(e.Val)))
	case *core.StringLit:
		return g.temp(e.Ty, stringValue(e.Val))
	case *core.CharLit:
		return g.temp(e.Ty, fmt.Sprintf("UINT32_C(%d)", e.Val))
	case *core.BoolLit:
		return g.temp(e.Ty, strconv.FormatBool(e.Val))
	case *core.UnitLit:
		return "fg_unit{}"
	case *core.Neg:
		v := g.expr(e.Operand)
		if g.typ(e.Ty) == "double" {
			return g.temp(e.Ty, "-"+v)
		}
		return g.temp(e.Ty, "fg_sub(int64_t(0),"+v+")")
	case *core.VarRef:
		if v := g.locals[e.Name]; v != "" {
			return v
		}
		if e.Local {
			if v := g.locals[e.Name]; v != "" {
				return v
			}
			panic("missing local " + e.Name)
		}
		d := g.defs[e.Name]
		if d == nil {
			panic("missing definition " + e.Name)
		}
		return g.temp(e.Ty, typeApply(symbol(e.Name), g.typeArgs(e.TyArgs))+"()")
	case *core.Let:
		if e.Rec {
			// The recursive cell is shared by the closure's captured environment.
			cell := g.fresh()
			g.line("auto *%s=fg_new(%s{});", cell, g.typ(e.Rhs.Type()))
			g.bind(e.Name, e.Rhs.Type(), "(*"+cell+")")
			v := g.expr(e.Rhs)
			g.line("*%s=%s;", cell, v)
		} else {
			v := g.expr(e.Rhs)
			g.bind(e.Name, e.Rhs.Type(), v)
		}
		return g.expr(e.Body)
	case *core.Seq:
		g.expr(e.First)
		return g.expr(e.Then)
	case *core.If:
		cond := g.expr(e.Cond)
		result := g.temp(e.Ty, "{}")
		g.line("if(%s){", cond)
		yes := g.expr(e.Then)
		g.line("%s=%s;\n}else{", result, yes)
		no := g.expr(e.Else)
		g.line("%s=%s;\n}", result, no)
		return result
	case *core.Case:
		value := g.expr(e.Scrut)
		g.bind(e.Bind, e.Scrut.Type(), value)
		result := g.temp(e.Ty, "{}")
		g.tree(e.Tree, func(e core.Expr) { v := g.expr(e); g.line("%s=%s;", result, v) })
		return result
	case *core.App:
		if e.CalleeKind == core.Ctor {
			var args []string
			for _, a := range e.Args {
				args = append(args, g.expr(a))
			}
			return g.temp(e.Ty, g.ctor(e.Ctor, e.Ty, args))
		}
		var callee string
		if e.CalleeKind == core.Value {
			callee = g.expr(e.Callee)
		}
		var values []string
		for _, a := range e.Args {
			values = append(values, g.expr(a))
		}
		var args []string
		for _, ev := range e.EvidenceArgs {
			args = append(args, g.ev(ev))
		}
		if e.Row != nil {
			args = append(args, g.row(e.Row))
		}
		args = append(args, values...)
		mode := e.Control.Resolve(g.mode)
		var call string
		if e.CalleeKind == core.Worker {
			ref := e.Callee.(*core.VarRef)
			name := symbol(ref.Name)
			if mode == types.Exit {
				name += "_x"
			}
			call = typeApply(name, g.typeArgs(e.TyArgs)) + "(" + strings.Join(args, ",") + ")"
		} else {
			member := "call"
			if mode == types.Exit {
				member = "call_exit"
			}
			call = callee + "." + member + "(" + strings.Join(args, ",") + ")"
		}
		if mode == types.Exit {
			return g.out(e.Ty, call)
		}
		return g.temp(e.Ty, call)
	case *core.Lambda:
		return g.lambda(e)
	case *core.NativeCall:
		return g.native(e)
	case *core.Perform:
		return g.perform(e)
	case *core.Handle:
		return g.handle(e)
	case *core.ControlExit:
		var args []string
		for _, a := range e.Payload {
			args = append(args, "fg_box_typed("+g.expr(a)+","+g.descriptor(a.Type())+")")
		}
		payload := g.fresh()
		g.line("auto *%s=static_cast<fg_any *>(fango_alloc(sizeof(fg_any)*%d));", payload, len(args))
		for i, a := range args {
			g.line("%s[%d]=%s;", payload, i, a)
		}
		g.line("%s=fg_new(fg_exit{%s,%s,%s,%d,%d,%s,{}});goto %s;", g.pending, g.ev(e.Effect), stringValue(e.Effect.Name), stringValue(e.Op.Name), e.Op.Index, len(args), payload, g.exitLabel)
		return g.typ(e.Ty) + "{}"
	case *core.ResumeTail:
		value := g.expr(e.Value)
		if e.NextState != nil && !g.keepsState(e.NextState) {
			next := g.expr(e.NextState)
			g.line("*%s=%s;", g.state, next)
		}
		return value
	case *core.Bracket:
		return g.bracket(e)
	case *core.FailureInspect:
		return g.failure(e)
	default:
		panic(fmt.Sprintf("unsupported Core node %T", e))
	}
}
func (g *generator) lambda(e *core.Lambda) string {
	child := g.clone()
	child.mode = types.Exit
	child.pending = "fg_pending"
	child.exitLabel = "fg_exit_label"
	var params []string
	var evidenceNames []string
	for _, ev := range e.EffectParams {
		name := child.fresh()
		evidenceNames = append(evidenceNames, name)
		params = append(params, fmt.Sprintf("%s *%s", g.evType(ev), name))
	}
	rowName := child.fresh()
	if e.RowParam != 0 {
		params = append(params, "fg_row "+rowName)
	}
	fn := e.Ty.(*types.TFun)
	argument := child.fresh()
	params = append(params, g.typ(fn.Arg)+" "+argument)
	child.bind(e.Param, fn.Arg, argument)
	child.line("[=](%s)->fg_out<%s>{", strings.Join(params, ","), g.typ(fn.Ret))
	child.line("fg_exit *fg_pending=nullptr;\n{")
	child.invocationNamed(e.EffectParams, e.RowEffects, e.RowParam, evidenceNames, rowName)
	value := child.expr(e.Body)
	child.line("return {%s,nullptr};\n}\nfg_exit_label:return {{},fg_pending};\n}", value)
	return g.temp(e.Ty, "fg_make_fn<"+g.signature(fn)+">("+child.buf.String()+")")
}
func (g *generator) tree(t core.Tree, leaf func(core.Expr)) {
	switch t := t.(type) {
	case *core.Leaf:
		leaf(t.Body)
	case *core.Unreachable:
		g.line("fango_panic(\"unreachable pattern match\");")
	case *core.Guard:
		c := g.expr(t.Cond)
		g.line("if(%s){", c)
		g.tree(t.Then, leaf)
		g.line("}else{")
		g.tree(t.Else, leaf)
		g.line("}")
	case *core.SwitchLit:
		values := make([]string, len(t.Cases))
		for i, c := range t.Cases {
			values[i] = g.expr(c.Lit)
		}
		for i, c := range t.Cases {
			v := values[i]
			if i > 0 {
				g.line("else ")
			}
			g.line("if(fg_eq(%s,%s)){", g.locals[t.Scrut], v)
			g.tree(c.Tree, leaf)
			g.line("}")
		}
		g.line("else{")
		g.tree(t.Default, leaf)
		g.line("}")
	case *core.SwitchCtor:
		value := g.locals[t.Scrut]
		a := t.ADT
		var tag string
		switch {
		case a.Con.Unique == g.b.Bool.Unique:
			tag = "(" + value + "?1:0)"
		case a.Repr == types.ReprList:
			tag = "(" + value + ".empty()?0:1)"
		case a.Repr == types.ReprBytes:
			tag = "0"
		default:
			tag = g.tag(value, a)
		}
		g.line("switch(%s){", tag)
		for _, c := range t.Cases {
			index := c.Ctor.Index
			if a.Con.Unique == g.b.Bool.Unique {
				if c.Ctor.Name == "True" {
					index = 1
				} else {
					index = 0
				}
			}
			g.line("case %d:{", index)
			con := g.localTypes[t.Scrut].(*types.TCon)
			fields := a.InstFields(c.Ctor, con.Args)
			for i, name := range c.Binds {
				if name == "" {
					continue
				}
				source := g.member(value, a, c.Ctor.Index, i)
				if a.Repr == types.ReprList {
					if i == 0 {
						source = value + ".head()"
					} else {
						source = value + ".tail()"
					}
				}
				g.bind(name, fields[i], g.temp(fields[i], source))
			}
			g.tree(c.Tree, leaf)
			g.line("break;}")
		}
		g.line("default:{")
		if t.Default != nil {
			g.tree(t.Default, leaf)
		} else {
			g.line("fango_panic(\"invalid constructor tag\");")
		}
		g.line("break;}\n}")
	default:
		panic(fmt.Sprintf("unsupported decision tree %T", t))
	}
}
