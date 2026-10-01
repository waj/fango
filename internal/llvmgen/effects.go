package llvmgen

import (
	"fmt"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func (g *generator) perform(e *core.Perform) string {
	if e.Op.Native != nil && g.evidence[e.Effect.Key()] == "" {
		return g.native(&core.NativeCall{Name: e.Op.Native.Name, Module: e.Op.Native.Module, Args: e.Args, Ty: e.Ty, Storage: e.Op.Native.Storage})
	}
	var values []string
	for _, a := range e.Args {
		values = append(values, g.expr(a))
	}
	ev := g.ev(e.Effect)
	var args []string
	if len(e.Op.LocalVars) > 0 {
		args = append(args, g.descriptorsArray(e.LocalTypes))
	}
	sub := map[int]types.Type{}
	for i, v := range e.Op.Owner.Params {
		sub[v.ID] = e.Effect.Args[i]
	}
	erased := g.clone()
	for _, v := range e.Op.LocalVars {
		erased.names[v.ID] = "fg_any"
	}
	for i, v := range values {
		want := types.SubstRigid(e.Op.ParamTypes[i], sub)
		if len(e.Op.LocalVars) > 0 {
			v = "fg_convert<" + erased.typ(want) + ">(" + v + ")"
		}
		args = append(args, v)
	}
	result := types.SubstRigid(e.Op.ResultType, sub)
	member := "call"
	if e.Control.Resolve(g.mode) == types.Exit {
		member = "call_exit"
	}
	call := fmt.Sprintf("(%s)->op%d.%s(%s)", ev, e.Op.Index, member, strings.Join(args, ","))
	if len(e.Op.Owner.Params) > 0 && len(e.Op.LocalVars) == 0 {
		out := g.temp(e.Ty, "{}")
		g.line("if((%s)->layout==fg_type<%s>()){", ev, g.evType(e.Effect))
		value := call
		if member == "call_exit" {
			value = g.out(e.Ty, call)
		}
		g.line("%s=%s;\n}else{", out, value)
		payload := g.fresh()
		g.line("fg_any %s[]={%s};", payload, strings.Join(func() []string {
			var xs []string
			for _, v := range values {
				xs = append(xs, "fg_box("+v+")")
			}
			if len(xs) == 0 {
				xs = append(xs, "fg_any{}")
			}
			return xs
		}(), ","))
		result := g.fresh()
		g.line("auto %s=(%s)->operations[%d].invoke((%s)->operations[%d].environment,nullptr,%s);", result, ev, e.Op.Index, ev, e.Op.Index, payload)
		g.line("if(%s.exit){%s=%s.exit;goto %s;}", result, g.pending, result, g.exitLabel)
		g.line("%s=fg_convert<%s>(%s.value);\n}", out, g.typ(e.Ty), result)
		return out
	}
	if member == "call_exit" {
		name := g.fresh()
		g.line("auto %s=%s;", name, call)
		g.line("if(%s.exit){%s=%s.exit;goto %s;}", name, g.pending, name, g.exitLabel)
		call = name + ".value"
	}
	if len(e.Op.LocalVars) > 0 {
		call = "fg_convert<" + g.typ(e.Ty) + ">(" + call + ")"
	} else {
		_ = result
	}
	return g.temp(e.Ty, call)
}

func (g *generator) handle(e *core.Handle) string {
	child := g.clone()
	child.mode = types.Exit
	child.pending = "fg_pending"
	child.exitLabel = "fg_body_exit"
	child.line("[=]()->fg_out<%s>{", g.typ(e.Ty))
	child.line("fg_exit *fg_pending=nullptr;")
	// State acquisition may itself exit before the activation exists.
	state := ""
	if e.State != nil {
		child.line("{")
		v := child.expr(e.State.Initial)
		state = child.fresh()
		child.line("auto *%s=fg_new(%s);", state, v)
	} else {
		child.line("{")
	}
	activation := child.fresh()
	child.line("auto *%s=fg_new(%s{});", activation, g.evType(e.Effect))
	child.line("%s->name=%s;%s->count=%d;", activation, stringValue(e.Effect.Name), activation, len(e.Effect.Args))
	child.line("%s->layout=fg_type<%s>();", activation, g.evType(e.Effect))
	if len(e.Effect.Args) > 0 {
		child.line("%s->operations=static_cast<fg_erased_op *>(fango_alloc(sizeof(fg_erased_op)*%d));", activation, len(e.Clauses))
	}
	if len(e.Effect.Args) > 0 {
		child.line("%s->arguments=static_cast<const fg_descriptor **>(fango_alloc(sizeof(fg_descriptor *)*%d));", activation, len(e.Effect.Args))
		for i, t := range e.Effect.Args {
			child.line("%s->arguments[%d]=%s;", activation, i, g.descriptor(t))
		}
	}
	for _, c := range e.Clauses {
		if c.Op.Abort {
			continue
		}
		op := g.clone()
		op.mode = types.Exit
		op.pending = "fg_pending"
		op.exitLabel = "fg_op_exit"
		op.state = state
		for _, v := range c.LocalVars {
			op.names[v.ID] = "fg_any"
		}
		var params, args []string
		if len(c.LocalVars) > 0 {
			params = append(params, "const fg_descriptor **fg_types")
			args = append(args, "const fg_descriptor **")
			for i, v := range c.LocalVars {
				op.descriptors[v.ID] = fmt.Sprintf("fg_types[%d]", i)
			}
		}
		for i, t := range c.ParamTypes {
			name := op.fresh()
			params = append(params, op.typ(t)+" "+name)
			args = append(args, op.typ(t))
			op.bind(c.Params[i], t, name)
		}
		result := c.Op.ResultType
		sub := map[int]types.Type{}
		for i, v := range c.Op.Owner.Params {
			sub[v.ID] = e.Effect.Args[i]
		}
		for i, v := range c.Op.LocalVars {
			sub[v.ID] = c.LocalVars[i]
		}
		result = types.SubstRigid(result, sub)
		op.line("[=](%s)->fg_out<%s>{fg_exit *fg_pending=nullptr;\n{", strings.Join(params, ","), op.typ(result))
		if e.State != nil {
			op.bind(e.State.Name, e.State.Ty, op.temp(e.State.Ty, "*"+state))
		}
		op.resumeTail(c.Body)
		op.line("}\nfg_op_exit:return {{},fg_pending};\n}")
		child.line("%s->op%d=fg_make_fn<%s(%s)>(%s);", activation, c.Op.Index, op.typ(result), strings.Join(args, ","), op.buf.String())
		if len(e.Effect.Args) > 0 && len(c.LocalVars) == 0 {
			child.line("%s->operations[%d]=fg_erase_operation(%s->op%d);", activation, c.Op.Index, activation, c.Op.Index)
		}
	}
	body := child.clone()
	body.evidence[e.Effect.Key()] = activation
	body.exitLabel = "fg_handler_exit"
	body.line("fg_out<%s> fg_body{};", g.typ(e.Body.Type()))
	body.line("{")
	v := body.expr(e.Body)
	body.line("fg_body.value=%s;\n}", v)
	body.line("if(!fg_pending){")
	ret := g.clone()
	ret.mode = types.Exit
	ret.pending = "fg_pending"
	ret.exitLabel = "fg_handler_exit"
	if e.State != nil {
		ret.bind(e.State.Name, e.State.Ty, ret.temp(e.State.Ty, "*"+state))
	}
	if e.Return != nil {
		ret.bind(e.Return.Param, e.Body.Type(), "fg_body.value")
		v = ret.expr(e.Return.Body)
	} else {
		v = "fg_body.value"
	}
	ret.line("return {%s,nullptr};", v)
	body.buf.Write(ret.buf.Bytes())
	body.line("}")
	body.line("fg_handler_exit:")
	body.line("if(fg_pending&&fg_pending->target==%s){switch(fg_pending->index){", activation)
	for _, c := range e.Clauses {
		if !c.Op.Abort {
			continue
		}
		caught := g.clone()
		caught.mode = types.Exit
		caught.pending = "fg_pending"
		caught.exitLabel = "fg_clause_exit_" + caught.fresh()
		caught.line("case %d:{", c.Op.Index)
		if e.State != nil {
			caught.bind(e.State.Name, e.State.Ty, caught.temp(e.State.Ty, "*"+state))
		}
		for i, p := range c.Params {
			caught.bind(p, c.ParamTypes[i], caught.temp(c.ParamTypes[i], fmt.Sprintf("fg_unbox<%s>(fg_pending->payload[%d])", caught.typ(c.ParamTypes[i]), i)))
		}
		if c.SuppressedParam != "" {
			caught.bind(c.SuppressedParam, c.SuppressedType, caught.temp(c.SuppressedType, "fg_suppressed(fg_pending)"))
		}
		caught.line("fg_pending=nullptr;\n{")
		v := caught.expr(c.Body)
		caught.line("return {%s,nullptr};\n}\n%s:return {{},fg_pending};\n}", v, caught.exitLabel)
		body.buf.Write(caught.buf.Bytes())
	}
	body.line("default:fango_panic(\"missing abort clause\");}}return {{},fg_pending};")
	child.buf.Write(body.buf.Bytes())
	child.line("}\nfg_body_exit:return {{},fg_pending};\n}")
	return g.out(e.Ty, "("+child.buf.String()+")()")
}

func (g *generator) resumeTail(e core.Expr) {
	switch e := e.(type) {
	case *core.ResumeTail:
		v := g.expr(e.Value)
		if e.NextState != nil {
			n := g.expr(e.NextState)
			g.line("*%s=%s;", g.state, n)
		}
		g.line("return {%s,nullptr};", v)
	case *core.ControlExit:
		g.expr(e)
	case *core.Let:
		if e.Rec {
			panic("recursive binding in resumptive clause")
		}
		v := g.expr(e.Rhs)
		g.bind(e.Name, e.Rhs.Type(), v)
		g.resumeTail(e.Body)
	case *core.Seq:
		g.expr(e.First)
		g.resumeTail(e.Then)
	case *core.If:
		c := g.expr(e.Cond)
		g.line("if(%s){", c)
		g.resumeTail(e.Then)
		g.line("}else{")
		g.resumeTail(e.Else)
		g.line("}")
	case *core.Case:
		v := g.expr(e.Scrut)
		g.bind(e.Bind, e.Scrut.Type(), v)
		g.tree(e.Tree, func(e core.Expr) { g.resumeTail(e) })
	default:
		g.line("return {%s,nullptr};", g.expr(e))
	}
}

func (g *generator) bracket(e *core.Bracket) string {
	child := g.clone()
	child.mode = types.Exit
	child.pending = "fg_pending"
	child.exitLabel = "fg_acquire_exit"
	child.line("[=]()->fg_out<%s>{fg_exit *fg_pending=nullptr;\n{", g.typ(e.Ty))
	resource := child.expr(e.Acquire)
	child.bind(e.Resource, e.ResourceTy, resource)
	child.line("fg_out<%s> fg_body{};\n{", g.typ(e.Body.Type()))
	child.exitLabel = "fg_release"
	v := child.expr(e.Body)
	child.line("fg_body.value=%s;\n}\nfg_release:\nfg_body.exit=fg_pending;fg_pending=nullptr;\n{", v)
	child.exitLabel = "fg_release_exit"
	child.expr(e.Release)
	child.line("}\nfg_release_exit:fg_body.exit=fg_suppress(fg_body.exit,fg_pending);return fg_body;\n}\nfg_acquire_exit:return {{},fg_pending};\n}")
	return g.out(e.Ty, "("+child.buf.String()+")()")
}

func (g *generator) failure(e *core.FailureInspect) string {
	var args []string
	for _, a := range e.Args {
		args = append(args, g.expr(a))
	}
	f := args[len(args)-1]
	switch e.Name {
	case types.FailureEffectName:
		return g.temp(e.Ty, f+"->effect")
	case types.FailureOperationName:
		return g.temp(e.Ty, f+"->operation")
	case types.FailureArgumentCountName:
		return g.temp(e.Ty, "int64_t("+f+"->count)")
	case types.FailureSuppressedName:
		return g.temp(e.Ty, "fg_suppressed("+f+")")
	case types.FailureArgumentName:
		t := e.Ty.(*types.TCon).Args[0]
		result := g.temp(e.Ty, g.ctor(e.Result.Ctors[0], e.Ty, nil))
		index := args[0]
		descriptor := g.descriptor(t)
		g.line("if(%s>=0&&uint64_t(%s)<%s->count&&%s->inspectable&&fg_descriptor_equal(%s->payload[%s].type,%s)){", index, index, f, descriptor, f, index, descriptor)
		g.line("%s=%s;\n}", result, g.ctor(e.Result.Ctors[1], e.Ty, []string{"fg_unbox<" + g.typ(t) + ">(" + f + "->payload[" + index + "])"}))
		return result
	}
	panic("unknown failure projection")
}
