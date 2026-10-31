package infer

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

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
// Constructor fields extend their declaration parameters with ordinary
// constructor-local hidden binders.
type TypeVars struct {
	hidden    bool
	scopedRow string // the declaration's `#s` binder, if any
	open      bool
	sup       *types.Supply
	vars      map[string]*types.TVar
	minted    []*types.TVar // open scopes: skolems in first-use order
	preds     []types.Pred
	native    bool
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

func (ck *Checker) newDeclAnnScope(d *ast.ValueDecl) *TypeVars {
	tv := ck.NewAnnScope()
	tv.scopedRow = d.ScopedRow
	return tv
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

// unknownType reports a name that is no type here, recognizing an effect used
// as one and a likely misspelling.
func (ck *Checker) unknownType(sp source.Span, name string) diag.Error {
	surface := types.SurfaceName(name)
	if ck.Effects[name] != nil {
		e := diag.Errorf(sp, "NAMING ERROR", "`%s` is an effect, not a type.", surface)
		e.Notes = append(e.Notes, fmt.Sprintf("Effects go on an arrow: `A ->{%s} B`.", surface))
		return e
	}
	if ck.EffectAliases[name] != nil {
		e := diag.Errorf(sp, "NAMING ERROR", "`%s` is an effect alias, not a type.", surface)
		e.Notes = append(e.Notes, fmt.Sprintf("Effects go on an arrow: `A ->{%s} B`. Another alias of them is written `alias Name = {%s}`.", surface, surface))
		return e
	}
	e := diag.Errorf(sp, "NAMING ERROR", "I don't know a type named `%s`.", surface)
	if hint := diag.DidYouMean(surface, surfaceKeys(ck.TypeNames)); hint != "" {
		e.Notes = append(e.Notes, hint)
	}
	return e
}

// unknownEffect reports a name that is no effect here, recognizing one that
// exists but was not exposed by its import, and a likely misspelling.
func (ck *Checker) unknownEffect(sp source.Span, name string) diag.Error {
	e := diag.Errorf(sp, "NAMING ERROR", "I don't know an effect named `%s`.", name)
	for canonical := range ck.Effects {
		if module, found := strings.CutSuffix(canonical, "."+name); found {
			e.Notes = append(e.Notes, fmt.Sprintf("`%s` has one: write `%s`, or `import %s exposing (%s)`.", module, canonical, module, name))
			return e
		}
	}
	var names []string
	for canonical := range ck.Effects {
		names = append(names, types.SurfaceName(canonical))
	}
	if hint := diag.DidYouMean(name, names); hint != "" {
		e.Notes = append(e.Notes, hint)
	}
	return e
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
		if want == types.RowVar || want == kindAny && ck.namesRow(te.Name) {
			if effect := ck.Effects[te.Name]; effect != nil && len(effect.Params) == 0 {
				return types.Row{Labels: []types.EffLabel{{Unique: effect.Unique, Name: effect.Name}}}, nil
			}
			if alias := ck.EffectAliases[te.Name]; alias != nil {
				if len(alias.Params) > 0 {
					return nil, []diag.Error{diag.Errorf(te.Sp, "EFFECT ARITY", "`%s` takes %d type argument(s), but none are given here.", types.SurfaceName(te.Name), len(alias.Params))}
				}
				return types.Row{Labels: ck.expandEffectAlias(alias, nil)}, nil
			}
			return nil, []diag.Error{diag.Errorf(te.Sp, "KIND MISMATCH", "A row-kinded parameter must be an effect-row variable or effect label, not the type `%s`.", te.Name)}
		}
		if alias := ck.TypeAliases[te.Name]; alias != nil {
			if len(alias.Params) > 0 {
				return nil, []diag.Error{diag.Errorf(te.Sp, "TYPE ARITY",
					"`%s` takes %d type argument(s), but none are given here.", types.SurfaceName(te.Name), len(alias.Params))}
			}
			return alias.Body, nil
		}
		t, ok := ck.TypeNames[te.Name]
		if !ok {
			return nil, []diag.Error{ck.unknownType(te.Sp, te.Name)}
		}
		if arity := ck.typeArity(t); arity > 0 {
			return nil, []diag.Error{diag.Errorf(te.Sp, "TYPE ARITY",
				"`%s` takes %d type argument(s), but none are given here.", types.SurfaceName(te.Name), arity)}
		}
		return t, nil
	case *ast.TVarName:
		if v, ok := tv.vars[te.Name]; ok {
			if tv.hidden && want == types.RowVar && tv.isMinted(v) {
				return nil, []diag.Error{diag.Errorf(te.Sp, "HIDDEN EFFECT ROW", "The effect row `%s` must be declared in the type parameters.", te.Name)}
			}
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
		if tv.hidden && want == types.RowVar {
			return nil, []diag.Error{diag.Errorf(te.Sp, "HIDDEN EFFECT ROW", "The effect row `%s` must be declared in the type's parameters. Only ordinary types can be hidden in a constructor.", te.Name)}
		}
		v := tv.sup.FreshRigid(types.General)
		v.Name = te.Name
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
		if want == types.RowVar || want == kindAny && ck.namesRow(te.Name) {
			if alias := ck.EffectAliases[te.Name]; alias != nil {
				if len(alias.Params) != len(te.Args) {
					return nil, []diag.Error{diag.Errorf(te.Span(), "EFFECT ARITY", "`%s` takes %d type argument(s), but %d are given.", types.SurfaceName(te.Name), len(alias.Params), len(te.Args))}
				}
				args, errs := ck.resolveAliasArgs(alias.Params, te.Args, tv)
				if args == nil {
					return nil, errs
				}
				return types.Row{Labels: ck.expandEffectAlias(alias, args)}, errs
			}
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
		if alias := ck.TypeAliases[te.Name]; alias != nil {
			if len(alias.Params) != len(te.Args) {
				return nil, []diag.Error{diag.Errorf(te.Span(), "TYPE ARITY",
					"`%s` takes %d type argument(s), but %d are given here.", types.SurfaceName(te.Name), len(alias.Params), len(te.Args))}
			}
			args, errs := ck.resolveAliasArgs(alias.Params, te.Args, tv)
			if args == nil {
				return nil, errs
			}
			return substAlias(alias.Body, alias.Params, args), errs
		}
		t, ok := ck.TypeNames[te.Name]
		if !ok {
			return nil, []diag.Error{ck.unknownType(te.NameSp, te.Name)}
		}
		con, ok := t.(*types.TCon)
		if !ok || ck.typeArity(t) != len(te.Args) {
			return nil, []diag.Error{diag.Errorf(te.Span(), "TYPE ARITY",
				"`%s` takes %d type argument(s), but %d are given here.",
				types.SurfaceName(te.Name), ck.typeArity(t), len(te.Args))}
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

// namesRow reports whether a name can only be read as an effect row, which
// settles it even where the parameter it fills has no known kind yet.
func (ck *Checker) namesRow(name string) bool {
	return ck.Effects[name] != nil || ck.EffectAliases[name] != nil
}

// resolveAliasArgs resolves an alias application's arguments at the kinds of
// its parameters. While a module's declarations are still being checked, a
// parameter that is not yet known to be a row may become one through a type
// whose constructors come later, so its argument waits for settleKinds.
func (ck *Checker) resolveAliasArgs(params []*types.TVar, exprs []ast.TypeExpr, tv *TypeVars) ([]types.Type, []diag.Error) {
	args := make([]types.Type, len(exprs))
	var errs []diag.Error
	for i, a := range exprs {
		kind := params[i].Kind
		if kind != types.RowVar && ck.kindsUnsettled {
			kind = kindAny
		}
		at, aErrs := ck.resolveTypeExpr(a, tv, kind)
		errs = append(errs, aErrs...)
		if at == nil {
			return nil, errs
		}
		args[i] = at
	}
	return args, errs
}

// substAlias is an alias body at its arguments. Applying the empty substitution
// normalizes the result, since a row argument filling a tail joins that row.
func substAlias(body types.Type, params []*types.TVar, args []types.Type) types.Type {
	if len(params) == 0 {
		return body
	}
	m := make(map[int]types.Type, len(params))
	for i, p := range params {
		m[p.ID] = args[i]
	}
	return Subst(nil).Apply(types.SubstRigid(body, m))
}

// expandEffectAlias is the alias's labels at args, as a row holds them. A
// label's Abort flag is read here because an alias resolves before effect
// operations do. Arguments may make two members one, as `{Fail a, Fail b}` at
// `Int Int` does.
func (ck *Checker) expandEffectAlias(alias *types.EffectAlias, args []types.Type) []types.EffLabel {
	members := alias.Labels
	if len(alias.Params) > 0 {
		members = substAlias(types.Row{Labels: members}, alias.Params, args).(types.Row).Labels
	}
	via := []types.EffectVia{{Name: alias.Name, Args: args, Members: members}}
	labels := make([]types.EffLabel, len(members))
	for i, l := range members {
		l.Via = via
		if info := ck.EffectsByUnique[l.Unique]; info != nil {
			l.Abort = info.HasAbort()
		}
		labels[i] = l
	}
	return labels
}

// withCurrentAborts rereads the abort flag of every label in a type alias's
// body, which resolved before its module's effect operations were declared.
func (ck *Checker) withCurrentAborts(t types.Type) types.Type {
	switch t := t.(type) {
	case *types.TCon:
		if len(t.Args) == 0 {
			return t
		}
		args := make([]types.Type, len(t.Args))
		for i, a := range t.Args {
			args[i] = ck.withCurrentAborts(a)
		}
		return &types.TCon{Unique: t.Unique, Name: t.Name, Args: args}
	case *types.TFun:
		return &types.TFun{Arg: ck.withCurrentAborts(t.Arg), Eff: ck.withCurrentAborts(t.Eff).(types.Row), Ret: ck.withCurrentAborts(t.Ret), Control: t.Control, OpenRow: t.OpenRow}
	case types.Row:
		labels := make([]types.EffLabel, len(t.Labels))
		for i, l := range t.Labels {
			if info := ck.EffectsByUnique[l.Unique]; info != nil {
				l.Abort = info.HasAbort()
			}
			args := make([]types.Type, len(l.Args))
			for j, a := range l.Args {
				args[j] = ck.withCurrentAborts(a)
			}
			l.Args = args
			labels[i] = l
		}
		return types.Row{Labels: labels, Tail: t.Tail}
	default:
		return t
	}
}

// aliasReferences calls visit with each type or effect name an alias body
// mentions, which is what decides the order aliases resolve in.
func aliasReferences(te ast.TypeExpr, visit func(name string, sp source.Span)) {
	row := func(r *ast.EffRow) {
		if r == nil {
			return
		}
		for _, l := range r.Labels {
			visit(l.Name, l.NameSp)
			for _, a := range l.Args {
				aliasReferences(a, visit)
			}
		}
	}
	switch te := te.(type) {
	case *ast.TName:
		visit(te.Name, te.Sp)
	case *ast.TApp:
		visit(te.Name, te.NameSp)
		for _, a := range te.Args {
			aliasReferences(a, visit)
		}
	case *ast.TFunExpr:
		aliasReferences(te.Arg, visit)
		row(te.Eff)
		aliasReferences(te.Ret, visit)
	case *ast.TRow:
		row(te.Row)
	}
}

// declareAliases resolves `alias` declarations, each after the aliases it
// names. It runs once type and effect names exist, before anything that can
// mention an alias: operation signatures, constructor fields, and annotations.
func (ck *Checker) declareAliases(decls []*ast.AliasDecl, batch bool) []diag.Error {
	var errs []diag.Error
	pending := map[string]*ast.AliasDecl{}
	for _, d := range decls {
		exists := ck.EffectAliases[d.Name] != nil || ck.TypeAliases[d.Name] != nil
		if exists && batch || pending[d.Name] != nil {
			errs = append(errs, diag.Errorf(d.NameSpan, "MULTIPLE DEFINITIONS", "The alias `%s` is defined more than once.", types.SurfaceName(d.Name)))
		}
		pending[d.Name] = d
	}
	const visiting, done, failed = 1, 2, 3
	state := map[string]int{}
	var visit func(d *ast.AliasDecl)
	visit = func(d *ast.AliasDecl) {
		state[d.Name] = visiting
		ok := false
		defer func() {
			state[d.Name] = failed
			if ok {
				state[d.Name] = done
			}
		}()
		surface := types.SurfaceName(d.Name)
		rowBody, isRow := d.Body.(*ast.TRow)
		what, bad := "type alias", "BAD TYPE ALIAS"
		if isRow {
			what, bad = "effect alias", "BAD EFFECT ALIAS"
		}
		if _, clash := ck.Effects[d.Name]; clash {
			errs = append(errs, diag.Errorf(d.NameSpan, "MULTIPLE DEFINITIONS", "The %s `%s` collides with an effect of the same name.", what, surface))
			return
		}
		if _, clash := ck.TypeNames[d.Name]; clash {
			errs = append(errs, diag.Errorf(d.NameSpan, "MULTIPLE DEFINITIONS", "The %s `%s` collides with a type of the same name.", what, surface))
			return
		}
		if isRow {
			if rowBody.Row.Tail != "" {
				errs = append(errs, diag.Errorf(rowBody.Row.TailSp, bad, "The effect alias `%s` must be a closed row; it cannot end in the row variable `%s`.", surface, rowBody.Row.Tail))
				return
			}
			if len(rowBody.Row.Labels) == 0 {
				errs = append(errs, diag.Errorf(rowBody.Row.Sp, bad, "The effect alias `%s` must name at least one effect.", surface))
				return
			}
		}
		cyclic, blocked := false, false
		aliasReferences(d.Body, func(name string, sp source.Span) {
			dep := pending[name]
			if dep == nil || cyclic || blocked {
				return
			}
			if state[name] == visiting {
				errs = append(errs, diag.Errorf(sp, bad, "The %s `%s` cannot include itself, directly or through another alias.", what, surface))
				cyclic = true
				return
			}
			if state[name] == 0 {
				visit(dep)
			}
			blocked = state[name] == failed // already reported at its own declaration
		})
		if cyclic || blocked {
			return
		}
		seen := map[string]bool{}
		names := make([]string, len(d.Params))
		params := make([]*types.TVar, len(d.Params))
		for i, p := range d.Params {
			if seen[p.Name] {
				errs = append(errs, diag.Errorf(p.Sp, "SHADOWING", "The parameter `%s` appears twice in the alias `%s`.", p.Name, surface))
				return
			}
			seen[p.Name] = true
			names[i] = p.Name
			params[i] = ck.Sup.FreshRigid(types.General)
			params[i].Name = p.Name
		}
		scope := newCtorScope(names, params)
		var body types.Type
		var bodyErrs []diag.Error
		if isRow {
			var row types.Row
			row, bodyErrs = ck.resolveEffRow(rowBody.Row, scope)
			body = row
		} else {
			body, bodyErrs = ck.resolveTypeExpr(d.Body, scope, types.General)
		}
		errs = append(errs, bodyErrs...)
		if len(bodyErrs) > 0 || body == nil {
			return
		}
		used := types.RigidVarsIn(body)
		for i, p := range params {
			if !slices.Contains(used, p) {
				errs = append(errs, diag.Errorf(d.Params[i].Sp, bad, "The %s `%s` never uses its parameter `%s`.", what, surface, p.Name))
				return
			}
		}
		if isRow {
			row := body.(types.Row)
			labels := make([]types.EffLabel, len(row.Labels))
			for i, l := range row.Labels {
				l.Via = nil
				labels[i] = l
			}
			delete(ck.TypeAliases, d.Name)
			ck.EffectAliases[d.Name] = &types.EffectAlias{Name: d.Name, Params: params, Labels: labels}
		} else {
			delete(ck.EffectAliases, d.Name)
			ck.TypeAliases[d.Name] = &types.TypeAlias{Name: d.Name, Params: params, Body: body}
		}
		ok = true
	}
	for _, d := range decls {
		if state[d.Name] == 0 {
			visit(d)
		}
	}
	return errs
}

// AliasDecl declares one alias at the prompt, replacing an earlier one.
func (ck *Checker) AliasDecl(d *ast.AliasDecl) []diag.Error {
	return ck.declareAliases([]*ast.AliasDecl{d}, false)
}

// settleKinds finishes kind checking for a batch of declarations. A parameter's
// kind comes from how it is used, but a use that passes it to a type declared
// later, or through an alias of one, could not be read until every constructor
// had been: `alias Run e = Job e`. So the kinds are propagated here until they
// stop changing, and then every type the batch declared is checked against them.
func (ck *Checker) settleKinds(adts map[*ast.TypeDecl]*types.ADTInfo, aliases []*ast.AliasDecl) []diag.Error {
	type root struct {
		name   string
		sp     source.Span
		params []*types.TVar
		ty     types.Type
		kind   types.VarKind
	}
	var roots []root
	for td, adt := range adts {
		for _, c := range adt.Ctors {
			for _, f := range c.Fields {
				roots = append(roots, root{td.Name, td.NameSpan, adt.Params, f, types.General})
			}
		}
	}
	for _, d := range aliases {
		if alias := ck.TypeAliases[d.Name]; alias != nil {
			roots = append(roots, root{d.Name, d.NameSpan, alias.Params, alias.Body, types.General})
		} else if alias := ck.EffectAliases[d.Name]; alias != nil {
			roots = append(roots, root{d.Name, d.NameSpan, alias.Params, types.Row{Labels: alias.Labels}, types.RowVar})
		}
	}
	slices.SortStableFunc(roots, func(a, b root) int { return cmp.Compare(a.name, b.name) })
	for changed := true; changed; {
		changed = false
		for _, r := range roots {
			walkTypeCons(r.ty, func(con *types.TCon, adt *types.ADTInfo) {
				for i, a := range con.Args {
					if v, ok := a.(*types.TVar); ok && i < len(adt.Params) && adt.Params[i].Kind == types.RowVar && v.Kind != types.RowVar && slices.Contains(r.params, v) {
						v.Kind = types.RowVar
						changed = true
					}
				}
			}, ck.ADTs)
		}
	}
	var errs []diag.Error
	reported := map[string]bool{}
	for _, r := range roots {
		if text := ck.kindProblem(r.ty, r.kind); text != "" && !reported[r.name] {
			reported[r.name] = true
			errs = append(errs, diag.Errorf(r.sp, "KIND MISMATCH", "`%s` %s", types.SurfaceName(r.name), text))
		}
	}
	return errs
}

func walkTypeCons(t types.Type, visit func(*types.TCon, *types.ADTInfo), adts map[int]*types.ADTInfo) {
	switch t := t.(type) {
	case *types.TCon:
		if adt := adts[t.Unique]; adt != nil {
			visit(t, adt)
		}
		for _, a := range t.Args {
			walkTypeCons(a, visit, adts)
		}
	case *types.TFun:
		walkTypeCons(t.Arg, visit, adts)
		walkTypeCons(t.Eff, visit, adts)
		walkTypeCons(t.Ret, visit, adts)
	case types.Row:
		for _, l := range t.Labels {
			for _, a := range l.Args {
				walkTypeCons(a, visit, adts)
			}
		}
		if t.Tail != nil {
			walkTypeCons(t.Tail, visit, adts)
		}
	}
}

// kindProblem describes where t breaks the kinds that have settled, or is empty.
func (ck *Checker) kindProblem(t types.Type, want types.VarKind) string {
	rowWanted := "gives an ordinary type `%s` where an effect row is needed."
	switch t := t.(type) {
	case *types.TVar:
		if t.Kind != want {
			if want == types.RowVar {
				return fmt.Sprintf(rowWanted, types.NewPrinter().Type(t))
			}
			return fmt.Sprintf("uses `%s` both as an ordinary type and as an effect row.", types.NewPrinter().Type(t))
		}
	case *types.TCon:
		if want == types.RowVar {
			return fmt.Sprintf(rowWanted, types.NewPrinter().Type(t))
		}
		adt := ck.ADTs[t.Unique]
		for i, a := range t.Args {
			kind := types.General
			if adt != nil && i < len(adt.Params) {
				kind = adt.Params[i].Kind
			}
			if text := ck.kindProblem(a, kind); text != "" {
				return text
			}
		}
	case *types.TFun:
		if want == types.RowVar {
			return fmt.Sprintf(rowWanted, types.NewPrinter().Type(t))
		}
		for _, part := range []struct {
			ty   types.Type
			kind types.VarKind
		}{{t.Arg, types.General}, {t.Eff, types.RowVar}, {t.Ret, types.General}} {
			if text := ck.kindProblem(part.ty, part.kind); text != "" {
				return text
			}
		}
	case types.Row:
		if want != types.RowVar && len(t.Labels) > 0 {
			return fmt.Sprintf("gives the effect row `%s` where an ordinary type is needed.", types.NewPrinter().Type(t))
		}
		for _, l := range t.Labels {
			for _, a := range l.Args {
				if text := ck.kindProblem(a, types.General); text != "" {
					return text
				}
			}
		}
		if t.Tail != nil {
			return ck.kindProblem(t.Tail, types.RowVar)
		}
	}
	return ""
}

func (ck *Checker) resolveEffRow(row *ast.EffRow, tv *TypeVars) (types.Row, []diag.Error) {
	if row == nil {
		return types.Row{}, nil
	}
	if row.Scoped && row.Tail != tv.scopedRow {
		return types.Row{}, []diag.Error{diag.Errorf(row.TailSp, "SCOPED CALLBACK", "A scoped row variable like `#%s` belongs only to the callback of an annotated top-level runner, `run : (Handle {#%s} ->{#%s} a) ->{e} a`; it cannot appear in this type.", row.Tail, row.Tail, row.Tail)}
	}
	var result types.Row
	var errs []diag.Error
	seen := map[types.EffectKey]int{}
	for _, l := range row.Labels {
		if alias := ck.EffectAliases[l.Name]; alias != nil {
			if len(l.Args) != len(alias.Params) {
				if len(alias.Params) == 0 {
					errs = append(errs, diag.Errorf(l.NameSp, "EFFECT ARITY", "The effect alias `%s` takes no type arguments.", types.SurfaceName(l.Name)))
				} else {
					errs = append(errs, diag.Errorf(l.NameSp, "EFFECT ARITY", "`%s` takes %d type argument(s), but %d are given.", types.SurfaceName(l.Name), len(alias.Params), len(l.Args)))
				}
				continue
			}
			aliasArgs, argErrs := ck.resolveAliasArgs(alias.Params, l.Args, tv)
			errs = append(errs, argErrs...)
			if aliasArgs == nil && len(alias.Params) > 0 {
				continue
			}
			for _, label := range ck.expandEffectAlias(alias, aliasArgs) {
				key := types.EffectLabelKey(label)
				at, dup := seen[key]
				switch {
				case !dup:
					seen[key] = len(result.Labels)
					result.Labels = append(result.Labels, label)
				case len(result.Labels[at].Via) == 0:
					errs = append(errs, diag.Errorf(l.NameSp, "DUPLICATE EFFECT", "The effect application `%s` is already in this row, through the alias `%s`.", types.SurfaceName(label.Name), types.SurfaceName(l.Name)))
					result.Labels[at].Via = label.Via // the report is enough; the row need not also be exact
				default:
					result.Labels[at].Via = types.MergeVia(result.Labels[at].Via, label.Via)
				}
			}
			continue
		}
		info := ck.Effects[l.Name]
		if info == nil {
			errs = append(errs, ck.unknownEffect(l.NameSp, l.Name))
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
		if at, dup := seen[key]; dup {
			if via := result.Labels[at].Via; len(via) > 0 {
				errs = append(errs, diag.Errorf(l.NameSp, "DUPLICATE EFFECT", "The effect application `%s` is already in this row, through the alias `%s`.", l.Name, types.SurfaceName(via[0].Name)))
			} else {
				errs = append(errs, diag.Errorf(l.NameSp, "DUPLICATE EFFECT", "The effect application `%s` appears twice in this row.", l.Name))
			}
			continue
		}
		seen[key] = len(result.Labels)
		result.Labels = append(result.Labels, types.EffLabel{Unique: info.Unique, Name: info.Name, Args: args, Abort: info.HasAbort()})
	}
	if row.Tail != "" {
		if old, ok := tv.vars[row.Tail]; ok {
			if tv.hidden && tv.isMinted(old) {
				return result, []diag.Error{diag.Errorf(row.TailSp, "HIDDEN EFFECT ROW", "The effect row `%s` must be declared in the type parameters.", row.Tail)}
			}
			if prior, used := tv.kinds[old.ID]; used && prior != types.RowVar {
				errs = append(errs, diag.Errorf(row.TailSp, "KIND MISMATCH", "`%s` is already used as an ordinary type variable, not an effect row.", row.Tail))
			} else {
				tv.kinds[old.ID] = types.RowVar
				old.Kind = types.RowVar
				result.Tail = old
			}
		} else if tv.hidden {
			errs = append(errs, diag.Errorf(row.TailSp, "HIDDEN EFFECT ROW", "The effect row `%s` must be declared in the type's parameters. Only ordinary types can be hidden in a constructor.", row.Tail))
		} else if !tv.open {
			errs = append(errs, diag.Errorf(row.TailSp, "NAMING ERROR", "The row variable `%s` is not declared here.", row.Tail))
		} else {
			v := tv.sup.FreshRigid(types.RowVar)
			v.Name = row.Tail
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

func (tv *TypeVars) isMinted(v *types.TVar) bool {
	for _, own := range tv.minted {
		if own.ID == v.ID {
			return true
		}
	}
	return false
}
