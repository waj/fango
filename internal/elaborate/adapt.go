package elaborate

import (
	"fmt"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

type valueAdapter struct {
	from, to types.Type
	ref      *core.VarRef
}

// Equal surface types can still have different residual callback ABIs, even
// underneath a curried result or nominal type argument.
func sameValueABI(a, b types.Type) bool {
	if !types.Equal(a, b) {
		return false
	}
	switch a := a.(type) {
	case *types.TFun:
		b := b.(*types.TFun)
		return types.FunctionOpenRow(a) == types.FunctionOpenRow(b) && types.FunctionControl(a) == types.FunctionControl(b) && sameValueABI(a.Arg, b.Arg) && sameValueABI(a.Ret, b.Ret)
	case *types.TCon:
		b := b.(*types.TCon)
		for i, arg := range a.Args {
			if !sameValueABI(arg, b.Args[i]) {
				return false
			}
		}
	}
	return true
}

// adaptNominalValue maps an immutable nominal value only when its stored
// runtime types differ. Row-indexed values whose rows erase identically need
// no traversal. Recursive conversions are ordinary, typed local functions.
func (el *elab) adaptNominalValue(e core.Expr, want types.Type) core.Expr {
	from, ok := e.Type().(*types.TCon)
	to, tok := want.(*types.TCon)
	if !ok || !tok || from.Unique != to.Unique {
		return e
	}
	adt := el.ck.ADTs[from.Unique]
	if adt == nil {
		return e
	}
	for _, a := range el.valueAdapters {
		if sameValueABI(a.from, from) && sameValueABI(a.to, to) {
			return el.valueApp(a.ref, e)
		}
	}
	name := fmt.Sprintf("_adaptData%d", el.tmp)
	el.tmp++
	param := fmt.Sprintf("_adaptData%d", el.tmp)
	el.tmp++
	matched := fmt.Sprintf("_adaptData%d", el.tmp)
	el.tmp++
	fn := &types.TFun{Arg: from, Ret: to}
	ref := &core.VarRef{Name: name, Local: true, Ty: fn}
	el.valueAdapters = append(el.valueAdapters, valueAdapter{from: from, to: to, ref: ref})
	defer func() { el.valueAdapters = el.valueAdapters[:len(el.valueAdapters)-1] }()
	fromSub, toSub := adt.ParamSubst(from.Args), adt.ParamSubst(to.Args)
	tree := &core.SwitchCtor{Scrut: matched, ADT: adt}
	for _, ctor := range adt.Ctors {
		binds := make([]string, len(ctor.Fields))
		args := make([]core.Expr, len(ctor.Fields))
		var ctorTy types.Type = to
		fields := make([]types.Type, len(ctor.Fields))
		for i, field := range ctor.Fields {
			binds[i] = fmt.Sprintf("_adaptField%d", el.tmp)
			el.tmp++
			actual := el.eraseRuntimeKinds(eraseRows(types.SubstRigid(field, fromSub)))
			fields[i] = el.eraseRuntimeKinds(eraseRows(types.SubstRigid(field, toSub)))
			args[i] = el.adaptFunctionValue(&core.VarRef{Name: binds[i], Local: true, Ty: actual}, fields[i])
		}
		for i := len(fields) - 1; i >= 0; i-- {
			ctorTy = &types.TFun{Arg: fields[i], Ret: ctorTy}
		}
		body := el.ctorCallee(ctor, ctorTy).saturatedApp(args)
		tree.Cases = append(tree.Cases, core.CtorCase{Ctor: ctor, Binds: binds, Tree: &core.Leaf{Body: body}})
	}
	body := &core.Case{Scrut: &core.VarRef{Name: param, Local: true, Ty: from}, Bind: matched, Tree: tree, Ty: to}
	lambda := &core.Lambda{Param: param, Body: body, Ty: fn, ParamCapture: el.ck.Sup.FreshCapture()}
	return &core.Let{Name: name, Rhs: lambda, Rec: true, Body: el.valueApp(ref, e), Ty: to}
}
