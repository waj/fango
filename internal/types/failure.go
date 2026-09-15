package types

// FailureInspectionShape checks the source and Core projection signature.
func FailureInspectionShape(name string, args []Type, result Type) bool {
	if !FailureInspection(name) || len(args) != IntrinsicArity(name) {
		return false
	}
	nominal := func(t Type, name string, arity int) bool {
		con, ok := t.(*TCon)
		return ok && con.Name == name && len(con.Args) == arity
	}
	if !nominal(args[len(args)-1], FailureTypeName, 0) {
		return false
	}
	switch name {
	case FailureArgumentName:
		return nominal(args[0], "Int", 0) && nominal(result, "Maybe.Maybe", 1)
	case FailureEffectName, FailureOperationName:
		return nominal(result, "String", 0)
	case FailureArgumentCountName:
		return nominal(result, "Int", 0)
	case FailureSuppressedName:
		return nominal(result, "List.List", 1) && nominal(result.(*TCon).Args[0], FailureTypeName, 0)
	}
	return false
}

func AttemptReportShape(t Type) bool {
	fn, ok := t.(*TFun)
	if !ok || len(fn.Eff.Labels) != 0 || fn.Eff.Tail == nil {
		return false
	}
	action, ok := fn.Arg.(*TFun)
	if !ok || len(action.Eff.Labels) != 1 || action.Eff.Tail == nil || !Equal(action.Eff.Tail, fn.Eff.Tail) {
		return false
	}
	unit, ok := action.Arg.(*TCon)
	if !ok || unit.Name != "()" || len(unit.Args) != 0 {
		return false
	}
	fail := action.Eff.Labels[0]
	if fail.Name != "Fail.Fail" || !fail.Abort || len(fail.Args) != 1 {
		return false
	}
	result, ok := fn.Ret.(*TCon)
	if !ok || result.Name != "Result.Result" || len(result.Args) != 2 || !Equal(result.Args[1], action.Ret) {
		return false
	}
	report, ok := result.Args[0].(*TCon)
	return ok && report.Name == "Fail.Report" && len(report.Args) == 1 && Equal(report.Args[0], fail.Args[0])
}
