package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// FreeGeneralVar returns a General-kinded metavariable still free in t
// after applying the checker's substitution, or nil. Non-nil means the
// type is visibly polymorphic and underdetermined (Number vars are exempt:
// they default to Int; rigid vars are exempt: they are quantified, not
// free).
func (ck *Checker) FreeGeneralVar(t types.Type) *types.TVar {
	t = ck.Sub.Apply(t)
	return freeGeneral(t)
}

func freeGeneral(t types.Type) *types.TVar {
	switch t := t.(type) {
	case *types.TVar:
		if t.Kind == types.General && !t.Rigid {
			return t
		}
		return nil
	case *types.TCon:
		for _, a := range t.Args {
			if v := freeGeneral(a); v != nil {
				return v
			}
		}
		return nil
	case *types.TFun:
		if v := freeGeneral(t.Arg); v != nil {
			return v
		}
		if v := freeGeneral(t.Eff); v != nil {
			return v
		}
		return freeGeneral(t.Ret)
	case types.Row:
		for _, l := range t.Labels {
			for _, a := range l.Args {
				if v := freeGeneral(a); v != nil {
					return v
				}
			}
		}
		if t.Tail != nil {
			return freeGeneral(t.Tail)
		}
		return nil
	default:
		return nil
	}
}

// TypeVars scopes the type variables of one type expression. Annotations use
// an open scope: the first use of a name mints a fresh rigid skolem (doc/design.md, "Type inference").
// Constructor fields use a closed scope holding exactly the declaration's
// parameters — an unknown variable there is an error, not a fresh skolem.
type TypeVars struct {
	open   bool
	sup    *types.Supply
	vars   map[string]*types.TVar
	minted []*types.TVar // open scopes: skolems in first-use order
}

// NewAnnScope is the open scope for one annotation; variables of the same
// name within the annotation share one skolem.
func (ck *Checker) NewAnnScope() *TypeVars {
	return &TypeVars{open: true, sup: ck.Sup, vars: map[string]*types.TVar{}}
}

// newCtorScope is the closed scope of a type declaration's parameters.
func newCtorScope(names []string, vars []*types.TVar) *TypeVars {
	m := make(map[string]*types.TVar, len(names))
	for i, n := range names {
		m[n] = vars[i]
	}
	return &TypeVars{vars: m}
}

func newEffectScope(names []string, vars []*types.TVar, sup *types.Supply) *TypeVars {
	tv := newCtorScope(names, vars)
	tv.open = true
	tv.sup = sup
	return tv
}

// Minted returns the skolems an open scope created, in first-use order.
func (tv *TypeVars) Minted() []*types.TVar { return tv.minted }

