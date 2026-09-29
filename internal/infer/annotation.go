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
	preds  []types.Pred
	native bool
	// kinds records the first required kind for each binder in this scope.
	// ADT parameters start as ordinary variables and are promoted to RowVar
	// when used as an effect-row tail; a second, incompatible use is a
	// source-level kind error.
	kinds map[int]types.VarKind
}

// kindAny is used while resolving a recursive reference to an ADT whose
// parameter kinds have not yet been established by its constructor fields.
const kindAny types.VarKind = -1

func (ck *Checker) newNativeAnnScope() *TypeVars {
	return &TypeVars{open: true, sup: ck.Sup, vars: map[string]*types.TVar{}, kinds: map[int]types.VarKind{}, native: true}
}
func (tv *TypeVars) Preds() []types.Pred { return append([]types.Pred(nil), tv.preds...) }

// NewAnnScope is the open scope for one annotation; variables of the same
// name within the annotation share one skolem.
func (ck *Checker) NewAnnScope() *TypeVars {
	return &TypeVars{open: true, sup: ck.Sup, vars: map[string]*types.TVar{}, kinds: map[int]types.VarKind{}}
}

// newCtorScope is the closed scope of a type declaration's parameters.
func newCtorScope(names []string, vars []*types.TVar) *TypeVars {
	m := make(map[string]*types.TVar, len(names))
	for i, n := range names {
		m[n] = vars[i]
	}
	kinds := map[int]types.VarKind{}
	return &TypeVars{vars: m, kinds: kinds}
}

func newEffectScope(names []string, vars []*types.TVar, sup *types.Supply) *TypeVars {
	tv := newCtorScope(names, vars)
	tv.open = true
	tv.sup = sup
	for _, v := range vars {
		tv.kinds[v.ID] = types.General
	}
	return tv
}

// Minted returns the skolems an open scope created, in first-use order.
func (tv *TypeVars) Minted() []*types.TVar { return tv.minted }

// ResolveTypeExpr converts a surface type expression into a checker type via
// the session's type-name table, resolving type variables in tv. Returns nil
// (with diagnostics) if any part fails to resolve.
func (ck *Checker) ResolveTypeExpr(te ast.TypeExpr, tv *TypeVars) (types.Type, []diag.Error) {
	return ck.resolveTypeExpr(te, tv, types.General)
}

