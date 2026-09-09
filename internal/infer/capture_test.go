package infer

import (
	"testing"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

func TestCheckpointRollsBackCaptureSummaries(t *testing.T) {
	sup := &types.Supply{}
	ck := NewChecker(sup, types.NewBuiltins(sup), NewEnv())
	ck.SetCaptureSummary("kept", []types.CaptureVar{1}, types.VarCapture(1))
	ck.ScopeSpans[1] = source.Span{Start: 1, End: 2}
	rollback := ck.Checkpoint()
	ck.SetCaptureSummary("failed", []types.CaptureVar{2}, types.ScopeCapture(3))
	ck.ScopeSpans[2] = source.Span{Start: 3, End: 4}
	rollback()
	if _, ok := ck.CaptureSummaries["failed"]; ok {
		t.Fatal("failed input's capture summary survived rollback")
	}
	if got := ck.CaptureSummaries["kept"].Captures; !got.HasVar(1) {
		t.Fatalf("pre-checkpoint summary was not restored: %#v", got)
	}
	if _, ok := ck.ScopeSpans[2]; ok {
		t.Fatal("failed input's scope provenance survived rollback")
	}
}

func TestInstantiateCapturesFreshensSchemeVariables(t *testing.T) {
	sup := &types.Supply{}
	ck := NewChecker(sup, types.NewBuiltins(sup), NewEnv())
	g := &generator{ck: ck}
	sch := types.Scheme{CaptureVars: []types.CaptureVar{7}, Captures: types.VarCapture(7)}
	a, b := g.instantiateCaptures(sch), g.instantiateCaptures(sch)
	if len(a.Vars) != 1 || len(b.Vars) != 1 || a.Vars[0] == b.Vars[0] || a.Vars[0] == 7 || b.Vars[0] == 7 {
		t.Fatalf("capture instantiations were not fresh: %#v %#v", a, b)
	}
}