// ResolveTypeExpr converts a surface type expression into a checker type via
// the session's type-name table, resolving type variables in tv. Returns nil
// (with diagnostics) if any part fails to resolve.
func (ck *Checker) ResolveTypeExpr(te ast.TypeExpr, tv *TypeVars) (types.Type, []diag.Error) {
	switch te := te.(type) {
	case *ast.TName:
		t, ok := ck.TypeNames[te.Name]
		if !ok {
			return nil, []diag.Error{diag.Errorf(te.Sp, "NAMING ERROR",
				"I don't know a type named `%s`.", te.Name)}
		}
		if arity := ck.typeArity(t); arity > 0 {
			return nil, []diag.Error{diag.Errorf(te.Sp, "TYPE ARITY",
				"`%s` takes %d type argument(s), but none are given here.", te.Name, arity)}
		}
		return t, nil
	case *ast.TVarName:
		if v, ok := tv.vars[te.Name]; ok {
			return v, nil
		}
		if !tv.open {
			return nil, []diag.Error{diag.Errorf(te.Sp, "NAMING ERROR",
				"The type variable `%s` is not declared by this type's parameters.", te.Name)}
		}
		v := tv.sup.FreshRigid(types.General)
		tv.vars[te.Name] = v
		tv.minted = append(tv.minted, v)
		return v, nil
	case *ast.TFunExpr:
		arg, argErrs := ck.ResolveTypeExpr(te.Arg, tv)
		ret, retErrs := ck.ResolveTypeExpr(te.Ret, tv)
		errs := append(argErrs, retErrs...)
		if arg == nil || ret == nil {
			return nil, errs
		}
		row, rowErrs := ck.resolveEffRow(te.Eff, tv)
		errs = append(errs, rowErrs...)
		// `A -> {e} B` and `A ->{e} B` normalize to the same existing
		// row-carrying arrow. A computation itself is the nullary case.
		if te.Eff == nil {
			if comp, ok := ret.(*types.TFun); ok && isUnitType(comp.Arg, ck.B) {
				return &types.TFun{Arg: arg, Eff: comp.Eff, Ret: comp.Ret}, errs
			}
		}
		return &types.TFun{Arg: arg, Eff: row, Ret: ret}, errs
	case *ast.TCompExpr:
		ret, retErrs := ck.ResolveTypeExpr(te.Ret, tv)
		row, rowErrs := ck.resolveEffRow(te.Eff, tv)
		errs := append(retErrs, rowErrs...)
		if ret == nil {
			return nil, errs
		}
		return &types.TFun{Arg: ck.B.Unit, Eff: row, Ret: ret}, errs
	case *ast.TApp:
		t, ok := ck.TypeNames[te.Name]
		if !ok {
			return nil, []diag.Error{diag.Errorf(te.NameSp, "NAMING ERROR",
				"I don't know a type named `%s`.", te.Name)}
		}
		con, ok := t.(*types.TCon)
		if !ok || ck.typeArity(t) != len(te.Args) {
			return nil, []diag.Error{diag.Errorf(te.Span(), "TYPE ARITY",
				"`%s` takes %d type argument(s), but %d are given here.",
				te.Name, ck.typeArity(t), len(te.Args))}
		}
		args := make([]types.Type, len(te.Args))
		var errs []diag.Error
		for i, a := range te.Args {
			if surfaceContainsComputation(a) {
				errs = append(errs, computationSecondClassError(a.Span(), "stored inside `"+te.Name+"`"))
				args[i] = ck.B.Unit
				continue
			}
			at, aErrs := ck.ResolveTypeExpr(a, tv)
			errs = append(errs, aErrs...)
			if at == nil {
				return nil, errs
			}
			args[i] = at
		}
		return &types.TCon{Unique: con.Unique, Name: con.Name, Args: args}, errs
	default:
		panic("infer: unhandled TypeExpr node")
	}
}

func isUnitType(t types.Type, b *types.Builtins) bool {
	c, ok := t.(*types.TCon)
	return ok && c.Unique == b.Unit.Unique
}

func surfaceContainsComputation(te ast.TypeExpr) bool {
	switch te := te.(type) {
	case *ast.TCompExpr:
		return true
	case *ast.TFunExpr:
		return surfaceContainsComputation(te.Arg) || surfaceContainsComputation(te.Ret)
	case *ast.TApp:
		for _, a := range te.Args {
			if surfaceContainsComputation(a) {
				return true
			}
		}
	}
	return false
}

func surfaceMentionsComputation(te ast.TypeExpr) bool {
	switch te := te.(type) {
	case *ast.TCompExpr:
		return true
	case *ast.TFunExpr:
		if u, ok := te.Arg.(*ast.TName); ok && u.Name == "()" && te.Eff != nil {
			return true
		}
		return surfaceMentionsComputation(te.Arg) || surfaceMentionsComputation(te.Ret)
	case *ast.TApp:
		for _, a := range te.Args {
			if surfaceMentionsComputation(a) {
				return true
			}
		}
	}
	return false
}

