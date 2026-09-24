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

func TestWorkCoreAndExecutionCodecProofs(t *testing.T) {
	entry := filepath.Join("..", "..", "testdata", "run", "work_package.fango")
	result, diagnostics, err := (&check.Session{DisableObjectCache: true}).Compile(entry)
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
		{"missing row", "missing work effect-budget proof", func(p *core.Prog) { workNode(p, "pack").SourceRow = nil }},
		{"stale row", "stale work effect-budget proof", func(p *core.Prog) { workNode(p, "pack").SourceRow = types.Row{} }},
		{"wrong protocol", "invalid work package protocol", func(p *core.Prog) { workNode(p, "pack").Ty = result.Checker.B.Unit }},
		{"missing definition source", "missing source effect contract", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == "main" {
					p.Defs[i].SourceType = nil
				}
			}
		}},
		{"missing lambda source", "missing lambda source effect contract", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name != "main" {
					continue
				}
				core.Inspect(p.Defs[i].Body, func(e core.Expr) {
					if lambda, ok := e.(*core.Lambda); ok {
						lambda.SourceType = nil
					}
				})
			}
		}},
		{"missing source call", "missing work source call proof", func(p *core.Prog) {
			for i := range p.Defs {
				core.Inspect(p.Defs[i].Body, func(e core.Expr) {
					if app, ok := e.(*core.App); ok {
						if ref, ok := app.Callee.(*core.VarRef); ok && ref.Name == types.WorkPackName {
							app.SourceType = nil
						}
					}
				})
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := execcodec.Decode(encoded)
			if err != nil {
				t.Fatal(err)
			}
			p := payload.Program
			tc.damage(p)
			var messages []string
			for _, err := range core.LintMachineInput(p, result.Checker.B) {
				messages = append(messages, err.Error())
			}
			got := strings.Join(messages, "\n")
			if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("got %s, want %q", got, tc.want)
			}
		})
	}
}

func TestWorkRejectsForgedPackageSourceRow(t *testing.T) {
	entry := filepath.Join("..", "..", "testdata", "run", "work_latent.fango")
	result, diagnostics, err := (&check.Session{DisableObjectCache: true}).Compile(entry)
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
			if !ok || ref.Name != types.WorkPackName {
				return
			}
			first := *app.SourceType.(*types.TFun)
			second := *first.Ret.(*types.TFun)
			cursor := *second.Arg.(*types.TCon)
			cursor.Args = append([]types.Type(nil), cursor.Args...)
			cursor.Args[3] = types.Row{}
			second.Arg, second.Eff = &cursor, types.Row{}
			first.Ret = &second
			app.SourceType = &first
			changed = true
		})
	}
	if !changed {
		t.Fatal("missing Runtime.Work.pack call")
	}
	// Rebuild the generic ownership certificate: the independent check must
	// reject the forged row even when the other certificate agrees with it.
	errors := core.InferCaptures(result.Program, result.Checker.B)
	errors = append(errors, core.LintMachineInput(result.Program, result.Checker.B)...)
	for _, err := range errors {
		if strings.Contains(err.Error(), "actual coroutine residual") {
			return
		}
	}
	t.Fatalf("forged source row accepted: %v", errors)
}

func workNode(p *core.Prog, kind string) *core.Work {
	var found *core.Work
	for _, d := range p.Defs {
		core.Inspect(d.Body, func(e core.Expr) {
			if w, ok := e.(*core.Work); ok && w.Kind == kind {
				found = w
			}
		})
	}
	if found == nil {
		panic("missing work intrinsic")
	}
	return found
}
