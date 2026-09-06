// Package elaborate bridges the typed AST and Core: zonking (fully applying
// the solved substitution), defaulting residual metavariables (Number →
// Int, General → Unit), the post-defaulting ground checks that Elm's
// `comparable` kind flag would otherwise do (equatable/orderable/printable),
// and constant folding. Lambda-lifting, saturation analysis, and decision
// trees are implemented by the focused helpers in lift.go and match.go.
//
// Constant folding here is a correctness requirement, not an optimization:
// Go evaluates constant expressions exactly (arbitrary precision), so
// emitting literal arithmetic verbatim would make `1.0 / 0.0` a Go compile
// error, round `0.1 + 0.2` differently than the runtime, and overflow on
// `9223372036854775807 + 1`. Folding uses the same Go int64/float64
// operations the interpreter performs, so both backends agree by
// construction.
package elaborate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/natives"
	"github.com/waj/fango/internal/types"
)

// Module elaborates checked declarations into a Core program. After it
// returns without errors, Core contains no metavariables — asserted by
// core.Lint.
func Module(infos []infer.DeclInfo, ck *infer.Checker) (*core.Prog, []diag.Error) {
	infos = append(append([]infer.DeclInfo{}, ck.PreludeInfos...), infos...)
	effects := make([]*types.EffectInfo, 0, len(ck.Effects))
	seenEffects := map[int]bool{}
	for _, eff := range ck.Effects {
		if seenEffects[eff.Unique] {
			continue
		}
		seenEffects[eff.Unique] = true
		effects = append(effects, eff)
	}
	sort.Slice(effects, func(i, j int) bool { return effects[i].Unique < effects[j].Unique })
	p := &core.Prog{ADTs: ck.ADTOrder, Effects: effects, Entry: ck.EntryName, Natives: ck.Natives}
	var errs []diag.Error
	for _, inst := range ck.Instances {
		d, es := instanceDefinition(inst, ck)
		p.Defs = append(p.Defs, d)
		errs = append(errs, es...)
	}
	for _, info := range infos {
		owner := symbolOwner(info.Name)
		defs, declErrs := decl(info, ck, owner != "")
		for i := range defs {
			defs[i].Owner = owner
		}
		errs = append(errs, declErrs...)
		p.Defs = append(p.Defs, defs...)
		def := &defs[0]
		if def.Name == ck.EntryName && len(def.Params) == 0 {
			if _, isFn := def.Type.(*types.TFun); isFn {
				errs = append(errs, diag.Errorf(info.NameSpan, "BAD MAIN",
					"`main` must be a value, or use the supported function form `main _ : () ->{IO} ()`."))
			} else if len(def.TyParams) > 0 {
				errs = append(errs, diag.Errorf(info.NameSpan, "BAD MAIN",
					"`main` must be a concrete value, but its type `%s` still has\ntype variables in it.", types.Show(def.Type)))
			}
		}
	}
	for _, d := range p.Defs {
		if d.Name == p.Entry && len(d.Params) == 0 {
			p.EntryDisplay = Display(&core.VarRef{Name: d.Name, Ty: d.Type}, ck, d.Owner)
		}
	}
	if len(errs) == 0 {
		specializeScalars(p, infos, ck)
	}
	return p, errs
}

// Decl elaborates one declaration — also the REPL's per-input entry point.
// The first returned Def is the declaration itself; any further Defs are
// lambda-lifted polymorphic block bindings (doc/design.md, "Go backend and runtime", lift.go).
func Decl(info infer.DeclInfo, ck *infer.Checker) ([]core.Def, []diag.Error) {
	return decl(info, ck, false)
}

