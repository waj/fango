package core_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/execcodec"
	"github.com/waj/fango/internal/types"
)

func TestDynamicCoroutineProofsSurviveSerialization(t *testing.T) {
	result, diagnostics, err := (&check.Session{DisableObjectCache: true}).Compile(filepath.Join("..", "..", "testdata", "run", "coroutine_registration.fango"))
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("compile: %v %v", err, diagnostics)
	}
	encoded, err := execcodec.Encode(&execcodec.Payload{Program: result.Program})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want string
		damage     func(*core.Prog)
	}{
		{"round trip", "", func(*core.Prog) {}},
		{"create budget", "invalid dynamic coroutine budget", func(p *core.Prog) { workNode(p, "create").SourceRow = nil }},
		{"create destination", "invalid dynamic coroutine destination", func(p *core.Prog) { workNode(p, "create").Args[0] = &core.UnitLit{Ty: result.Checker.B.Unit} }},
		{"registration budget", "invalid registered work budget", func(p *core.Prog) { workNode(p, "register").SourceRow = types.Row{} }},
		{"registration result", "invalid registered work protocol", func(p *core.Prog) { workNode(p, "register").Ty = result.Checker.B.Unit }},
		{"registration facet", "invalid coroutine registration protocol", func(p *core.Prog) { workNode(p, "registration").Ty = result.Checker.B.Unit }},
		{"scope source", "invalid dynamic scope source contract", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == types.CoroutineScopeName {
					p.Defs[i].SourceType = nil
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := execcodec.Decode(encoded)
			if err != nil {
				t.Fatal(err)
			}
			tc.damage(decoded.Program)
			var messages []string
			for _, err := range core.LintMachineInput(decoded.Program, result.Checker.B) {
				messages = append(messages, err.Error())
			}
			got := strings.Join(messages, "\n")
			if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("got %s, want %q", got, tc.want)
			}
		})
	}
}

func TestDynamicRegistrationRejectsForgedSourceBudget(t *testing.T) {
	result, diagnostics, err := (&check.Session{DisableObjectCache: true}).Compile(filepath.Join("..", "..", "testdata", "run", "coroutine_registration.fango"))
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("compile: %v %v", err, diagnostics)
	}
	changed := false
	for i := range result.Program.Defs {
		core.Inspect(result.Program.Defs[i].Body, func(e core.Expr) {
			app, ok := e.(*core.App)
			if !ok {
				return
			}
			ref, ok := app.Callee.(*core.VarRef)
			if !ok || ref.Name != types.WorkRegisterName {
				return
			}
			first := *app.SourceType.(*types.TFun)
			second := *first.Ret.(*types.TFun)
			factory := *second.Arg.(*types.TFun)
			body := *factory.Ret.(*types.TFun)
			var protocol []types.EffLabel
			for _, label := range body.Eff.Labels {
				if label.Name == types.CoroutineSuspensionName {
					protocol = append(protocol, label)
				}
			}
			body.Eff = types.Row{Labels: protocol}
			factory.Ret = &body
			second.Arg, second.Eff = &factory, types.Row{}
			first.Ret = &second
			app.SourceType = &first
			changed = true
		})
	}
	if !changed {
		t.Fatal("missing registration")
	}
	errors := core.InferCaptures(result.Program, result.Checker.B)
	errors = append(errors, core.LintMachineInput(result.Program, result.Checker.B)...)
	for _, err := range errors {
		if strings.Contains(err.Error(), "actual producer residual") {
			return
		}
	}
	t.Fatalf("forged registration accepted: %v", errors)
}
