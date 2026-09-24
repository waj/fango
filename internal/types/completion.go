package types

const (
	NativeRequestImmediateName = "NativeRequest.immediate"
	CompletionTypeName         = "Completion.Completion"
	CompletionCaptureName      = "Completion.capture"
	CompletionReplayName       = "Completion.replay"
	CompletionFailureName      = "Completion.failure"
)

func CompletionIntrinsic(name string) bool {
	return CapturesCompletion(name) || name == CompletionReplayName || name == CompletionFailureName
}

// CompletionDeclarationShape additionally verifies the unerased source row.
func CompletionDeclarationShape(name string, t Type) bool {
	fn, ok := t.(*TFun)
	if !ok || !CompletionShape(name, fn.Arg, fn.Ret) {
		return false
	}
	con, _ := fn.Arg.(*TCon)
	if CapturesCompletion(name) {
		con = fn.Ret.(*TCon)
	}
	row, ok := con.Args[1].(*TVar)
	if !ok || row.Kind != RowVar {
		return false
	}
	if name == CompletionFailureName {
		return fn.Eff.Tail == nil && len(fn.Eff.Labels) == 0
	}
	if len(fn.Eff.Labels) != 0 || fn.Eff.Tail == nil || !Equal(fn.Eff.Tail, row) {
		return false
	}
	if CapturesCompletion(name) {
		action := fn.Arg.(*TFun)
		return len(action.Eff.Labels) == 0 && action.Eff.Tail != nil && Equal(action.Eff.Tail, row)
	}
	return true
}

func CompletionShape(name string, arg, result Type) bool {
	var completion Type = arg
	if CapturesCompletion(name) {
		completion = result
	}
	con, ok := completion.(*TCon)
	if !ok || con.Name != CompletionTypeName || len(con.Args) != 2 {
		return false
	}
	switch name {
	case CompletionCaptureName, NativeRequestImmediateName:
		fn, ok := arg.(*TFun)
		if !ok {
			return false
		}
		unit, ok := fn.Arg.(*TCon)
		return ok && unit.Name == "()" && Equal(fn.Ret, con.Args[0])
	case CompletionReplayName:
		return Equal(result, con.Args[0])
	case CompletionFailureName:
		maybe, ok := result.(*TCon)
		if !ok || maybe.Name != "Maybe.Maybe" || len(maybe.Args) != 1 {
			return false
		}
		failure, ok := maybe.Args[0].(*TCon)
		return ok && failure.Name == FailureTypeName && len(failure.Args) == 0
	}
	return false
}

func CapturesCompletion(name string) bool {
	return name == CompletionCaptureName || name == NativeRequestImmediateName
}