func decl(info infer.DeclInfo, ck *infer.Checker, stableLifts bool) ([]core.Def, []diag.Error) {
	el := newElab(ck, info.Name, info.Scheme)
	el.stableLifts = stableLifts
	rawType := ck.Sub.Apply(info.Type)
	el.defaultFree(rawType)
	rawType = ck.Sub.Apply(rawType)
	defType := eraseRows(rawType)
	dictNames, dictTypes := el.bindDictionaries(info.Scheme.Preds)
	params := make([]string, len(info.Params))
	if len(info.Params) > 0 {
		argTys, _ := core.PeelFun(defType, len(info.Params))
		for i, p := range info.Params {
			params[i] = coreParamName(p)
			if p.Name != "_" && p.Name != "()" {
				el.pushScope(p.Name, argTys[i])
			}
		}
	}
	def := core.Def{
		Name:         info.Name,
		Type:         prependTypes(dictTypes, defType),
		TyParams:     runtimeRigidVars(rawType),
		Params:       append(dictNames, params...),
		EffectParams: executingEffects(rawType, len(params)),
		Body:         el.anf(el.expr(info.Body)),
	}
	return append([]core.Def{def}, el.aux...), el.errs
}

func symbolOwner(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return ""
}

// runtimeRigidVars includes value-type variables and variables appearing
// only in effect-label arguments, while excluding erased row-tail variables.
// The former still parameterize typed evidence even when an effect parameter
// is phantom in every operation signature.
func runtimeRigidVars(t types.Type) []*types.TVar {
	var out []*types.TVar
	seen := map[int]bool{}
	var walk func(types.Type)
	walk = func(t types.Type) {
		switch t := t.(type) {
		case *types.TVar:
			if t.Rigid && t.Kind != types.RowVar && !seen[t.ID] {
				seen[t.ID] = true
				out = append(out, t)
			}
		case *types.TCon:
			for _, a := range t.Args {
				walk(a)
			}
		case *types.TFun:
			walk(t.Arg)
			walk(t.Eff)
			walk(t.Ret)
		case types.Row:
			for _, l := range t.Labels {
				for _, a := range l.Args {
					walk(a)
				}
			}
		}
	}
	walk(t)
	return out
}

func executingEffects(t types.Type, arity int) []core.EffectInstance {
	if arity == 0 {
		return nil
	}
	var f *types.TFun
	for i := 0; i < arity; i++ {
		f = t.(*types.TFun)
		t = f.Ret
	}
	row := types.SortedRow(f.Eff)
	out := make([]core.EffectInstance, 0, len(row.Labels))
	seen := map[int]bool{}
	for _, l := range row.Labels {
		if types.SurfaceName(l.Name) != "IO" && !seen[l.Unique] {
			out = append(out, core.EffectInstance{Unique: l.Unique, Name: l.Name, Args: append([]types.Type(nil), l.Args...)})
			seen[l.Unique] = true
		}
	}
	return out
}

// Expr elaborates one expression against the checker's solved types. The
// returned aux Defs are lambda-lifted polymorphic block bindings (REPL
// inputs can contain blocks); the caller must install them before
// evaluating the expression.
func Expr(e ast.Expr, ck *infer.Checker) (core.Expr, []core.Def, []diag.Error) {
	if errs := ck.DefaultPreds(ck.PendingPreds, e.Span()); len(errs) > 0 {
		return nil, nil, errs
	}
	el := newElab(ck, "", types.Scheme{})
	ce := el.anf(el.expr(e))
	return ce, el.aux, el.errs
}

type elab struct {
	dicts []dictionary
	owner string
	ck    *infer.Checker
	errs  []diag.Error
	tmp   int // fresh-name counter for spine temporaries, per Decl/Expr

	// declName/declScheme identify the declaration being elaborated: its
	// self-references must instantiate against THIS scheme (the REPL
	// elaborates before binding, so the environment may hold a previous
	// generation).
	declName   string
	declScheme types.Scheme

	// scope tracks the AST-level locals in scope (params, block bindings,
	// pattern variables) with zonked types — the free-variable universe for
	// lambda-lifting (lift.go).
	scope    []scopeVar
	scopeIdx map[string]int

	// lifted maps a generalized block binding's source name to its lifted
	// top-level definition; aux accumulates those definitions.
	lifted map[string]*liftedLocal
	aux    []core.Def

	stableLifts bool
	liftSeq     int
}

