package elaborate

import (
	"encoding/hex"
	"fmt"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

type scalarVariant struct {
	def          core.Def
	dictionaries []string
	bindings     map[string]core.Expr
	types        map[int]types.Type
}

// specializeScalars emits at most two variants per eligible source worker:
// one numeric type parameter, standard scalar predicates, and effect-free Core.
// Variants belong to the original owner and are emitted regardless of uses,
// preserving module-package stability. The already-resolved Core is cloned;
// instance selection is never rerun at a more concrete type.
func specializeScalars(p *core.Prog, infos []infer.DeclInfo, ck *infer.Checker) {
	s := &scalarSpecializer{ck: ck, variants: map[string]map[int]*scalarVariant{}, dictionaries: map[string]*infer.InstanceInfo{}, methods: map[string]scalarMethod{}}
	for _, in := range ck.Instances {
		if in.Owner != "Basics" || len(in.Vars) != 0 || len(in.Preds) != 0 {
			continue
		}
		s.dictionaries[in.Name] = in
		for i, name := range in.Methods {
			s.methods[name] = scalarMethod{in, i}
		}
	}
	byName := map[string]infer.DeclInfo{}
	for _, info := range infos {
		byName[info.Name] = info
	}
	var ordered []*scalarVariant
	for _, d := range p.Defs {
		info, ok := byName[d.Name]
		if !ok || len(info.Params) == 0 || len(d.TyParams) != 1 || len(info.Scheme.Preds) == 0 || !pureType(d.Type) {
			continue
		}
		v := d.TyParams[0]
		numeric, eligible := false, true
		for _, pred := range info.Scheme.Preds {
			if !types.Equal(pred.Ty, v) {
				eligible = false
				break
			}
			switch pred.Class {
			case "Basics.Num":
				numeric = true
			case "Basics.Eq", "Basics.Ord", "Basics.Show":
			default:
				eligible = false
			}
		}
		if !numeric || !eligible || len(d.EffectParams) != 0 || !effectFreeBody(d.Body) {
			continue
		}
		for _, scalar := range []*types.TCon{ck.B.Int, ck.B.Float} {
			variant := &scalarVariant{def: d, bindings: map[string]core.Expr{}, types: map[int]types.Type{v.ID: scalar}}
			for i, pred := range info.Scheme.Preds {
				r := ck.ResolveInstance(types.Pred{Class: pred.Class, Ty: scalar}, d.Owner, info.InstanceLimit, nil)
				in := r.Instance
				if r.Blocked || r.Error != nil || in == nil || s.dictionaries[in.Name] == nil {
					eligible = false
					break
				}
				variant.dictionaries = append(variant.dictionaries, in.Name)
				variant.bindings[d.Params[i]] = &core.VarRef{Name: in.Name, Ty: in.Class.DictType(scalar)}
			}
			if !eligible {
				continue
			}
			name := "_scalar_" + scalar.Name + "_" + hex.EncodeToString([]byte(d.Name))
			if d.Owner != "" {
				name = d.Owner + "." + name
			}
			variant.def.Name = name
			variant.def.TyParams = nil
			variant.def.Params = append([]string(nil), d.Params[len(variant.dictionaries):]...)
			variant.def.ParamCaptures = append([]types.CaptureVar(nil), d.ParamCaptures[len(variant.dictionaries):]...)
			_, bodyType := core.PeelFun(d.Type, len(variant.dictionaries))
			variant.def.Type = types.SubstRigid(bodyType, variant.types)
			if s.variants[d.Name] == nil {
				s.variants[d.Name] = map[int]*scalarVariant{}
			}
			s.variants[d.Name][scalar.Unique] = variant
			ordered = append(ordered, variant)
		}
	}
	if len(ordered) == 0 {
		return
	}
	for _, variant := range ordered {
		s.fresh = 0
		s.folder = newElab(ck, variant.def.Name, types.Scheme{})
		typ := func(t types.Type) types.Type { return types.SubstRigid(t, variant.types) }
		body := core.Rewrite(variant.def.Body, typ, func(e core.Expr) core.Expr {
			if v, ok := e.(*core.VarRef); ok {
				if value := variant.bindings[v.Name]; value != nil {
					return value
				}
			}
			return e
		})
		variant.def.Body = core.Rewrite(body, sameType, s.simplify)
	}
	for i := range p.Defs {
		p.Defs[i].Body = core.Rewrite(p.Defs[i].Body, sameType, s.redirect)
	}
	if p.EntryDisplay != nil {
		p.EntryDisplay = core.Rewrite(p.EntryDisplay, sameType, s.redirect)
	}
	for _, v := range ordered {
		p.Defs = append(p.Defs, v.def)
	}
}

func sameType(t types.Type) types.Type { return t }

// A pure result can still contain internal handlers. Keep those workers
// generic: moving closures across handlers could change captured evidence.
func effectFreeBody(body core.Expr) bool {
	pure := true
	core.Rewrite(body, func(t types.Type) types.Type {
		pure = pure && pureType(t)
		return t
	}, func(e core.Expr) core.Expr {
		switch e := e.(type) {
		case *core.Handle, *core.Perform, *core.ResumeTail, *core.Bracket:
			pure = false
		case *core.App:
			pure = pure && len(e.EvidenceArgs) == 0
		}
		return e
	})
	return pure
}

func pureType(t types.Type) bool {
	switch t := t.(type) {
	case *types.TFun:
		return len(t.Eff.Labels) == 0 && t.Eff.Tail == nil && pureType(t.Arg) && pureType(t.Ret)
	case *types.TCon:
		for _, a := range t.Args {
			if !pureType(a) {
				return false
			}
		}
	}
	return true
}

type scalarMethod struct {
	instance *infer.InstanceInfo
	index    int
}
type scalarSpecializer struct {
	ck           *infer.Checker
	variants     map[string]map[int]*scalarVariant
	dictionaries map[string]*infer.InstanceInfo
	methods      map[string]scalarMethod
	fresh        int
	folder       *elab
}

func (s *scalarSpecializer) redirect(e core.Expr) core.Expr {
	a, ok := e.(*core.App)
	if !ok || a.CalleeKind != core.Worker || len(a.TyArgs) != 1 {
		return e
	}
	v, ok := a.Callee.(*core.VarRef)
	if !ok {
		return e
	}
	t, ok := a.TyArgs[0].(*types.TCon)
	if !ok {
		return e
	}
	variant := s.variants[v.Name][t.Unique]
	if variant == nil || len(a.Args) < len(variant.dictionaries) {
		return e
	}
	for i, name := range variant.dictionaries {
		d, ok := a.Args[i].(*core.VarRef)
		if !ok || d.Local || d.Name != name {
			return e
		}
	}
	n := *a
	n.Callee = &core.VarRef{Name: variant.def.Name, Ty: variant.def.Type}
	n.TyArgs = nil
	n.Args = append([]core.Expr(nil), a.Args[len(variant.dictionaries):]...)
	return &n
}

// Only literals, lexical aliases, and lambda values may be substituted away.
// Calls and global cells retain strict, evaluate-once Let bindings.
func duplicable(e core.Expr) bool {
	switch e := e.(type) {
	case *core.IntLit, *core.FloatLit, *core.StringLit, *core.CharLit, *core.BoolLit, *core.UnitLit, *core.Lambda:
		return true
	case *core.VarRef:
		return e.Local
	}
	return false
}
func (s *scalarSpecializer) replace(e core.Expr, name string, value core.Expr) core.Expr {
	return core.Rewrite(e, sameType, func(e core.Expr) core.Expr {
		if v, ok := e.(*core.VarRef); ok && v.Name == name {
			return value
		}
		return e
	})
}
func (s *scalarSpecializer) simplify(e core.Expr) core.Expr {
	switch e := e.(type) {
	case *core.Case:
		ref, ok := e.Scrut.(*core.VarRef)
		if !ok {
			break
		}
		in := s.dictionaries[ref.Name]
		tree, ok := e.Tree.(*core.SwitchCtor)
		if in == nil || !ok || tree.ADT != in.Class.Dict || len(tree.Cases) != 1 {
			break
		}
		leaf, ok := tree.Cases[0].Tree.(*core.Leaf)
		if !ok {
			break
		}
		field, ok := leaf.Body.(*core.VarRef)
		if !ok {
			break
		}
		for i, bind := range tree.Cases[0].Binds {
			if bind != "" && bind == field.Name {
				return s.methodValue(scalarMethod{in, i}, e.Ty)
			}
		}
	case *core.Let:
		if !e.Rec && duplicable(e.Rhs) {
			return core.Rewrite(s.replace(e.Body, e.Name, e.Rhs), sameType, s.simplify)
		}
	case *core.App:
		if e.CalleeKind == core.Value {
			if binding, ok := e.Callee.(*core.Let); ok && !binding.Rec && len(e.Args) == 1 && !core.Mentions(e.Args[0], binding.Name) {
				call := *e
				call.Callee = binding.Body
				return s.simplify(&core.Let{Name: binding.Name, Rhs: binding.Rhs, Body: s.simplify(&call), Ty: e.Ty})
			}
			if lambda, ok := e.Callee.(*core.Lambda); ok && len(e.Args) == 1 {
				// Beta reduction uses a strict Let; its argument is never duplicated.
				return s.simplify(&core.Let{Name: lambda.Param, Rhs: e.Args[0], Body: lambda.Body, Ty: e.Ty})
			}
		} else if e.CalleeKind == core.Worker {
			if ref, ok := e.Callee.(*core.VarRef); ok {
				if method, ok := s.methods[ref.Name]; ok {
					if method.instance.IdentityMethods[method.index] && len(e.Args) == 1 {
						return e.Args[0]
					}
					if name := method.instance.NativeMethods[method.index]; name != "" {
						return s.folder.fold(&core.NativeCall{Name: name, Module: s.ck.Natives[name].Module, Args: e.Args, Ty: e.Ty})
					}
				}
			}
		}
	case *core.NativeCall:
		return s.folder.fold(e)
	}
	return s.redirect(e)
}
func (s *scalarSpecializer) methodValue(method scalarMethod, ty types.Type) core.Expr {
	var args []core.Expr
	var arrows []types.Type
	ret := ty
	for {
		fn, ok := ret.(*types.TFun)
		if !ok {
			break
		}
		name := fmt.Sprintf("_specializedArg%d", s.fresh)
		s.fresh++
		args = append(args, &core.VarRef{Name: name, Ty: fn.Arg, Local: true})
		arrows = append(arrows, ret)
		ret = fn.Ret
	}
	var body core.Expr
	if method.instance.IdentityMethods[method.index] {
		body = args[0]
	} else if name := method.instance.NativeMethods[method.index]; name != "" {
		body = &core.NativeCall{Name: name, Module: s.ck.Natives[name].Module, Args: args, Ty: ret}
	} else {
		body = &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: method.instance.Methods[method.index], Ty: ty}, Args: args, Ty: ret, Control: core.ArrowControl(ty, len(args))}
	}
	for i := len(args) - 1; i >= 0; i-- {
		body = &core.Lambda{Param: args[i].(*core.VarRef).Name, Body: body, Ty: arrows[i], ParamCapture: s.ck.Sup.FreshCapture()}
	}
	return body
}
