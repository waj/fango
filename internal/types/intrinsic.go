package types

// Compiler intrinsics are bundled native declarations implemented as Core
// nodes. Recognition uses resolved declaration identity, never user spelling.
const (
	ServiceRunName        = "Runtime.Service.run"
	ServiceInvocationName = "Runtime.Service.Invocation"
	// ScopeBracketName owns synchronous resource cleanup on normal and abort exits.
	ScopeBracketName         = "Runtime.Scope.bracket"
	CoroutineTypeName        = "Runtime.Coroutine.Coroutine"
	CoroutineStepName        = "Runtime.Coroutine.Step"
	CoroutineFacetTypeName   = "Runtime.Coroutine.Facet"
	CoroutineFacetName       = "Runtime.Coroutine.facet"
	CoroutineScopeName       = "Runtime.Coroutine.scope"
	CoroutineCreateName      = "Runtime.Coroutine.create"
	CoroutineScopeTypeName   = "Runtime.Coroutine.Scope"
	CoroutineWithName        = "Runtime.Coroutine.with"
	CoroutineAdvanceName     = "Runtime.Coroutine.advance"
	CoroutineCloseName       = "Runtime.Coroutine.close"
	CoroutineSuspensionName  = "Runtime.Coroutine.Suspension"
	CoroutineDriveName       = "Runtime.Coroutine.Drive"
	FailureTypeName          = "Failure.Failure"
	FailureArgumentName      = "Failure.argument"
	FailureEffectName        = "Failure.effectName"
	FailureOperationName     = "Failure.operationName"
	FailureArgumentCountName = "Failure.argumentCount"
	FailureSuppressedName    = "Failure.suppressed"
	FailAttemptReportName    = "Fail.attemptReport"
)

// RuntimeEvidenceEffect reports whether a row label needs runtime evidence.
// IO is ambient. Coroutine control uses checked owner identities.
func RuntimeEvidenceEffect(label EffLabel) bool {
	return SurfaceName(label.Name) != "IO" && !(label.Suspension && (label.Name == CoroutineDriveName || label.Name == CoroutineSuspensionName))
}

// CursorAccess is advancement proof metadata. Zero is deliberately invalid.
type CursorAccess uint8

const ExclusiveAdvance CursorAccess = 1

func Intrinsic(name string) bool { return IntrinsicArity(name) != 0 }

// IntrinsicArity is fixed by the compiler, not read from the declaration.
func IntrinsicArity(name string) int {
	if CompletionIntrinsic(name) {
		return 1
	}
	switch name {
	case ServiceRunName:
		return 2
	case WorkRunName, WorkFacetName, WorkOwnerName, CoroutineFacetName:
		return 1
	case WorkPackName, WorkCloseName, WorkRegisterName:
		return 2
	case WorkAdvanceName:
		return 3
	case ScopeBracketName:
		return 3
	case CoroutineCreateName, CoroutineWithName, CoroutineAdvanceName, FailureArgumentName:
		return 2
	case CoroutineScopeName, CoroutineCloseName, FailureEffectName, FailureOperationName, FailureArgumentCountName, FailureSuppressedName, FailAttemptReportName:
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
