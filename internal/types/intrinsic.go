package types

// Compiler intrinsics are bundled native declarations implemented as Core
// nodes. Recognition uses resolved declaration identity, never user spelling.
const (
	AsyncParMapName    = "Async.parMap"
	AsyncLaunchName    = "Async.launch"
	AsyncRebaseName    = "Async.rebase"
	AsyncSuperviseName = "Async.supervise"

	// ScopeBracketName owns resource cleanup on normal and abort exits.
	ScopeBracketName = "Runtime.Scope.bracket"

	FailureTypeName          = "Failure.Failure"
	FailureArgumentName      = "Failure.argument"
	FailureEffectName        = "Failure.effectName"
	FailureOperationName     = "Failure.operationName"
	FailureArgumentCountName = "Failure.argumentCount"
	FailureSuppressedName    = "Failure.suppressed"
	FailAttemptReportName    = "Fail.attemptReport"

	// PromptLevelName is answered by the REPL's evaluator, which runs the
	// prompt's inputs inside the handlers around the call.
	PromptLevelName = "Runtime.Prompt.level"
)

// RuntimeEvidenceEffect reports whether a row label needs runtime evidence.
// IO is ambient; scoped permissions erase.
func RuntimeEvidenceEffect(label EffLabel) bool {
	return !label.Scoped && SurfaceName(label.Name) != "IO"
}

func Intrinsic(name string) bool { return IntrinsicArity(name) != 0 }

// IntrinsicArity is fixed by the compiler, not read from the declaration.
func IntrinsicArity(name string) int {
	switch name {
	case ScopeBracketName:
		return 3
	case FailureArgumentName, AsyncLaunchName, AsyncParMapName:
		return 2
	case AsyncSuperviseName, AsyncRebaseName, FailureEffectName, FailureOperationName, FailureArgumentCountName, FailureSuppressedName, FailAttemptReportName:
		return 1
	}
	return 0
}

func FailureInspection(name string) bool {
	switch name {
	case FailureArgumentName, FailureEffectName, FailureOperationName, FailureArgumentCountName, FailureSuppressedName:
		return true
	}
	return false
}
