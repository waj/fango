package fangort

// TypeDescriptor is a checked nominal type identity. Generated code supplies
// the declaration identity and argument descriptors; no Go reflection or value
// shape is used to decide whether a payload has the requested source type.
// All fields are private so source snapshots cannot alter their proof.
type TypeDescriptor struct {
	name        string
	arguments   []*TypeDescriptor
	inspectable bool
}

// NominalType constructs immutable descriptor data. safe describes the whole
// declaration, including constructors that are absent from a particular value.
func NominalType(name string, safe bool, arguments ...*TypeDescriptor) *TypeDescriptor {
	args := append([]*TypeDescriptor(nil), arguments...)
	for _, arg := range args {
		safe = safe && arg != nil && arg.inspectable
	}
	return &TypeDescriptor{name: name, arguments: args, inspectable: safe}
}

func sameDescriptor(left, right *TypeDescriptor) bool {
	if left == nil || right == nil {
		return false
	}
	if left == right {
		return true
	}
	if left.name != right.name || len(left.arguments) != len(right.arguments) {
		return false
	}
	for i, arg := range left.arguments {
		if !sameDescriptor(arg, right.arguments[i]) {
			return false
		}
	}
	return true
}

// Failure is a detached snapshot. It contains immutable payload data and nested
// snapshots, never handler targets, frames, or resumptions.
type Failure struct {
	effect      string
	operation   string
	arguments   []any
	descriptors []*TypeDescriptor
	suppressed  []*Failure
}

func SnapshotFailure(exit *ExitRequest) *Failure {
	if exit == nil {
		return nil
	}
	failure := &Failure{effect: exit.Effect, operation: exit.OperationName,
		arguments: append([]any(nil), exit.Payload...), descriptors: append([]*TypeDescriptor(nil), exit.PayloadTypes...)}
	for _, secondary := range exit.Suppressed {
		failure.suppressed = append(failure.suppressed, SnapshotFailure(secondary))
	}
	return failure
}

func SnapshotSuppressed(exit *ExitRequest) List[*Failure] {
	result := ListNil[*Failure]()
	if exit != nil {
		for i := len(exit.Suppressed) - 1; i >= 0; i-- {
			result = ListCons(SnapshotFailure(exit.Suppressed[i]), result)
		}
	}
	return result
}

func (failure *Failure) Effect() string {
	if failure == nil {
		return ""
	}
	return failure.effect
}
func (failure *Failure) Operation() string {
	if failure == nil {
		return ""
	}
	return failure.operation
}
func (failure *Failure) ArgumentCount() int64 {
	if failure == nil {
		return 0
	}
	return int64(len(failure.arguments))
}
func (failure *Failure) Suppressed() List[*Failure] {
	result := ListNil[*Failure]()
	if failure != nil {
		for i := len(failure.suppressed) - 1; i >= 0; i-- {
			result = ListCons(failure.suppressed[i], result)
		}
	}
	return result
}

// FailureArgument projects only after checking the complete source descriptor.
// A failed Go assertion after a descriptor match means the compiler packaged an
// invalid proof; it is not an alternate type-inspection mechanism.
func FailureArgument[A any](index int64, failure *Failure, expected *TypeDescriptor) (A, bool) {
	var zero A
	if failure == nil || index < 0 || index >= int64(len(failure.arguments)) || index >= int64(len(failure.descriptors)) || expected == nil || !expected.inspectable || failure.descriptors[index] == nil || !failure.descriptors[index].inspectable || !sameDescriptor(expected, failure.descriptors[index]) {
		return zero, false
	}
	value, ok := failure.arguments[index].(A)
	if !ok {
		panic("fango: failure payload disagrees with its checked type descriptor")
	}
	return value, true
}
