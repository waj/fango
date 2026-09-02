package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
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
		return freeGeneral(t.Ret)
	default:
		return nil
	}
}

// TypeVars scopes the type variables of one type expression. Annotations use
// an open scope: the first use of a name mints a fresh rigid skolem (§7.2).
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
		if !ck.AllowPoly {
			return nil, []diag.Error{diag.Errorf(te.Sp, "UNSUPPORTED ANNOTATION",
				"Type variables in annotations arrive with polymorphism (S5).\nFor now annotations must be concrete: Int, Float, String, Bool, ().")}
		}
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
		return &types.TFun{Arg: arg, Eff: types.Row{}, Ret: ret}, errs
	case *ast.TApp:
		if !ck.AllowPoly {
			// Every type in scope is arity 0 until S5, so any application is an
			// arity error (a known name) or a naming error.
			if _, ok := ck.TypeNames[te.Name]; ok {
				return nil, []diag.Error{diag.Errorf(te.Span(), "TYPE ARITY",
					"`%s` is not a parameterized type, but it is applied to %d type\nargument(s) here. Parameterized types arrive with polymorphism (S5).",
					te.Name, len(te.Args))}
			}
			return nil, []diag.Error{diag.Errorf(te.NameSp, "NAMING ERROR",
				"I don't know a type named `%s`.", te.Name)}
		}
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
// Feeds the `==`-at-function-types rejection (§8.6).
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