func computationSecondClassError(sp source.Span, where string) diag.Error {
	return diag.Errorf(sp, "SECOND-CLASS COMPUTATION",
		"A computation type cannot be %s. Computations may only be binding, parameter, or return types (Rule 4).", where)
}

func (ck *Checker) resolveEffRow(row *ast.EffRow, tv *TypeVars) (types.Row, []diag.Error) {
	if row == nil {
		return types.Row{}, nil
	}
	var result types.Row
	var errs []diag.Error
	seen := map[int]bool{}
	for _, l := range row.Labels {
		info := ck.Effects[l.Name]
		if info == nil {
			errs = append(errs, diag.Errorf(l.NameSp, "NAMING ERROR", "I don't know an effect named `%s`.", l.Name))
			continue
		}
		if seen[info.Unique] {
			errs = append(errs, diag.Errorf(l.NameSp, "DUPLICATE EFFECT", "The effect `%s` appears twice in this row; effect labels are distinct.", l.Name))
			continue
		}
		seen[info.Unique] = true
		if len(l.Args) != len(info.Params) {
			errs = append(errs, diag.Errorf(l.NameSp, "EFFECT ARITY", "`%s` takes %d type argument(s), but %d are given.", l.Name, len(info.Params), len(l.Args)))
			continue
		}
		args := make([]types.Type, len(l.Args))
		for i, a := range l.Args {
			if surfaceContainsComputation(a) {
				errs = append(errs, computationSecondClassError(a.Span(), "used to instantiate effect parameter `"+l.Name+"`"))
				args[i] = ck.B.Unit
				continue
			}
			at, aErrs := ck.ResolveTypeExpr(a, tv)
			errs = append(errs, aErrs...)
			args[i] = at
		}
		result.Labels = append(result.Labels, types.EffLabel{Unique: info.Unique, Name: info.Name, Args: args})
	}
	if row.Tail != "" {
		if old, ok := tv.vars[row.Tail]; ok {
			if old.Kind != types.RowVar {
				errs = append(errs, diag.Errorf(row.TailSp, "KIND MISMATCH", "`%s` is already used as an ordinary type variable, not an effect row.", row.Tail))
			} else {
				result.Tail = old
			}
		} else if !tv.open {
			errs = append(errs, diag.Errorf(row.TailSp, "NAMING ERROR", "The row variable `%s` is not declared here.", row.Tail))
		} else {
			v := tv.sup.FreshRigid(types.RowVar)
			tv.vars[row.Tail] = v
			tv.minted = append(tv.minted, v)
			result.Tail = v
		}
	}
	return types.SortedRow(result), errs
}

// typeArity is the declared parameter count of a named type (0 for scalars
// and monomorphic ADTs).
func (ck *Checker) typeArity(t types.Type) int {
	con, ok := t.(*types.TCon)
	if !ok {
		return 0
	}
	if adt, ok := ck.ADTs[con.Unique]; ok {
		return len(adt.Params)
	}
	return 0
}

// ContainsFunction reports whether t transitively contains a function type —
// through ADT fields too, instantiated at the occurrence's type arguments.
// Feeds the `==`-at-function-types rejection (doc/design.md, "Go backend and runtime").
func (ck *Checker) ContainsFunction(t types.Type) bool {
	return ck.containsFunction(t, map[int]bool{})
}

func (ck *Checker) containsFunction(t types.Type, visiting map[int]bool) bool {
	switch t := t.(type) {
	case *types.TFun:
		return true
	case *types.TCon:
		if visiting[t.Unique] {
			return false // recursive occurrence: already being examined
		}
		visiting[t.Unique] = true
		if adt, ok := ck.ADTs[t.Unique]; ok {
			m := adt.ParamSubst(t.Args)
			for _, c := range adt.Ctors {
				for _, f := range c.Fields {
					if ck.containsFunction(types.SubstRigid(f, m), visiting) {
						return true
					}
				}
			}
		}
		return false
	default:
		return false
	}
}