func newElab(ck *infer.Checker, declName string, declScheme types.Scheme) *elab {
	return &elab{ck: ck, declName: declName, declScheme: declScheme,
		owner:    symbolOwner(declName),
		scopeIdx: map[string]int{}, lifted: map[string]*liftedLocal{}}
}

type scopeVar struct {
	name string
	ty   types.Type // zonked at binding time
}

func (el *elab) pushScope(name string, ty types.Type) {
	el.scopeIdx[name] = len(el.scope)
	el.scope = append(el.scope, scopeVar{name, ty})
}

func (el *elab) popScope(n int) {
	for i := len(el.scope) - n; i < len(el.scope); i++ {
		delete(el.scopeIdx, el.scope[i].name)
	}
	el.scope = el.scope[:len(el.scope)-n]
}

// lambda nests a multi-parameter surface lambda into single-param Core
// Lambdas, peeling one arrow per parameter off the (ground) function type.
func (el *elab) lambda(params []ast.Param, body ast.Expr, funTy types.Type) core.Expr {
	if len(params) == 0 {
		return el.expr(body)
	}
	fn, ok := funTy.(*types.TFun)
	if !ok {
		panic("elaborate: lambda type is not a function type")
	}
	if params[0].Name != "_" && params[0].Name != "()" {
		el.pushScope(params[0].Name, fn.Arg)
	}
	inner := el.lambda(params[1:], body, fn.Ret)
	if params[0].Name != "_" && params[0].Name != "()" {
		el.popScope(1)
	}
	return &core.Lambda{
		Param: coreParamName(params[0]),
		Body:  inner,
		Ty:    fn,
	}
}

func coreParamName(p ast.Param) string {
	if p.Name == "()" {
		return "_"
	}
	return p.Name
}

