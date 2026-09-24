package types

const (
	NativeRequestImmediateName   = "Runtime.NativeRequest.immediate"
	CompletionTypeName           = "Runtime.Completion.Completion"
	CompletionCaptureName        = "Runtime.Completion.capture"
	CompletionReplayName         = "Runtime.Completion.replay"
	CompletionFailureName        = "Runtime.Completion.failure"
	CompletionFromFailureName    = "Runtime.Completion.fromFailure"
	CompletionDropSuspensionName = "Runtime.Completion.dropSuspension"
	CompletionDropDriveName      = "Runtime.Completion.dropDrive"
)

func CompletionIntrinsic(name string) bool {
	return CapturesCompletion(name) || name == CompletionReplayName || name == CompletionFailureName || name == CompletionFromFailureName || name == CompletionDropSuspensionName || name == CompletionDropDriveName
}

// CompletionDeclarationShape additionally verifies the unerased source row.
func CompletionDeclarationShape(name string, t Type) bool {
	fn, ok := t.(*TFun)
	if !ok || !CompletionShape(name, fn.Arg, fn.Ret) {
		return false
	}
	if name == CompletionDropSuspensionName || name == CompletionDropDriveName {
		con := fn.Arg.(*TCon)
		resultCon := fn.Ret.(*TCon)
		inputRow, ok := con.Args[1].(Row)
		label := CoroutineSuspensionName
		if name == CompletionDropDriveName {
			label = CoroutineDriveName
		}
		if !ok || len(inputRow.Labels) != 1 || inputRow.Labels[0].Name != label || !inputRow.Labels[0].Suspension || inputRow.Labels[0].Abort || len(inputRow.Labels[0].Args) != 0 || inputRow.Tail == nil {
			return false
		}
		return fn.Eff.Empty() && Equal(inputRow.Tail, resultCon.Args[1])
	}
	if name == CompletionFromFailureName {
		row, ok := fn.Ret.(*TCon).Args[1].(*TVar)
		return fn.Eff.Empty() && ok && row.Kind == RowVar
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
	if name == CompletionFromFailureName {
		failure, ok := arg.(*TCon)
		if !ok || failure.Name != FailureTypeName || len(failure.Args) != 0 {
			return false
		}
		completion, ok := result.(*TCon)
		if !ok || completion.Name != CompletionTypeName || len(completion.Args) != 2 {
			return false
		}
		unit, ok := completion.Args[0].(*TCon)
		if !ok || unit.Name != "()" {
			return false
		}
		return true
	}
	var completion Type = arg
	if CapturesCompletion(name) {
		completion = result
	}
	con, ok := completion.(*TCon)
	if !ok || con.Name != CompletionTypeName || len(con.Args) != 2 {
		return false
	}
	switch name {
	case CompletionDropSuspensionName, CompletionDropDriveName:
		resultCon, ok := result.(*TCon)
		if !ok || resultCon.Name != CompletionTypeName || len(resultCon.Args) != 2 || !Equal(con.Args[0], resultCon.Args[0]) {
			return false
		}
		return true
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
