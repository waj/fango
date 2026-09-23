package core

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestSuspensionProjectionRequiresMatchingEffectProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, operation string
		through         []int
		want            int
	}{
		{"handled suspension", "suspend", []int{10}, 0},
		{"unrelated effect", "suspend", []int{11}, 1},
		{"direct pause", "suspend", nil, 1},
		{"drive survives", "drive", []int{10}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := testFlowChecker()
			var needs []ControlNeed
			f.collectControlNeed = func(n ControlNeed) { needs = append(needs, n) }
			call := &flowContext{sourceRows: []types.Type{types.Row{Tail: types.Row{Labels: []types.EffLabel{{Unique: 10, Name: "Emit"}}}}}}
			f.requireControl(call, 0, tc.operation, tc.through...)
			if len(needs) != tc.want {
				t.Fatalf("got %d control obligations, want %d", len(needs), tc.want)
			}
		})
	}
}

func TestRecursiveInputsDistinguishWrappedDescriptions(t *testing.T) {
	f := testFlowChecker()
	f.contexts["owner"] = &flowContext{id: "owner"}
	adapter := &types.CaptureFlow{Kind: "lambda", Children: []*types.CaptureFlow{{Kind: "scalar"}}}
	// Outer already captures inner, but they describe distinct production.
	f.objects = append(f.objects,
		&flowObject{kind: "ctor"},
		&flowObject{kind: "ctor", fields: []flowValue{{refs: []int{1}}}},
		&flowObject{kind: "lambda", code: adapter, ancestry: []string{"owner"}, fields: []flowValue{{refs: []int{2}}}},
		&flowObject{kind: "lambda", code: adapter, ancestry: []string{"owner"}, fields: []flowValue{{refs: []int{1}}}},
		&flowObject{kind: "lambda", code: adapter, ancestry: []string{"owner"}, fields: []flowValue{{refs: []int{2}}}},
	)
	env := func(ref int) flowEnv { return flowEnv{values: map[string]flowValue{"producer": {refs: []int{ref}}}} }
	if f.recursiveInputs("owner", env(3), env(4)) {
		t.Fatal("fresh adapters hid distinct pre-existing producers")
	}
	if !f.recursiveInputs("owner", env(3), env(5)) {
		t.Fatal("equivalent recursive adapters did not fold")
	}
	// Accumulating ordinary data must still reach a finite recursive summary.
	f.objects = append(f.objects, &flowObject{kind: "ctor", ancestry: []string{"owner"}, fields: []flowValue{{refs: []int{1, 2}}}})
	if !f.recursiveInputs("owner", env(1), env(6)) {
		t.Fatal("growing local data prevented recursive widening")
	}
}

func TestAbstractDriveDoesNotHideUnknownWrapperFields(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		f := testFlowChecker()
		f.owners = append(f.owners, &flowOwner{code: &types.CaptureFlow{Kind: "coroutine"}})
		f.objects = append(f.objects,
			&flowObject{kind: "coroutine", owner: 1, fields: []flowValue{{unknown: true}}},
			&flowObject{kind: "ctor", fields: []flowValue{{refs: []int{1}}, {unknown: unknown}}},
		)
		got := f.abstractDrive(flowValue{refs: []int{2}}, emptyFlowEnv(), rootFlowSite, nil, map[int]bool{})
		if got == unknown {
			t.Fatalf("unknown=%v: complete drive proof=%v", unknown, got)
		}
	}
}
