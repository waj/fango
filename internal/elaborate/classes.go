package elaborate

import (
	"fmt"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

type dictionary struct {
	pred  types.Pred
	value core.Expr
}

// Dictionaries lower to compiler-internal, single-constructor ADTs. Core's
// existing constructor/application/projection checks consequently validate
// the complete evidence ABI in both backends.
func (el *elab) bindDictionaries(ps []types.Pred) ([]string, []types.Type) {
	var names []string
	var tys []types.Type
	for _, p := range ps {
		p.Ty = el.ck.Sub.Apply(p.Ty)
		cl := el.ck.Classes[p.Class]
		if cl == nil {
			continue
		}
		n := fmt.Sprintf("_dict%d", el.tmp)
		el.tmp++
		ty := cl.DictType(p.Ty)
		names = append(names, n)
		tys = append(tys, ty)
		el.dicts = append(el.dicts, dictionary{p, &core.VarRef{Name: n, Ty: ty}})
		el.pushScope(n, ty)
	}
	return names, tys
}

func prependTypes(ts []types.Type, t types.Type) types.Type {
	for i := len(ts) - 1; i >= 0; i-- {
		t = &types.TFun{Arg: ts[i], Ret: t}
	}
	return t
}

func (el *elab) instantiatedPreds(s types.Scheme, raw types.Type) []types.Pred {
	if len(s.Preds) == 0 {
		return nil
	}
	m := map[int]types.Type{}
	matchType(el.ck.Sub.Apply(s.Body), el.ck.Sub.Apply(raw), m)
	return types.SubstPreds(s.Preds, m)
}

func (el *elab) addEvidence(c callee, s types.Scheme, raw types.Type) callee {
	var ts []types.Type
	var args []core.Expr
	for _, p := range el.instantiatedPreds(s, raw) {
		e := el.dictionary(p)
		args = append(args, e)
		ts = append(ts, e.Type())
	}
	c.ty = prependTypes(ts, c.ty)
	c.arity += len(args)
	c.pre = append(args, c.pre...)
	return c
}

func (el *elab) givenDictionary(p types.Pred) core.Expr {
	p.Ty = el.ck.Sub.Apply(p.Ty)
	for i := len(el.dicts) - 1; i >= 0; i-- {
		d := el.dicts[i]
		if d.pred.Class == p.Class && types.Equal(el.ck.Sub.Apply(d.pred.Ty), p.Ty) {
			return d.value
		}
	}
	return nil
}

func (el *elab) dictionary(p types.Pred) core.Expr {
	p.Ty = el.ck.Sub.Apply(p.Ty)
	if d := el.givenDictionary(p); d != nil {
		return d
	}
	if err := infer.ResolutionPathError(el.evidencePath, p, source.Span{}); err != nil {
		el.errs = append(el.errs, *err)
		return &core.VarRef{Name: "_missingDictionary", Ty: el.ck.Classes[p.Class].DictType(p.Ty)}
	}
	el.evidencePath = append(el.evidencePath, p)
	defer func() { el.evidencePath = el.evidencePath[:len(el.evidencePath)-1] }()
	in, _, _ := el.matchInstance(p)
	if in == nil {
		el.errs = append(el.errs, diag.Errorf(source.Span{}, "MISSING INSTANCE", "No evidence for `%s %s`.", p.Class, types.Show(p.Ty)))
		cl := el.ck.Classes[p.Class]
		return &core.VarRef{Name: "_missingDictionary", Ty: cl.DictType(p.Ty)}
	}
	sch, _ := el.ck.Env.Lookup(in.Name)
	ty := in.Class.DictType(p.Ty)
	if len(sch.Vars) == 0 && len(sch.Preds) == 0 {
		return &core.VarRef{Name: in.Name, Ty: ty}
	}
	return el.nullaryValueUse(in.Name, sch, ty)
}

func (el *elab) methodValue(method *types.MethodInfo, raw types.Type) core.Expr {
	args := matchTyArgs(method.Type, []*types.TVar{method.Class.Param}, el.ck.Sub.Apply(raw))
	p := types.Pred{Class: method.Class.Name, Ty: args[0]}
	d := el.givenDictionary(p)
	if in, m, _ := el.matchInstance(p); d == nil && in != nil {
		n := in.Methods[method.Index]
		s, _ := el.ck.Env.Lookup(n)
		mt := types.SubstRigid(s.Body, m)
		return el.valueReference(n, mt)
	}
	if d == nil {
		d = el.dictionary(p)
	}
	bind := fmt.Sprintf("_dictionary%d", el.tmp)
	el.tmp++
	field := fmt.Sprintf("_method%d", el.tmp)
	el.tmp++
	binds := make([]string, len(method.Class.Methods))
	binds[method.Index] = field
	ty := el.zonkDefault(raw)
	return &core.Case{Scrut: d, Bind: bind, Ty: ty, Tree: &core.SwitchCtor{Scrut: bind, ADT: method.Class.Dict, Cases: []core.CtorCase{{Ctor: method.Class.Dict.Ctors[0], Binds: binds, Tree: &core.Leaf{Body: &core.VarRef{Name: field, Ty: ty}}}}}}
}

// An instance method knows its own dictionary independently of lookup.
// Referring to that factory with its existing context preserves recursive
// methods without selecting an instance for an arbitrary polymorphic type.
func (el *elab) matchInstance(p types.Pred) (*infer.InstanceInfo, map[int]types.Type, bool) {
	p.Ty = el.ck.Sub.Apply(p.Ty)
	if in := el.selfInstance; in != nil && p.Class == in.Class.Name && types.Equal(p.Ty, in.Head) {
		m := map[int]types.Type{}
		for _, v := range in.Vars {
			m[v.ID] = v
		}
		return in, m, false
	}
	var given []types.Pred
	for _, d := range el.dicts {
		given = append(given, d.pred)
	}
	if in := el.selfInstance; in != nil {
		given = append(given, types.Pred{Class: in.Class.Name, Ty: in.Head})
	}
	r := el.ck.ResolveInstance(p, el.owner, el.instanceLimit, given)
	if r.Error != nil {
		el.errs = append(el.errs, *r.Error)
	}
	return r.Instance, r.Bindings, r.Blocked
}

func (el *elab) valueReference(name string, raw types.Type) core.Expr {
	ty := el.zonkDefault(raw)
	if n := el.ck.Natives[name]; n != nil {
		return el.nativeValue(n, ty)
	}
	if arity, ok := el.ck.Workers[name]; ok {
		return el.curriedWorkerRef(name, ty, raw, arity)
	}
	sch, _ := el.ck.Env.Lookup(name)
	if hasRuntimeVars(sch) || len(sch.Preds) > 0 {
		return el.nullaryValueUse(name, sch, ty)
	}
	return &core.VarRef{Name: name, Ty: ty}
}

func instanceDefinition(in *infer.InstanceInfo, ck *infer.Checker) (core.Def, []diag.Error) {
	sch, _ := ck.Env.Lookup(in.Name)
	el := newElab(ck, in.Name, sch)
	el.owner = in.Owner
	el.instanceLimit = in.Limit
	params, dictTypes := el.bindDictionaries(in.Preds)
	ty := in.Class.DictType(in.Head)
	ctor := in.Class.Dict.Ctors[0]
	var fields []core.Expr
	for i, name := range in.Methods {
		mt := types.SubstRigid(in.Class.Methods[i].Type, map[int]types.Type{in.Class.Param.ID: in.Head})
		fields = append(fields, el.valueReference(name, mt))
	}
	ct := types.Type(ty)
	for i := len(fields) - 1; i >= 0; i-- {
		ct = &types.TFun{Arg: fields[i].Type(), Ret: ct}
	}
	body := &core.App{CalleeKind: core.Ctor, Callee: &core.VarRef{Name: ctor.Name, Ty: ct}, Args: fields, Ty: ty, TyArgs: []types.Type{in.Head}, Ctor: ctor}
	return core.Def{Name: in.Name, Owner: in.Owner, Type: prependTypes(dictTypes, ty), TyParams: in.Vars, Params: params, Body: el.anf(body)}, el.errs
}

// Display evaluates its argument once and renders through the ordinary Show
// instance. Values without Show remain inspectable without adding a constraint.
func Display(e core.Expr, ck *infer.Checker, owner string) core.Expr {
	el := newElab(ck, "", types.Scheme{})
	el.owner = owner
	ty := e.Type()
	name := "_displayValue"
	var body core.Expr
	if ck.CanResolve(ck.StandardPred("Show", ty), owner) {
		method := el.methodValue(ck.Methods["Basics.show"], &types.TFun{Arg: ty, Ret: ck.B.String})
		body = el.valueApp(method, &core.VarRef{Name: name, Ty: ty, Local: true})
	} else {
		text := "<value : " + types.Show(ty) + ">"
		if _, ok := ty.(*types.TFun); ok {
			text = "<function>"
		}
		body = &core.StringLit{Val: text, Ty: ck.B.String}
	}
	return el.anf(&core.Let{Name: name, Rhs: e, Body: body, Ty: ck.B.String})
}

func Instances(instances []*infer.InstanceInfo, ck *infer.Checker) ([]core.Def, []diag.Error) {
	var defs []core.Def
	var errs []diag.Error
	for _, in := range instances {
		d, es := instanceDefinition(in, ck)
		defs = append(defs, d)
		errs = append(errs, es...)
	}
	return defs, errs
}