func (el *elab) expr(e ast.Expr) core.Expr {
	if desugared := el.ck.Desugared[e]; desugared != nil {
		return el.expr(desugared)
	}
	ty := el.zonkDefault(el.ck.ExprTypes[e])
	switch e := e.(type) {
	case *ast.IntLit:
		// An integer literal whose solved type is Float (`1 + 0.5`) is a
		// Float literal — Elm's number rule made concrete.
		if el.unique(ty) == el.ck.B.Float.Unique {
			return &core.FloatLit{Val: float64(e.Value), Ty: ty}
		}
		return &core.IntLit{Val: e.Value, Ty: ty}
	case *ast.FloatLit:
		return &core.FloatLit{Val: e.Value, Ty: ty}
	case *ast.StringLit:
		return &core.StringLit{Val: e.Value, Ty: ty}
	case *ast.UnitLit:
		return &core.UnitLit{Ty: ty}
	case *ast.Var:
		if _, local := el.scopeIdx[e.Name]; local {
			return &core.VarRef{Name: e.Name, Ty: ty, Local: true}
		}
		if method := el.ck.Methods[e.Name]; method != nil {
			return el.methodValue(method, el.ck.ExprTypes[e])
		}
		if op := el.ck.Operations[e.Name]; op != nil {

			return el.operationValue(op, ty, el.ck.Sub.Apply(el.ck.ExprTypes[e]))
		}
		if n := el.ck.Natives[e.Name]; n != nil {
			return el.nativeValue(n, ty)
		}
		// A lifted local in first-class position gets the same curried-
		// wrapper treatment as a worker (its frees are the leading args).
		if lf := el.lifted[e.Name]; lf != nil {
			return el.partial(el.liftedCallee(lf, ty, el.ck.Sub.Apply(el.ck.ExprTypes[e])), nil)
		}
		// A worker name in first-class position (not an application head —
		// spine.go intercepts those) eta-expands into its curried wrapper.
		if arity, isWorker := el.ck.Workers[e.Name]; isWorker {
			return el.curriedWorkerRef(e.Name, ty, el.ck.Sub.Apply(el.ck.ExprTypes[e]), arity)
		}
		// A polymorphic top-level value compiled to a nullary generic worker
		// (doc/design.md, "Go backend and runtime"): every use is an instantiated zero-argument call.
		if sch, ok := el.ck.Env.Lookup(e.Name); ok && hasRuntimeVars(sch) {
			return el.nullaryValueUse(e.Name, sch, ty)
		}
		return &core.VarRef{Name: e.Name, Ty: ty}
	case *ast.Ctor:
		switch e.Name {
		case "True":
			return &core.BoolLit{Val: true, Ty: ty}
		case "False":
			return &core.BoolLit{Val: false, Ty: ty}
		default:
			info, ok := el.ck.Ctors[e.Name]
			if !ok {
				panic("elaborate: unknown constructor `" + e.Name + "` — the checker should have rejected this")
			}
			return el.ctorValue(info, ty)
		}
	case *ast.App:
		return el.app(e)
	case *ast.Neg:
		return el.fold(&core.Neg{Operand: el.expr(e.Operand), Ty: ty})
	case *ast.If:
		return &core.If{
			Cond: el.expr(e.Cond),
			Then: el.expr(e.Then),
			Else: el.expr(e.Else),
			Ty:   ty,
		}
	case *ast.BinOp:
		l, r := el.expr(e.L), el.expr(e.R)
		if n := el.ck.BinNatives[e]; n != nil {
			return el.fold(&core.NativeCall{Name: n.Name, Module: n.Module, Ty: ty, Args: []core.Expr{l, r}})
		}
		return el.fold(&core.BinOp{Op: e.Op, Ty: ty, L: l, R: r})
	case *ast.Lambda:
		el.defaultFree(el.ck.Sub.Apply(el.ck.ExprTypes[e]))
		return el.lambda(e.Params, e.Body, eraseRows(el.ck.Sub.Apply(el.ck.ExprTypes[e])))
	case *ast.Case:
		return el.caseExpr(e, ty)
	case *ast.Handle:
		return el.handleExpr(e, ty)
	case *ast.Resume:
		panic("elaborate: bare resume")
	case *ast.Block:
		// Fold bindings into a right-nested Let chain; every level carries
		// the block's (result) type. RHSs elaborate in source order so
		// defaulting is deterministic. Local functions become (possibly
		// recursive) Lets of nested Lambdas. Generalized bindings do not
		// become Lets at all: they lambda-lift to top-level generic
		// definitions (doc/design.md, "Go backend and runtime", lift.go) and their uses rewrite to calls.
		var order []any
		pushed := 0
		var liftedHere []string
		items := e.Items
		if len(items) == 0 {
			for i := range e.Binds {
				items = append(items, ast.BlockItem{BindIndex: i})
			}
		}
		for _, item := range items {
			if item.Expr != nil {
				order = append(order, el.expr(item.Expr))
				continue
			}
			bind := &e.Binds[item.BindIndex]
			bindTy := el.ck.BindTypes[bind]
			if sch := el.ck.BindSchemes[bind]; hasRuntimeVars(sch) {
				el.liftBinding(bind, sch)
				liftedHere = append(liftedHere, bind.Name)
				continue
			}
			zonked := el.zonkDefault(bindTy)
			isFn := len(bind.Params) > 0
			if isFn {
				// In scope inside its own body (recursion) — and inside any
				// lift the body contains.
				el.pushScope(bind.Name, zonked)
				pushed++
			}
			var rhs core.Expr
			if len(bind.Params) > 0 {
				rhs = el.lambda(bind.Params, bind.Body, zonked)
			} else {
				rhs = el.expr(bind.Body)
			}
			if !isFn {
				el.pushScope(bind.Name, zonked)
				pushed++
			}
			let := &core.Let{
				Name: bind.Name,
				Rhs:  rhs,
				Rec:  isFn && core.Mentions(rhs, bind.Name),
			}
			order = append(order, let)
		}
		body := el.expr(e.Result)
		el.popScope(pushed)
		for _, name := range liftedHere {
			delete(el.lifted, name)
		}
		for i := len(order) - 1; i >= 0; i-- {
			switch x := order[i].(type) {
			case *core.Let:
				x.Body = body
				x.Ty = body.Type()
				body = x
			case core.Expr:
				body = &core.Seq{First: x, Then: body, Ty: body.Type()}
			}
		}
		return body
	default:
		panic(fmt.Sprintf("elaborate: unhandled AST node %T", e))
	}
}

