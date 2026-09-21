package core

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestYieldOwnerFollowsCallbackCaptureSubstitution(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	variable, scope := sup.FreshCapture(), sup.FreshScope()
	owner := EffectInstance{Unique: sup.NextUnique(), Name: types.StreamYieldEffectName,
		Args: []types.Type{b.Int}, Captures: types.VarCapture(variable), Control: types.Control{Transport: types.Machine}}
	pause := &Suspend{Owner: owner, Request: &IntLit{Val: 7, Ty: b.Int}, Ty: b.Unit}
	adapted := SubstituteCaptureVars(pause, map[types.CaptureVar]types.CaptureSet{variable: types.ScopeCapture(scope)}, nil).(*Suspend)
	if !types.EqualCaptures(adapted.Owner.Captures, types.ScopeCapture(scope)) {
		t.Fatalf("adapted yield kept stale evidence: %+v", adapted.Owner)
	}
	if !types.EqualCaptures(pause.Owner.Captures, types.VarCapture(variable)) {
		t.Fatal("adapting a callback mutated its original owner contract")
	}
}
