package fangort

import "testing"

func TestFailureTraceDetachesPresenceAndNestedFrames(t *testing.T) {
	frame := Frame{Function: "Main.body", File: "Main.fango", Line: 12}
	secondary := &ExitRequest{TraceCaptured: true, Trace: []Frame{{Function: "Main.cleanup", Line: 20}}}
	exit := Suppress(&ExitRequest{TraceCaptured: true, Trace: []Frame{frame}}, secondary)
	failure := SnapshotFailure(exit)
	exit.Trace[0].Line = 99
	secondary.Trace[0].Line = 99
	frames, captured := failure.Trace()
	if !captured || len(frames) != 1 || frames[0] != frame {
		t.Fatalf("primary trace changed: %v %v", frames, captured)
	}
	frames[0].Line = 100
	again, _ := failure.Trace()
	if again[0] != frame {
		t.Fatal("native reader modified the detached trace")
	}
	nested, captured := failure.Suppressed().Head().Trace()
	if !captured || len(nested) != 1 || nested[0].Line != 20 {
		t.Fatalf("suppressed trace changed: %v %v", nested, captured)
	}
	for _, present := range []bool{false, true} {
		frames, got := SnapshotFailure(&ExitRequest{TraceCaptured: present}).Trace()
		if got != present || len(frames) != 0 {
			t.Fatalf("empty trace presence: %v %v, want %v", frames, got, present)
		}
	}
}

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
