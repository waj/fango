package core

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestCoreReconstructsSynchronousScopeObligations(t *testing.T) {
	for _, phase := range []string{"acquisition", "release", "body"} {
		t.Run(phase, func(t *testing.T) {
			sup := &types.Supply{}
			b := types.NewBuiltins(sup)
			unit := &UnitLit{Ty: b.Unit}
			pause := &Suspend{Request: unit, Ty: b.Unit}
			control := types.Control{Transport: types.Machine}
			bracket := &Bracket{Scope: sup.FreshScope(), Resource: "resource", ResourceTy: b.Unit,
				Acquire: unit, Body: unit, Release: unit, Ty: b.Unit, Control: control}
			switch phase {
			case "acquisition":
				bracket.Acquire = pause
			case "release":
				bracket.Release = pause
			case "body":
				bracket.Body = pause
			}
			p := &Prog{Defs: []Def{{Name: types.ScopeBracketName,
				Type: &types.TFun{Arg: b.Unit, Ret: b.Unit, Control: control}, Control: control,
				Params: []string{"ignored"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Body: bracket}}}
			check := func(errs []error) {
				t.Helper()
				var found bool
				for _, err := range errs {
					if strings.Contains(err.Error(), "SUSPENDING RESOURCE CALLBACK") && strings.Contains(err.Error(), phase) {
						found = true
					} else {
						t.Errorf("unexpected error: %v", err)
					}
				}
				if found != (phase == "release") {
					t.Fatalf("%s suspension errors: %v", phase, errs)
				}
			}
			check(InferCaptures(p, b))
			check(LintMachineInput(p, b))
			// Even a forged contract that erases the callback suspension cannot
			// bypass the independent proof reconstructed from executable Core.
			if phase == "release" {
				index := 2
				p.Defs[0].CaptureContract.Body.Children[index].Kind = "scalar"
				errs := LintMachineInput(p, b)
				var stale, rejected bool
				for _, err := range errs {
					stale = stale || strings.Contains(err.Error(), "capture contract is stale")
					rejected = rejected || strings.Contains(err.Error(), "SUSPENDING RESOURCE CALLBACK")
				}
				if !stale || !rejected {
					t.Fatalf("forged contract accepted: %v", errs)
				}
			}
		})
	}
}

func TestSynchronousCallbackHandlerBoundaries(t *testing.T) {
	for _, mode := range []string{"outer abort", "inner abort", "outer resumptive"} {
		t.Run(mode, func(t *testing.T) {
			sup := &types.Supply{}
			b := types.NewBuiltins(sup)
			var next int
			node := func(kind string, children ...*types.CaptureFlow) *types.CaptureFlow {
				next++
				return &types.CaptureFlow{ID: next, Kind: kind, Type: b.Unit, Children: children}
			}
			unit := node("scalar")
			pause := node("suspend", unit)
			operation := node("exit")
			operation.Effects = []int{1}
			if mode == "outer resumptive" {
				operation.Kind = "perform"
			}
			// An ordinary helper keeps the suspension summary distinct from
			// its enclosing handler, including on fixed-point revisits.
			ref := node("global")
			ref.Name = "helper"
			call := node("call", ref)
			handler := node("handle", nil, call, nil)
			handler.Scope, handler.Effects = sup.FreshScope(), []int{1}
			handler.Clauses = []types.CaptureClause{{Index: 0, Body: pause}}
			scope := node("scope", unit, unit, operation)
			scope.Scope, scope.Name, scope.TypeArgs = sup.FreshScope(), "resource", []types.Type{b.Unit}
			root, helper := handler, scope
			if mode == "inner abort" {
				root, helper = scope, operation
				scope.Children[2] = handler
			}
			p := &Prog{Defs: []Def{
				{Name: "root", CaptureContract: &types.CaptureContract{Body: root}},
				{Name: "helper", CaptureContract: &types.CaptureContract{Effects: []int{1}, Body: helper}},
			}}
			errs := checkCaptureFlows(newCaptureAnalyzer(p, b))
			if (len(errs) == 0) != (mode == "outer abort") {
				t.Fatalf("%s: %v", mode, errs)
			}
			for _, err := range errs {
				if _, ok := err.(SuspensionError); !ok {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}
