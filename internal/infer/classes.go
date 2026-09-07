package infer

import (
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

type InstanceInfo struct {
	Class       *types.ClassInfo
	Head        types.Type
	Vars        []*types.TVar
	Preds       []types.Pred
	Name, Owner string
	Span        source.Span
	Methods     []string
	// Limit freezes the instance environment at this declaration.
	Limit int
	// Exact native forwarding and identity bodies can bypass dictionary
	// projection when an instance is statically known.
	NativeMethods   map[int]string
	IdentityMethods map[int]bool
}

func (ck *Checker) ClassDecl(d *ast.ClassDecl) []diag.Error {
	if ck.Classes[d.Name] != nil || ck.TypeNames[d.Name] != nil || ck.Effects[d.Name] != nil {
		return []diag.Error{diag.Errorf(d.NameSpan, "MULTIPLE DEFINITIONS", "The class `%s` is already defined or collides with a type or effect.", d.Name)}
	}
	p := ck.Sup.FreshRigid(types.General)
	scope := newCtorScope([]string{d.Param.Name}, []*types.TVar{p})
	dictName := "_dictionary_" + types.SurfaceName(d.Name)
	if owner := symbolModule(d.Name); owner != "" {
		dictName = owner + "." + dictName
	}
	con := &types.TCon{Unique: ck.Sup.NextUnique(), Name: dictName, Args: []types.Type{p}}
	adt := &types.ADTInfo{Con: con, Params: []*types.TVar{p}}
	cl := &types.ClassInfo{Name: d.Name, Param: p, Dict: adt}
	var errs []diag.Error
	seen := map[string]bool{}
	for i, m := range d.Methods {
		if seen[m.Name] || ck.Env.Has(m.Name) {
			errs = append(errs, diag.Errorf(m.NameSpan, "MULTIPLE DEFINITIONS", "Method `%s` is already defined.", m.Name))
			continue
		}
		seen[m.Name] = true
		ty, es := ck.ResolveTypeExpr(m.Type, scope)
		errs = append(errs, es...)
		if ty == nil {
			continue
		}
		ids := map[int]bool{}
		collectVarIDs(ty, ids)
		_, fn := ty.(*types.TFun)
		if !fn || !ids[p.ID] || hasOpenRow(ty) {
			errs = append(errs, diag.Errorf(m.NameSpan, "CLASS METHOD TYPE", "A method must be a function mentioning `%s`, with no additional type variables or open effect rows.", d.Param.Name))
		}
		cl.Methods = append(cl.Methods, types.MethodInfo{Name: m.Name, Type: ty, Class: cl, Index: i})
	}
	if len(errs) > 0 {
		return errs
	}
	ctor := &types.CtorInfo{Name: con.Name + "Value", Result: con}
	for i := range cl.Methods {
		m := &cl.Methods[i]
		ctor.Fields = append(ctor.Fields, m.Type)
		ck.Methods[m.Name] = m
		ck.Env.Bind(m.Name, types.Scheme{Vars: []*types.TVar{p}, Preds: []types.Pred{{Class: cl.Name, Ty: p}}, Body: m.Type})
	}
	adt.Ctors = []*types.CtorInfo{ctor}
	ck.Classes[d.Name] = cl
	ck.ADTs[con.Unique] = adt
	ck.ADTOrder = append(ck.ADTOrder, adt)
	return nil
}

func hasOpenRow(t types.Type) bool {
	switch t := t.(type) {
	case *types.TFun:
		return hasOpenRow(t.Eff) || hasOpenRow(t.Arg) || hasOpenRow(t.Ret)
	case types.Row:
		if t.Tail != nil {
			return true
		}
		for _, l := range t.Labels {
			for _, a := range l.Args {
				if hasOpenRow(a) {
					return true
				}
			}
		}
	case *types.TCon:
		for _, a := range t.Args {
			if hasOpenRow(a) {
				return true
			}
		}
	}
	return false
}

func (ck *Checker) ResolvePreds(ps []ast.PredExpr, scope *TypeVars) ([]types.Pred, []diag.Error) {
	var out []types.Pred
	var errs []diag.Error
	for _, p := range ps {
		cl := ck.Classes[p.Class]
		if cl == nil {
			errs = append(errs, diag.Errorf(p.Sp, "UNKNOWN CLASS", "I don't know a class named `%s`.", p.Class))
			continue
		}
		ty, es := ck.ResolveTypeExpr(p.Ty, scope)
		errs = append(errs, es...)
		if ty != nil {
			out = append(out, types.Pred{Class: cl.Name, Ty: ty})
		}
	}
	return out, errs
}

func (ck *Checker) InstanceDecl(d *ast.InstanceDecl) ([]DeclInfo, []diag.Error) {
	cl := ck.Classes[d.Head.Class]
	if cl == nil {
		return nil, []diag.Error{diag.Errorf(d.Head.Sp, "UNKNOWN CLASS", "I don't know class `%s`.", d.Head.Class)}
	}
	scope := ck.NewAnnScope()
	head, errs := ck.ResolveTypeExpr(d.Head.Ty, scope)
	switch head.(type) {
	case *types.TCon, *types.TVar:
	default:
		return nil, append(errs, diag.Errorf(d.Head.Sp, "INSTANCE HEAD", "An instance head must be a type variable or a fully applied named type."))
	}
	if hasOpenRow(head) {
		return nil, append(errs, diag.Errorf(d.Head.Sp, "INSTANCE HEAD", "An instance head may not contain open effect rows."))
	}
	seen := map[int]bool{}
	collectVarIDs(head, seen)
	scope.open = false
	ps, es := ck.ResolvePreds(d.Preds, scope)
	errs = append(errs, es...)
	ps = ck.NormalizePreds(ps)
	for _, p := range ps {
		if hasOpenRow(p.Ty) {
			errs = append(errs, diag.Errorf(d.Head.Sp, "INSTANCE CONTEXT", "Instance constraints may not contain open effect rows."))
		}
		if _, blanket := head.(*types.TVar); blanket {
			if v, ok := p.Ty.(*types.TVar); !ok || !seen[v.ID] {
				errs = append(errs, diag.Errorf(d.Head.Sp, "INSTANCE CONTEXT", "Blanket instance constraints must apply to the head's type variable."))
			}
		}
	}
	for _, old := range ck.Instances {
		if old.Class != cl {
			continue
		}
		oldGeq := headAtLeastAsSpecific(old.Head, head)
		newGeq := headAtLeastAsSpecific(head, old.Head)
		switch {
		case oldGeq && newGeq && contextIncludes(head, ps, old.Head, old.Preds) && contextIncludes(old.Head, old.Preds, head, ps):
			errs = append(errs, diag.Errorf(d.Head.Sp, "OVERLAPPING INSTANCE", "Instance `%s %s` duplicates the instance declared at %v.", types.SurfaceName(cl.Name), types.Show(head), old.Span.StartPos()))
		case !oldGeq && !newGeq && headsUnify(old.Head, head):
			errs = append(errs, diag.Errorf(d.Head.Sp, "OVERLAPPING INSTANCE", "Instance `%s %s` overlaps the instance declared at %v; neither is more specific, so some uses would be ambiguous.", types.SurfaceName(cl.Name), types.Show(head), old.Span.StartPos()))
		}
	}
	if _, blanket := head.(*types.TVar); blanket {
		if cycle := ck.blanketCycle(cl.Name, ps); len(cycle) > 0 {
			errs = append(errs, diag.Errorf(d.Head.Sp, "INSTANCE CONTEXT", "Circular blanket instance requirements: %s.", strings.Join(cycle, " -> ")))
		}
	}
	methods := map[string]*ast.ValueDecl{}
	for _, m := range d.Methods {
		n := types.SurfaceName(m.Name)
		if methods[n] != nil {
			errs = append(errs, diag.Errorf(m.NameSpan, "DUPLICATE METHOD", "Method `%s` is defined twice.", n))
		}
		methods[n] = m
	}
	for _, m := range cl.Methods {
		if methods[types.SurfaceName(m.Name)] == nil {
			errs = append(errs, diag.Errorf(d.Head.Sp, "MISSING METHOD", "Instance requires method `%s`.", types.SurfaceName(m.Name)))
		}
	}
	for n, m := range methods {
		found := false
		for _, cm := range cl.Methods {
			if types.SurfaceName(cm.Name) == n {
				found = true
			}
		}
		if !found {
			errs = append(errs, diag.Errorf(m.NameSpan, "UNKNOWN METHOD", "Class `%s` has no method `%s`.", cl.Name, n))
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	name := "_instance_" + hex.EncodeToString([]byte(cl.Name)) + "_" + hex.EncodeToString([]byte(canonicalHeadKey(head)))
	if len(ps) > 0 {
		name += "_context_" + hex.EncodeToString([]byte(canonicalContextKey(head, ps)))
	}
	if ck.MonoValues {
		name += "_generation_"
		for i, u := range headUniques(head) {
			if i > 0 {
				name += "_"
			}
			name += strconv.Itoa(u)
		}
		for _, p := range ck.NormalizePreds(ps) {
			for _, u := range headUniques(p.Ty) {
				name += "_" + strconv.Itoa(u)
			}
		}
	}
	if d.Owner != "" {
		name = d.Owner + "." + name
	}
	inst := &InstanceInfo{Class: cl, Head: head, Vars: scope.Minted(), Preds: ck.NormalizePreds(ps), Name: name, Owner: d.Owner, Span: d.Head.Sp, Limit: len(ck.Instances) + 1}
	inst.NativeMethods = map[int]string{}
	inst.IdentityMethods = map[int]bool{}
	ck.Instances = append(ck.Instances, inst)
	ck.Env.Bind(name, types.Scheme{Vars: inst.Vars, Preds: inst.Preds, Body: cl.DictType(head)})
	var infos []DeclInfo
	for _, cm := range cl.Methods {
		m := methods[types.SurfaceName(cm.Name)]
		m.Name = name + "_" + types.SurfaceName(cm.Name)
		mt := types.SubstRigid(cm.Type, map[int]types.Type{cl.Param.ID: head})
		info, es := ck.instanceMethod(m, mt, inst)
		errs = append(errs, es...)
		ck.BindDecl(info)
		infos = append(infos, info)
		inst.Methods = append(inst.Methods, m.Name)
		if len(inst.Vars) == 0 && len(inst.Preds) == 0 {
			if v, ok := m.Body.(*ast.Var); ok && len(m.Params) == 1 && v.Name == m.Params[0].Name {
				inst.IdentityMethods[cm.Index] = true
			}
			if body, ok := m.Body.(*ast.App); ok {
				args := appArgs(body)
				if v, ok := appHead(body).(*ast.Var); ok && len(args) == len(m.Params) && len(args) > 0 {
					if n := ck.Natives[v.Name]; n != nil && n.Effect == nil && n.Arity == len(args) {
						forward := true
						for i, a := range args {
							v, ok := a.(*ast.Var)
							forward = forward && ok && v.Name == m.Params[i].Name
						}
						if forward {
							inst.NativeMethods[cm.Index] = n.Name
						}
					}
				}
			}
		}
	}
	return infos, errs
}

func (ck *Checker) instanceMethod(d *ast.ValueDecl, ty types.Type, inst *InstanceInfo) (DeclInfo, []diag.Error) {
	previous := ck.checkingInstance
	ck.checkingInstance = inst
	defer func() { ck.checkingInstance = previous }()
	given := inst.Preds
	g := &generator{ck: ck, ambient: types.Row{Tail: ck.Sup.FreshVar(types.RowVar)}}
	var inferred types.Type
	if len(d.Params) > 0 {
		inferred = g.functionWithAnnotatedParams(d.Name, d.NameSpan, d.Params, d.Body, ty)
	} else {
		inferred = g.expr(d.Body)
		// Constructing the method value must be pure. Effects belong to its
		// arrows and may execute only when the method is applied.
		g.cs = append(g.cs, Constraint{Left: g.ambient, Right: types.Row{}, Span: d.Body.Span(), Why: Why{Kind: WhyEffectEscapes}})
	}
	g.cs = append(g.cs, Constraint{Left: inferred, Right: ty, Span: d.NameSpan, Why: Why{Kind: WhyAnnotation, Name: d.Name}})
	sub, _, errs := Solve(g.cs, nil, ck.Sub, ck.B, ck.Sup)
	ck.Sub = sub
	errs = append(errs, g.errs...)
	g.errs = nil
	g.resolveRecords(true)
	errs = append(errs, g.errs...)
	sch := ck.generalize(ty, nil)
	sch.Preds = ck.NormalizePreds(given)
	var es []diag.Error
	sch, es = ck.qualify(sch, g.preds, given, true, d.NameSpan)
	errs = append(errs, es...)
	return DeclInfo{Name: d.Name, NameSpan: d.NameSpan, Params: d.Params, Type: ty, Body: d.Body, Scheme: sch, Instance: inst, InstanceLimit: inst.Limit}, errs
}

// annotatedDecl checks a declaration against a signature the compiler
// supplies rather than one the author wrote. Deriver methods are the only
// such declaration: their type is dictated by the class they generate for.
func (ck *Checker) annotatedDecl(d *ast.ValueDecl, ty types.Type) (DeclInfo, []diag.Error) {
	g := &generator{ck: ck, ambient: types.Row{Tail: ck.Sup.FreshVar(types.RowVar)}}
	var inferred types.Type
	if len(d.Params) > 0 {
		inferred = g.functionWithAnnotatedParams(d.Name, d.NameSpan, d.Params, d.Body, ty)
	} else {
		inferred = g.expr(d.Body)
		g.cs = append(g.cs, Constraint{Left: g.ambient, Right: types.Row{}, Span: d.Body.Span(), Why: Why{Kind: WhyEffectEscapes}})
	}
	g.cs = append(g.cs, Constraint{Left: inferred, Right: ty, Span: d.NameSpan, Why: Why{Kind: WhyAnnotation, Name: types.SurfaceName(d.Name)}})
	sub, _, errs := Solve(g.cs, nil, ck.Sub, ck.B, ck.Sup)
	ck.Sub = sub
	errs = append(errs, g.errs...)
	g.errs = nil
	g.resolveRecords(true)
	errs = append(errs, g.errs...)
	sch, es := ck.qualify(ck.generalize(ty, nil), g.preds, nil, true, d.NameSpan)
	errs = append(errs, es...)
	return DeclInfo{Name: d.Name, NameSpan: d.NameSpan, Params: d.Params, Type: ty, Body: d.Body, Scheme: sch, InstanceLimit: len(ck.Instances)}, errs
}

func (ck *Checker) NormalizePreds(ps []types.Pred) []types.Pred {
	var out []types.Pred
	for _, p := range ps {
		p.Ty = ck.Sub.Apply(p.Ty)
		dup := false
		for _, q := range out {
			if p.Class == q.Class && types.Equal(p.Ty, q.Ty) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Class < out[j].Class })
	return out
}

func (ck *Checker) reduceObligations(obs []predObligation, given []types.Pred) ([]types.Pred, []diag.Error) {
	var residual []types.Pred
	var errs []diag.Error
	if in := ck.checkingInstance; in != nil {
		given = append(append([]types.Pred{}, given...), types.Pred{Class: in.Class.Name, Ty: in.Head})
	}
	for _, o := range obs {
		r := ck.ResolveInstance(o.pred, ck.CurrentOwner, len(ck.Instances), given)
		if r.Blocked {
			residual = append(residual, o.pred)
		}
		if r.Error != nil {
			err := *r.Error
			err.Span = o.span
			errs = append(errs, err)
		}
	}
	return ck.NormalizePreds(residual), errs
}

func (ck *Checker) DefaultPreds(ps []types.Pred, sp source.Span) []diag.Error {
	for id := range ck.numericDefaults(ps) {
		ck.Sub[id] = ck.B.Int
	}
	var obs []predObligation
	for _, p := range ps {
		obs = append(obs, predObligation{pred: p, span: sp})
	}
	left, errs := ck.reduceObligations(obs, nil)
	for _, p := range left {
		errs = append(errs, diag.Errorf(sp, "AMBIGUOUS CONSTRAINT", "Cannot determine the type for `%s %s`.", types.SurfaceName(p.Class), types.Show(p.Ty)))
	}
	return errs
}

func (ck *Checker) StandardPred(name string, t types.Type) types.Pred {
	return types.Pred{Class: "Basics." + name, Ty: t}
}

func (ck *Checker) qualify(sch types.Scheme, obs []predObligation, given []types.Pred, annotated bool, sp source.Span) (types.Scheme, []diag.Error) {
	left, errs := ck.reduceObligations(obs, given)
	quant := map[int]bool{}
	for _, v := range sch.Vars {
		quant[v.ID] = true
	}
	var ground []types.Pred
	for _, p := range left {
		if mentionsAny(p.Ty, quant) {
			if ck.inferringContext != nil {
				// A generated instance adopts what its body actually needs
				// rather than declaring a context up front.
				*ck.inferringContext = append(*ck.inferringContext, p)
			} else if annotated {
				errs = append(errs, diag.Errorf(sp, "MISSING CONSTRAINT", "The annotation requires the additional constraint `%s %s`.", types.SurfaceName(p.Class), types.Show(p.Ty)))
			} else {
				sch.Preds = append(sch.Preds, p)
			}
		} else {
			ground = append(ground, p)
		}
	}
	if len(ground) > 0 {
		errs = append(errs, ck.DefaultPreds(ground, sp)...)
	}
	if annotated {
		sch.Preds = given
	}
	ids := map[int]bool{}
	collectVarIDs(ck.Sub.Apply(sch.Body), ids)
	for _, p := range sch.Preds {
		mentioned := map[int]bool{}
		collectVarIDs(ck.Sub.Apply(p.Ty), mentioned)
		for id := range mentioned {
			if !ids[id] {
				errs = append(errs, diag.Errorf(sp, "AMBIGUOUS CONSTRAINT", "A constraint mentions a variable absent from the annotated type."))
				break
			}
		}
	}
	sch.Preds = ck.NormalizePreds(sch.Preds)
	sch.Body = ck.Sub.Apply(sch.Body)
	return sch, errs
}

func (ck *Checker) CanResolve(p types.Pred, owner string) bool {
	r := ck.ResolveInstance(p, owner, len(ck.Instances), nil)
	return !r.Blocked && r.Error == nil
}

func hasTypeVars(t types.Type) bool {
	ids := map[int]bool{}
	collectVarIDs(t, ids)
	return len(ids) != 0
}

func mentionsAny(t types.Type, vars map[int]bool) bool {
	ids := map[int]bool{}
	collectVarIDs(t, ids)
	for id := range ids {
		if vars[id] {
			return true
		}
	}
	return false
}

// ResolutionPathError bounds all recursive evidence consumers. Only active
// ancestors count as cycles: sibling requirements may legitimately repeat.
func ResolutionPathError(path []types.Pred, p types.Pred, sp source.Span) *diag.Error {
	for i, q := range path {
		if p.Class == q.Class && types.Equal(p.Ty, q.Ty) {
			var names []string
			for _, r := range append(append([]types.Pred{}, path[i:]...), p) {
				names = append(names, types.SurfaceName(r.Class)+" "+types.Show(r.Ty))
			}
			err := diag.Errorf(sp, "INSTANCE RESOLUTION", "Circular instance requirements: %s.", strings.Join(names, " -> "))
			return &err
		}
	}
	if len(path) > 100 {
		err := diag.Errorf(sp, "INSTANCE RESOLUTION", "Instance resolution exceeded its nesting limit.")
		return &err
	}
	return nil
}

func (ck *Checker) blanketCycle(class string, ps []types.Pred) []string {
	edges := map[string][]types.Pred{}
	for _, in := range ck.Instances {
		if _, ok := in.Head.(*types.TVar); ok {
			edges[in.Class.Name] = append(edges[in.Class.Name], in.Preds...)
		}
	}
	edges[class] = append(edges[class], ps...)
	done := map[string]bool{}
	var visit func(string, []string) []string
	visit = func(n string, path []string) []string {
		for i, old := range path {
			if old == n {
				cycle := append(append([]string{}, path[i:]...), n)
				for j := range cycle {
					cycle[j] = types.SurfaceName(cycle[j])
				}
				return cycle
			}
		}
		if done[n] {
			return nil
		}
		for _, p := range edges[n] {
			if cycle := visit(p.Class, append(path, n)); cycle != nil {
				return cycle
			}
		}
		done[n] = true
		return nil
	}
	return visit(class, nil)
}

// numericDefaults tests alternative context expansions independently for each
// metavariable. It discovers eligibility, not evidence: real selection runs
// again after the eligible variables have defaulted to Int.
func (ck *Checker) numericDefaults(ps []types.Pred) map[int]bool {
	vars := map[int]types.Type{}
	for _, p := range ps {
		collectVariables(ck.Sub.Apply(p.Ty), vars)
	}
	result := map[int]bool{}
	for id, raw := range vars {
		if raw.(*types.TVar).Rigid {
			continue
		}
		numeric, compatible := false, true
		for _, p := range ps {
			ok, num := ck.defaultEligible(p, id, nil)
			compatible = compatible && ok
			numeric = numeric || num
		}
		if compatible && numeric {
			result[id] = true
		}
	}
	return result
}

// defaultEligible returns whether an expansion can avoid unsupported bare
// constraints on id, and whether such an expansion can require Num id.
// Keeping these two bits avoids materializing a Cartesian product of all
// candidate contexts.
func (ck *Checker) defaultEligible(p types.Pred, id int, path []types.Pred) (bool, bool) {
	p.Ty = ck.Sub.Apply(p.Ty)
	if !hasTypeVars(p.Ty) {
		return true, false
	}
	if v, ok := p.Ty.(*types.TVar); ok {
		if v.ID != id {
			return true, false
		}
		switch p.Class {
		case "Basics.Num":
			return true, true
		case "Basics.Eq", "Basics.Ord", "Basics.Show":
			return true, false
		}
	}
	if ResolutionPathError(path, p, source.Span{}) != nil {
		return false, false
	}
	variables := map[int]types.Type{}
	collectVariables(p.Ty, variables)
	sub := Subst{}
	for vid, raw := range variables {
		v := *raw.(*types.TVar)
		v.Rigid = true
		sub[vid] = &v
	}
	probe := types.Pred{Class: p.Class, Ty: sub.Apply(p.Ty)}
	candidates := ck.matchingInstances(probe, ck.CurrentOwner, len(ck.Instances))
	if len(candidates) == 0 {
		_, bare := p.Ty.(*types.TVar)
		return !bare, false // Preserve deferred structural defaulting.
	}
	anyOK, anyNumeric := false, false
	for _, c := range candidates {
		ok, num := true, false
		for _, q := range types.SubstPreds(c.in.Preds, c.bindings) {
			q.Ty = types.SubstRigid(q.Ty, variables)
			childOK, childNum := ck.defaultEligible(q, id, append(path, p))
			ok = ok && childOK
			num = num || childNum
		}
		anyOK = anyOK || ok
		anyNumeric = anyNumeric || (ok && num)
	}
	return anyOK, anyNumeric
}

func collectVariables(t types.Type, out map[int]types.Type) {
	switch t := t.(type) {
	case *types.TVar:
		out[t.ID] = t
	case *types.TCon:
		for _, a := range t.Args {
			collectVariables(a, out)
		}
	case *types.TFun:
		collectVariables(t.Arg, out)
		collectVariables(t.Eff, out)
		collectVariables(t.Ret, out)
	case types.Row:
		for _, l := range t.Labels {
			for _, a := range l.Args {
				collectVariables(a, out)
			}
		}
		if t.Tail != nil {
			collectVariables(t.Tail, out)
		}
	}
}