func (ck *Checker) resolveTypeExpr(te ast.TypeExpr, tv *TypeVars, want types.VarKind) (types.Type, []diag.Error) {
	switch te := te.(type) {
	case *ast.TRow:
		// A row literal has effect-row kind and no other reading, so it is
		// accepted wherever the parameter it fills is row-kinded and refused
		// everywhere else. kindAny is a parameter whose kind the declaration
		// has not fixed yet, which a row is still the only reading of.
		if want != types.RowVar && want != kindAny {
			return nil, []diag.Error{diag.Errorf(te.Span(), "KIND MISMATCH",
				"An effect row has effect-row kind, not ordinary type kind.")}
		}
		return ck.resolveEffRow(te.Row, tv)
	case *ast.TName:
		if want == types.RowVar {
			if effect := ck.Effects[te.Name]; effect != nil && len(effect.Params) == 0 {
				return types.Row{Labels: []types.EffLabel{{Unique: effect.Unique, Name: effect.Name}}}, nil
			}
			return nil, []diag.Error{diag.Errorf(te.Sp, "KIND MISMATCH", "A row-kinded parameter must be an effect-row variable or effect label, not the type `%s`.", te.Name)}
		}
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
			if want == kindAny {
				return v, nil
			}
			if prior, used := tv.kinds[v.ID]; used && prior != want {
				return nil, []diag.Error{diag.Errorf(te.Sp, "KIND MISMATCH", "The parameter `%s` is used both as an ordinary type and as an effect row.", te.Name)}
			}
			tv.kinds[v.ID] = want
			v.Kind = want
			return v, nil
		}
		if !tv.open {
			return nil, []diag.Error{diag.Errorf(te.Sp, "NAMING ERROR",
				"The type variable `%s` is not declared by this type's parameters.", te.Name)}
		}
		v := tv.sup.FreshRigid(types.General)
		tv.vars[te.Name] = v
		tv.minted = append(tv.minted, v)
		if want == kindAny {
			return v, nil
		}
		if prior, ok := tv.kinds[v.ID]; ok && prior != want {
			return nil, []diag.Error{diag.Errorf(te.Sp, "KIND MISMATCH", "The parameter `%s` is used both as an ordinary type and as an effect row.", te.Name)}
		}
		tv.kinds[v.ID] = want
		v.Kind = want
		return v, nil
	case *ast.TFunExpr:
		if want == types.RowVar {
			return nil, []diag.Error{diag.Errorf(te.Span(), "KIND MISMATCH", "A function type has ordinary type kind, not effect-row kind.")}
		}
		arg, argErrs := ck.resolveTypeExpr(te.Arg, tv, types.General)
		ret, retErrs := ck.resolveTypeExpr(te.Ret, tv, types.General)
		errs := append(argErrs, retErrs...)
		if arg == nil || ret == nil {
			return nil, errs
		}
		row, rowErrs := ck.resolveEffRow(te.Eff, tv)
		errs = append(errs, rowErrs...)
		return &types.TFun{Arg: arg, Eff: row, Ret: ret}, errs
	case *ast.TApp:
		if want == types.RowVar {
			if effect := ck.Effects[te.Name]; effect != nil {
				if len(effect.Params) != len(te.Args) {
					return nil, []diag.Error{diag.Errorf(te.Span(), "EFFECT ARITY", "`%s` takes %d effect parameter(s), but %d are given.", te.Name, len(effect.Params), len(te.Args))}
				}
				args := make([]types.Type, len(te.Args))
				var errs []diag.Error
				for i, a := range te.Args {
					at, aErrs := ck.resolveTypeExpr(a, tv, types.General)
					errs = append(errs, aErrs...)
					if at == nil {
						return nil, errs
					}
					args[i] = at
				}
				return types.Row{Labels: []types.EffLabel{{Unique: effect.Unique, Name: effect.Name, Args: args}}}, errs
			}
			return nil, []diag.Error{diag.Errorf(te.Span(), "KIND MISMATCH", "A row-kinded parameter must be an effect label, not a type application.")}
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
			paramKind := types.General
			if adt := ck.ADTs[con.Unique]; adt != nil && i < len(adt.Params) && (len(adt.ParamKindsKnown) == 0 || adt.ParamKindsKnown[i]) {
				paramKind = adt.Params[i].Kind
			} else if adt := ck.ADTs[con.Unique]; adt != nil && i < len(adt.Params) {
				paramKind = kindAny
			}
			at, aErrs := ck.resolveTypeExpr(a, tv, paramKind)
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

func (ck *Checker) resolveEffRow(row *ast.EffRow, tv *TypeVars) (types.Row, []diag.Error) {
	if row == nil {
		return types.Row{}, nil
	}
	var result types.Row
	var errs []diag.Error
	seen := map[types.EffectKey]bool{}
	for _, l := range row.Labels {
		info := ck.Effects[l.Name]
		if info == nil {
			errs = append(errs, diag.Errorf(l.NameSp, "NAMING ERROR", "I don't know an effect named `%s`.", l.Name))
			continue
		}
		if len(l.Args) != len(info.Params) {
			errs = append(errs, diag.Errorf(l.NameSp, "EFFECT ARITY", "`%s` takes %d type argument(s), but %d are given.", l.Name, len(info.Params), len(l.Args)))
			continue
		}
		args := make([]types.Type, len(l.Args))
		valid := true
		for i, a := range l.Args {
			at, aErrs := ck.resolveTypeExpr(a, tv, types.General)
			errs = append(errs, aErrs...)
			valid = valid && at != nil
			args[i] = at
		}
		if !valid {
			continue
		}
		key := types.AppliedEffectKey(info.Unique, args)
		if seen[key] {
			errs = append(errs, diag.Errorf(l.NameSp, "DUPLICATE EFFECT", "The effect application `%s` appears twice in this row.", l.Name))
			continue
		}
		seen[key] = true
		abort := len(info.Ops) > 0 && info.Ops[0].Abort
		result.Labels = append(result.Labels, types.EffLabel{Unique: info.Unique, Name: info.Name, Args: args, Abort: abort})
	}
	if row.Tail != "" {
		if old, ok := tv.vars[row.Tail]; ok {
			if prior, used := tv.kinds[old.ID]; used && prior != types.RowVar {
				errs = append(errs, diag.Errorf(row.TailSp, "KIND MISMATCH", "`%s` is already used as an ordinary type variable, not an effect row.", row.Tail))
			} else {
				tv.kinds[old.ID] = types.RowVar
				old.Kind = types.RowVar
				result.Tail = old
			}
		} else if !tv.open {
			errs = append(errs, diag.Errorf(row.TailSp, "NAMING ERROR", "The row variable `%s` is not declared here.", row.Tail))
		} else {
			v := tv.sup.FreshRigid(types.RowVar)
			tv.vars[row.Tail] = v
			tv.kinds[v.ID] = types.RowVar
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
