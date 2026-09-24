package core

import "github.com/waj/fango/internal/types"

func (l *linter) serviceWork(w *Work, where string) {
	if len(w.Args) != 1 || !EqualValueRepresentation(w.Args[0].Type(), w.Ty) {
		l.errorf("%s: invalid service authority edge", where)
		return
	}
	if w.Kind == "invocation-slot" {
		d := l.workers[l.defName]
		arg, ok := w.Args[0].(*VarRef)
		if l.defName != types.ServiceRunName || !l.intrinsics[l.defName] || d == nil || !types.ServiceRunShape(d.SourceType) || !ok || !arg.Local || len(d.Params) != 2 || arg.Name != d.Params[0] {
			l.errorf("%s: invocation slot outside its checked producer scope", where)
		}
	} else {
		row, ok := w.SourceRow.(types.Row)
		valid := ok && len(row.Labels) == 1 && row.Tail == nil
		if valid {
			label := row.Labels[0]
			effect := l.effects[label.Unique]
			valid = effect != nil && effect.Invocation && effect.Name == types.ServiceInvocationName && len(label.Args) == 2 && EqualValueRepresentation(w.Ty, types.InvocationCallback(label))
			lambda, ok := w.Args[0].(*Lambda)
			valid = valid && ok
			if valid {
				call, ok := lambda.Body.(*Perform)
				valid = ok && call.Effect.Unique == label.Unique && len(call.Args) == 1 && len(lambda.EffectParams) == 0 && lambda.RowParam == 0
				if valid {
					arg, ok := call.Args[0].(*VarRef)
					valid = ok && arg.Local && arg.Name == lambda.Param && EqualValueRepresentation(call.Type(), label.Args[1])
				}
			}
		}
		if !valid {
			l.errorf("%s: invalid implicit service invocation adapter", where)
		}
	}
	l.expr(w.Args[0], where)
}

func (l *linter) invocationHandle(h *Handle, where string) {
	valid := h.Scoped && h.State == nil && h.Return == nil && len(h.Clauses) == 1 && len(h.Effect.Args) == 2
	calls := 0
	if valid {
		clause := h.Clauses[0]
		Inspect(clause.Body, func(expr Expr) {
			switch e := expr.(type) {
			case *App:
				calls++
				ref, ok := e.Callee.(*VarRef)
				valid = valid && ok
				if ok {
					proof := l.serviceAuthority[ref.Name]
					fn, ok := proof.(*types.TFun)
					valid = valid && ref.Local && ok && types.Equal(fn.Arg, h.Effect.Args[0]) && types.Equal(fn.Ret, h.Effect.Args[1])
				}
			case *Perform, *Handle, *Lambda, *NativeCall, *Work:
				valid = false
			}
		})
	}
	if !valid || calls != 1 {
		l.errorf("%s: invocation handler lacks its checked execution authority", where)
	}
}
