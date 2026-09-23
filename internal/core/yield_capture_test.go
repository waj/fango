package core

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestEffectOwnerFollowsCallbackCaptureSubstitution(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	variable, scope := sup.FreshCapture(), sup.FreshScope()
	owner := EffectInstance{Unique: sup.NextUnique(), Name: "Test.Pause",
		Args: []types.Type{b.Int}, Captures: types.VarCapture(variable), Control: types.Control{Transport: types.Machine}}
	pause := &Perform{Effect: owner, Args: []Expr{&IntLit{Val: 7, Ty: b.Int}}, Ty: b.Unit}
	adapted := SubstituteCaptureVars(pause, map[types.CaptureVar]types.CaptureSet{variable: types.ScopeCapture(scope)}, nil).(*Perform)
	if !types.EqualCaptures(adapted.Effect.Captures, types.ScopeCapture(scope)) {
		t.Fatalf("adapted yield kept stale evidence: %+v", adapted.Effect)
	}
	if !types.EqualCaptures(pause.Effect.Captures, types.VarCapture(variable)) {
		t.Fatal("adapting a callback mutated its original owner contract")
	}
}
