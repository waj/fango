package fangort

import "testing"

func TestFailureSnapshotUsesNominalDescriptorsAndDetachesControl(t *testing.T) {
	integer := NominalType("Int", true)
	text := NominalType("String", true)
	private := NominalType("Private.Error#1", true, integer)
	type privateError struct{ code int64 }
	exit := &ExitRequest{Target: &ExitTarget{}, Effect: "Fail.Fail", OperationName: "Fail.fail", Payload: []any{privateError{42}}, PayloadTypes: []*TypeDescriptor{private}, Suppressed: []*ExitRequest{{Effect: "Other.Failure", OperationName: "Other.raise", Payload: []any{"cleanup"}, PayloadTypes: []*TypeDescriptor{text}}}}
	failure := SnapshotFailure(exit)
	exit.Target = nil
	exit.Payload[0] = privateError{99}
	exit.PayloadTypes[0] = text
	exit.Suppressed[0].Payload[0] = "changed"
	if failure.Effect() != "Fail.Fail" || failure.Operation() != "Fail.fail" || failure.ArgumentCount() != 1 {
		t.Fatal("snapshot metadata changed")
	}
	got, ok := FailureArgument[privateError](0, failure, NominalType("Private.Error#1", true, NominalType("Int", true)))
	if !ok || got.code != 42 {
		t.Fatalf("private payload: %#v %v", got, ok)
	}
	for _, wrong := range []*TypeDescriptor{text, NominalType("Private.Error#2", true, integer), NominalType("Private.Error#1", true, text), NominalType("Private.Error#1", false, integer), nil} {
		if _, ok := FailureArgument[privateError](0, failure, wrong); ok {
			t.Fatal("wrong descriptor accepted")
		}
	}
	for _, index := range []int64{-1, 1, 1 << 62} {
		if _, ok := FailureArgument[privateError](index, failure, private); ok {
			t.Fatal("invalid index accepted")
		}
	}
	nested := failure.suppressed[0]
	if got, ok := FailureArgument[string](0, nested, text); !ok || got != "cleanup" {
		t.Fatalf("suppressed payload: %q %v", got, ok)
	}
}

func TestFailureSnapshotNeverInspectsFunctionOrResourceBearingTypes(t *testing.T) {
	unsafe := NominalType("Function", false)
	wrapped := NominalType("Maybe", true, unsafe)
	if wrapped.inspectable {
		t.Fatal("container erased its opaque type argument")
	}
	failure := SnapshotFailure(&ExitRequest{Payload: []any{func() {}}, PayloadTypes: []*TypeDescriptor{unsafe}})
	if _, ok := FailureArgument[func()](0, failure, unsafe); ok {
		t.Fatal("function payload exposed")
	}
	opaque := SnapshotFailure(&ExitRequest{Payload: []any{42}})
	if _, ok := FailureArgument[int](0, opaque, NominalType("Int", true)); ok {
		t.Fatal("missing descriptor accepted")
	}
}

func TestFailureSnapshotRetainsNestedSuppressionOrder(t *testing.T) {
	makeExit := func(name string) *ExitRequest { return &ExitRequest{Effect: name} }
	outer := Suppress(makeExit("outer"), makeExit("nested"))
	failure := SnapshotFailure(Suppress(Suppress(makeExit("body"), makeExit("inner")), outer))
	if failure.effect != "body" || len(failure.suppressed) != 2 || failure.suppressed[0].effect != "inner" || failure.suppressed[1].effect != "outer" || failure.suppressed[1].suppressed[0].effect != "nested" {
		t.Fatal("suppression tree flattened or reordered")
	}
}
