package elaborate

import (
	"fmt"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Spine collapsing and saturation analysis (DESIGN.md §8.2, §8.7).
// Saturation is resolved exactly once, here. Core after elaboration
// contains only:
//
//   - App{Worker}: exactly arity args, callee a VarRef naming a worker
//   - App{Value}:  exactly one arg per application (typed indirect call)
//   - Lambda:      one currying step
//
// Both backends consume the same pre-chewed spines; the linter enforces
// the shape (including banning bare worker references, which proves every
// first-class use was eta-expanded).

// app elaborates the outermost application of a spine.
func (el *elab) app(e *ast.App) core.Expr {
	// Flatten the curried spine to (head, args…).
	var rev []ast.Expr
	var cur ast.Expr = e
	for {
		a, ok := cur.(*ast.App)
		if !ok {
			break
		}
		rev = append(rev, a.Arg)
		cur = a.Fn
	}
	head := cur
	args := make([]ast.Expr, len(rev))
	for i, a := range rev {
		args[len(rev)-1-i] = a
	}

	// Known worker head?
	if v, ok := head.(*ast.Var); ok {
		if arity, isWorker := el.ck.Workers[v.Name]; isWorker {
			workerTy := el.zonkDefault(el.ck.ExprTypes[head])
			return el.workerCall(v.Name, workerTy, arity, args)
		}
	}

	// Constructor head? Same saturation discipline as workers (§8.2 item 5);
	// True/False fall through (they are nullary BoolLits and a type-correct
	// program never applies them).
	if c, ok := head.(*ast.Ctor); ok {
		if info, isCtor := el.ck.Ctors[c.Name]; isCtor && len(info.Fields) > 0 {
			return el.ctorCall(info, args)
		}
	}

	// Unknown callee: one typed indirect call per application.
	res := el.expr(head)
	for _, a := range args {
		res = el.valueApp(res, el.expr(a))
	}
	return res
}

// valueApp is one typed indirect application: callee(arg).
func (el *elab) valueApp(callee, arg core.Expr) core.Expr {
	fn, ok := callee.Type().(*types.TFun)
	if !ok {
		panic(fmt.Sprintf("elaborate: applying a non-function type %s", types.Show(callee.Type())))
	}
	return &core.App{
		CalleeKind: core.Value,
		Callee:     callee,
		Args:       []core.Expr{arg},
		Ty:         fn.Ret,
	}
}

// callee is a known-arity application head: a top-level worker or a
// constructor. Both get the same saturation analysis; only the emitted
// App's kind differs.
type callee struct {
	kind  core.CalleeKind
	name  string
	ty    types.Type // full curried type
	arity int
	ctor  *types.CtorInfo // when kind == core.Ctor
}

func workerCallee(name string, workerTy types.Type, arity int) callee {
	return callee{kind: core.Worker, name: name, ty: workerTy, arity: arity}
}

func ctorCallee(info *types.CtorInfo) callee {
	return callee{kind: core.Ctor, name: info.Name, ty: info.ValueType(),
		arity: len(info.Fields), ctor: info}
}

// saturatedApp builds the direct App for a fully applied callee.
func (c callee) saturatedApp(args []core.Expr) *core.App {
	_, ret := core.PeelFun(c.ty, c.arity)
	return &core.App{
		CalleeKind: c.kind,
		Callee:     &core.VarRef{Name: c.name, Ty: c.ty},
		Args:       args,
		Ty:         ret,
		Ctor:       c.ctor,
	}
}

// workerCall classifies a call to a known worker by saturation.
func (el *elab) workerCall(name string, workerTy types.Type, arity int, args []ast.Expr) core.Expr {
	return el.calleeCall(workerCallee(name, workerTy, arity), args)
}

// ctorCall classifies a constructor application by saturation.
func (el *elab) ctorCall(info *types.CtorInfo, args []ast.Expr) core.Expr {
	return el.calleeCall(ctorCallee(info), args)
}

func (el *elab) calleeCall(c callee, args []ast.Expr) core.Expr {
	switch {
	case len(args) == c.arity: // saturated: a direct call / struct literal
		coreArgs := make([]core.Expr, c.arity)
		for i, a := range args {
			coreArgs[i] = el.expr(a)
		}
		return c.saturatedApp(coreArgs)

	case len(args) > c.arity: // oversaturated: direct call, then indirect
		res := el.calleeCall(c, args[:c.arity])
		for _, a := range args[c.arity:] {
			res = el.valueApp(res, el.expr(a))
		}
		return res

	default: // partial (including a bare reference via curried*Ref)
		return el.partial(c, args)
	}
}

// partial eta-expands an unsaturated worker or constructor application into
// §8.2 item 4's "exactly one closure whose body calls the worker": non-atomic
// given arguments are hoisted into Lets (strictness — they must evaluate when
// the partial is created, not per call), then nested Lambdas supply the
// missing parameters around one saturated App.
func (el *elab) partial(c callee, given []ast.Expr) core.Expr {
	workerTy, arity := c.ty, c.arity
	argTys, _ := core.PeelFun(workerTy, arity)

	// Elaborate given args; hoist non-atoms into temps.
	type hoist struct {
		name string
		rhs  core.Expr
	}
	var hoists []hoist
	coreArgs := make([]core.Expr, 0, arity)
	for _, a := range given {
		ca := el.expr(a)
		if isAtom(ca) {
			coreArgs = append(coreArgs, ca)
			continue
		}
		tmp := fmt.Sprintf("_a%d", el.tmp)
		el.tmp++
		hoists = append(hoists, hoist{tmp, ca})
		coreArgs = append(coreArgs, &core.VarRef{Name: tmp, Ty: ca.Type()})
	}

	// Missing parameters become nested lambda params.
	missing := arity - len(given)
	lamParams := make([]string, missing)
	for i := range missing {
		lamParams[i] = fmt.Sprintf("_w%d", el.tmp)
		el.tmp++
		coreArgs = append(coreArgs, &core.VarRef{Name: lamParams[i], Ty: argTys[len(given)+i]})
	}

	var body core.Expr = c.saturatedApp(coreArgs)

	// Wrap lambdas innermost-out; each level's type is the remaining chain.
	lamTy := workerTy
	for range given {
		lamTy = lamTy.(*types.TFun).Ret
	}
	tys := make([]types.Type, missing)
	t := lamTy
	for i := range missing {
		tys[i] = t
		t = t.(*types.TFun).Ret
	}
	for i := missing - 1; i >= 0; i-- {
		body = &core.Lambda{Param: lamParams[i], Body: body, Ty: tys[i]}
	}

	// Hoisted args evaluate at partial-creation time: Lets wrap outside.
	for i := len(hoists) - 1; i >= 0; i-- {
		body = &core.Let{Name: hoists[i].name, Rhs: hoists[i].rhs, Body: body, Ty: body.Type()}
	}
	return body
}

// curriedWorkerRef is the k=0 case: a worker used first-class expands to
// its curried wrapper at the use site — demand-driven by construction.
func (el *elab) curriedWorkerRef(name string, workerTy types.Type, arity int) core.Expr {
	return el.partial(workerCallee(name, workerTy, arity), nil)
}

// ctorValue is a constructor in value position: nullary constructors are the
// saturated zero-arg App (a zero-field struct in codegen); field-taking ones
// get the same curried-wrapper treatment as first-class workers.
func (el *elab) ctorValue(info *types.CtorInfo) core.Expr {
	if len(info.Fields) == 0 {
		return ctorCallee(info).saturatedApp(nil)
	}
	return el.partial(ctorCallee(info), nil)
}

// isAtom reports whether re-evaluating e is free (no work, no effects):
// literals and variable references skip ANF hoisting.
func isAtom(e core.Expr) bool {
	switch e.(type) {
	case *core.IntLit, *core.FloatLit, *core.StringLit, *core.BoolLit, *core.VarRef:
		return true
	default:
		return false
	}
}
