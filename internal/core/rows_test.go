package core

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/types"
)

func rowProofFixture() (*Prog, *Lambda, *App) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	fn := &types.TFun{Arg: b.Unit, Ret: b.Int, OpenRow: true, Control: types.Control{Polymorphic: true}}
	call := &App{CalleeKind: Value, Callee: &VarRef{Name: "callback", Local: true, Ty: fn}, Args: []Expr{&UnitLit{Ty: b.Unit}}, Ty: b.Int, Row: &RowArgument{From: 2}}
	lam := &Lambda{Param: "unit", Ty: fn, RowParam: 2, Body: call}
	def := Def{Name: "factory", Params: []string{"callback"}, Type: &types.TFun{Arg: fn, Ret: fn}, Body: lam}
	return &Prog{Defs: []Def{def}}, lam, call
}

func TestResidualRowProofRejectsMissingStaleAndNonlexicalArguments(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Prog, *Lambda, *App)
		want string
	}{
		{"missing binder", func(_ *Prog, lam *Lambda, _ *App) { lam.RowParam = 0 }, "binder disagrees"},
		{"missing argument", func(_ *Prog, _ *Lambda, call *App) { call.Row = nil }, "missing or unexpected"},
		{"unavailable argument", func(_ *Prog, _ *Lambda, call *App) { call.Row.From = 99 }, "unavailable row"},
		{"unavailable effect", func(_ *Prog, _ *Lambda, call *App) { call.Row.Effects = []EffectInstance{{Unique: 10, Name: "Reader"}} }, "lexical activation"},
		{"closed factory binder", func(p *Prog, _ *Lambda, _ *App) { p.Defs[0].RowParam = 3 }, "binder disagrees"},
		{"duplicate binder", func(p *Prog, lam *Lambda, _ *App) {
			copy := p.Defs[0]
			copy.Name = "other"
			copy.Body = lam
			p.Defs = append(p.Defs, copy)
		}, "duplicate residual"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, lam, call := rowProofFixture()
			if errs := CheckRowEvidence(p); len(errs) != 0 {
				t.Fatalf("valid proof: %v", errs)
			}
			test.edit(p, lam, call)
			found := false
			for _, err := range CheckRowEvidence(p) {
				found = found || strings.Contains(err.Error(), test.want)
			}
			if !found {
				t.Fatalf("missing %q diagnostic", test.want)
			}
		})
	}
}

func TestResidualRowsAndDeferredEvidenceSurviveRewriteAndContractChecks(t *testing.T) {
	p, lam, call := rowProofFixture()
	ev := EffectInstance{Unique: 10, Name: "Reader", Captures: types.VarCapture(3)}
	lam.RowEffects = []EffectInstance{ev}
	call.Row.Effects = []EffectInstance{ev}
	if errs := CheckRowEvidence(p); len(errs) != 0 {
		t.Fatal(errs)
	}
	p.Defs[0].CaptureContract = inferCaptureContract(&p.Defs[0])
	rewritten := SubstituteCaptureVars(lam, map[types.CaptureVar]types.CaptureSet{3: types.VarCapture(4)}).(*Lambda)
	if rewritten.RowEffects[0].Captures.Vars[0] != 4 || rewritten.Body.(*App).Row.Effects[0].Captures.Vars[0] != 4 || lam.RowEffects[0].Captures.Vars[0] != 3 {
		t.Fatal("row evidence substitution was lost or mutated its source")
	}
	call.Row.From = 0
	if CaptureContractCurrent(&p.Defs[0]) {
		t.Fatal("stale residual flow contract accepted")
	}
}

func TestFreeRowsExcludeInvocationBinders(t *testing.T) {
	_, lam, call := rowProofFixture()
	if len(FreeRows(lam)) != 0 {
		t.Fatal("invocation row captured")
	}
	call.Row.From = 1
	if !FreeRows(lam)[1] || len(FreeRows(lam)) != 1 {
		t.Fatal("outer row reference lost")
	}
}

func TestStoredCallbackCannotRetagResidualABI(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	callback := &types.TFun{Arg: b.Unit, Ret: b.Int, OpenRow: true}
	ref := &VarRef{Name: "callback", Local: true, Ty: callback}
	p := &Prog{Defs: []Def{{Name: "identity", Type: &types.TFun{Arg: callback, Ret: callback}, Params: []string{"callback"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Body: ref}}}
	if errs := InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	if errs := Lint(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	closed := *callback
	closed.OpenRow = false
	if EqualEvidenceActivation(EffectInstance{Unique: 1, Args: []types.Type{callback}}, EffectInstance{Unique: 1, Args: []types.Type{&closed}}) {
		t.Fatal("residual evidence accepted a retagged callback argument")
	}
	ref.Ty = &closed
	found := false
	for _, err := range Lint(p, b) {
		found = found || strings.Contains(err.Error(), "changes its binding type")
	}
	if !found {
		t.Fatal("a stored open-row callback was retagged to a closed ABI")
	}
}