func (el *elab) handleExpr(e *ast.Handle, ty types.Type) core.Expr {
	info := el.ck.HandleInfos[e]
	if info == nil {
		panic("elaborate: missing handler info")
	}
	clauses := make([]core.HandlerClause, len(e.Clauses))
	for i := range e.Clauses {
		cl, ci := &e.Clauses[i], info.Clauses[i]
		params := make([]string, len(cl.Params))
		pushed := 0
		for j, p := range cl.Params {
			params[j] = p.Name
			if p.Name != "_" && p.Name != "()" {
				el.pushScope(p.Name, el.zonkDefault(ci.ParamTypes[j]))
				pushed++
			}
		}
		pts := make([]types.Type, len(ci.ParamTypes))
		for j, p := range ci.ParamTypes {
			pts[j] = el.zonkDefault(p)
		}
		clauses[i] = core.HandlerClause{Op: ci.Op, Params: params, ParamTypes: pts,
			ResultType: el.zonkDefault(ci.OpResult), Body: el.expr(cl.Body)}
		el.popScope(pushed)
	}
	var ret *core.ReturnClause
	if e.Return != nil {
		name := e.Return.Param.Name
		if name != "_" && name != "()" {
			el.pushScope(name, el.zonkDefault(info.BodyResult))
		}
		ret = &core.ReturnClause{Param: name, Body: el.expr(e.Return.Body)}
		if name != "_" && name != "()" {
			el.popScope(1)
		}
	}
	inst := core.EffectInstance{Unique: info.Effect.Unique, Name: info.Effect.Name}
	for _, a := range info.Effect.Args {
		inst.Args = append(inst.Args, el.zonkDefault(a))
	}
	return &core.Handle{Body: el.expr(e.Body), Effect: inst, Clauses: clauses, Return: ret, TailResumptive: true, Ty: ty}
}

func hasRuntimeVars(s types.Scheme) bool {
	for _, v := range s.Vars {
		if v.Kind != types.RowVar {
			return true
		}
	}
	return false
}

func (el *elab) unique(t types.Type) int {
	if con, ok := t.(*types.TCon); ok && len(con.Args) == 0 {
		return con.Unique
	}
	return -1
}

// fold constant-folds arithmetic over literal operands with the exact
// operations eval uses (wrapping int64, IEEE float64) — see the package
// comment for why this is load-bearing.
func (el *elab) fold(e core.Expr) core.Expr {
	switch e := e.(type) {
	case *core.Neg:
		switch op := e.Operand.(type) {
		case *core.IntLit:
			return &core.IntLit{Val: -op.Val, Ty: e.Ty}
		case *core.FloatLit:
			return &core.FloatLit{Val: -op.Val, Ty: e.Ty}
		}
		return e
	case *core.BinOp:
		name := map[string]string{"+": "Basics.add", "-": "Basics.sub", "*": "Basics.mul", "/": "Basics.fdiv"}[e.Op]
		if name != "" {
			folded := el.fold(&core.NativeCall{Name: name, Module: "Basics", Args: []core.Expr{e.L, e.R}, Ty: e.Ty})
			switch folded.(type) {
			case *core.IntLit, *core.FloatLit:
				return folded
			}
		}
		return e
	case *core.NativeCall:
		spec, ok := natives.Lookup(e.Name)
		if !ok || !spec.Foldable || len(e.Args) != spec.Arity {
			return e
		}
		literal := func(x core.Expr) (any, bool) {
			switch x := x.(type) {
			case *core.IntLit:
				return x.Val, true
			case *core.FloatLit:
				return x.Val, true
			default:
				return nil, false
			}
		}
		args := make([]any, len(e.Args))
		for i, a := range e.Args {
			var ok bool
			args[i], ok = literal(a)
			if !ok {
				return e
			}
		}
		v, err := spec.Eval(&natives.Runtime{}, args)
		if err != nil {
			return e
		}
		switch v := v.(type) {
		case int64:
			return &core.IntLit{Val: v, Ty: e.Ty}
		case float64:
			return &core.FloatLit{Val: v, Ty: e.Ty}
		}
		return e
	default:
		return e
	}
}

// zonkDefault applies the substitution, then defaults any metavariable
// still free: Number-kinded → Int, general → Unit, row tails → empty
// (see doc/design.md, "Type inference", "Go backend and runtime", and
// "Core and evidence invariants"). Open row tails are erased here; concrete
// labels remain on every arrow because indirect calls use them as their
// evidence ABI.
// Defaults are recorded in the checker's substitution so every other
// occurrence of the same variable — including environment schemes held by
// a live REPL session — resolves identically.
func (el *elab) zonkDefault(t types.Type) types.Type {
	origin := t
	t = el.ck.Sub.Apply(t)
	el.defaultFree(t)
	return eraseRowsFrom(origin, el.ck.Sub.Apply(t))
}

func eraseRows(t types.Type) types.Type {
	return eraseRowsFrom(t, t)
}

// eraseRowsFrom retains only labels written into the arrow before solving.
// Labels absorbed later through an open row tail describe an allowed caller
// context, not effects the function closure itself must receive as evidence.
func eraseRowsFrom(origin, t types.Type) types.Type {
	switch t := t.(type) {
	case *types.TVar:
		return t
	case *types.TCon:
		if len(t.Args) == 0 {
			return t
		}
		args := make([]types.Type, len(t.Args))
		for i, a := range t.Args {
			var oa types.Type = a
			if oc, ok := origin.(*types.TCon); ok && i < len(oc.Args) {
				oa = oc.Args[i]
			}
			args[i] = eraseRowsFrom(oa, a)
		}
		return &types.TCon{Unique: t.Unique, Name: t.Name, Args: args}
	case *types.TFun:
		of, ok := origin.(*types.TFun)
		if !ok {
			of = t
		}
		arg, ret := eraseRowsFrom(of.Arg, t.Arg), eraseRowsFrom(of.Ret, t.Ret)
		var eff types.Row
		for _, l := range types.SortedRow(t.Eff).Labels {
			keep := false
			for _, ol := range of.Eff.Labels {
				if ol.Unique == l.Unique {
					keep = true
					break
				}
			}
			if !keep {
				continue
			}
			args := make([]types.Type, len(l.Args))
			for i, a := range l.Args {
				args[i] = eraseRowsFrom(a, a)
			}
			eff.Labels = append(eff.Labels, types.EffLabel{Unique: l.Unique, Name: l.Name, Args: args})
		}
		return &types.TFun{Arg: arg, Eff: eff, Ret: ret}
	case types.Row:
		return types.Row{}
	default:
		return t
	}
}

func (el *elab) defaultFree(t types.Type) {
	switch t := t.(type) {
	case *types.TVar:
		if t.Rigid {
			// Scheme-bound: the definition's own type parameter, not a
			// residual meta. Defaulting it would poison the substitution.
			return
		}
		switch t.Kind {
		case types.General:
			el.ck.Sub[t.ID] = el.ck.B.Unit
		case types.RowVar:
			el.ck.Sub[t.ID] = types.Row{}
		}
	case *types.TCon:
		for _, a := range t.Args {
			el.defaultFree(a)
		}
	case *types.TFun:
		el.defaultFree(t.Arg)
		el.defaultFree(t.Eff)
		el.defaultFree(t.Ret)
	case types.Row:
		for _, l := range t.Labels {
			for _, a := range l.Args {
				el.defaultFree(a)
			}
		}
		if t.Tail != nil {
			el.defaultFree(t.Tail)
		}
	}
}
