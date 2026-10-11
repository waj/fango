package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func TestFailureReportCoreProofs(t *testing.T) {
	for _, test := range []struct {
		name, want string
		damage     func(*core.Prog)
	}{
		{"renamed nominal descriptor", "stale nominal type identity", func(p *core.Prog) {
			for i := range p.Defs {
				p.Defs[i].Body = core.Rewrite(p.Defs[i].Body, func(ty types.Type) types.Type {
					if con, ok := ty.(*types.TCon); ok && con.Name == "Int" {
						copy := *con
						copy.Name = "String"
						return &copy
					}
					return ty
				}, func(e core.Expr) core.Expr { return e })
			}
		}},
		{"missing argument packaging", "Maybe packaging proof", func(p *core.Prog) {
			for i := range p.Defs {
				if e, ok := p.Defs[i].Body.(*core.FailureInspect); ok && e.Name == types.FailureArgumentName {
					e.Result = nil
				}
			}
		}},
		{"missing declaration", "outside its declared intrinsic", func(p *core.Prog) { delete(p.Intrinsics, types.FailureArgumentName) }},
		{"missing report binding", "suppressed failure binding", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == types.FailAttemptReportName {
					p.Defs[i].Body.(*core.Handle).Clauses[0].SuppressedParam = ""
				}
			}
		}},
		{"stale report summary", "result capture summary is stale", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == types.FailAttemptReportName {
					p.Defs[i].ResultCaptures = types.ScopeCapture(9999)
				}
			}
		}},
		{"missing diagnostic capture policy", "diagnostic capture policy", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == types.FailAttemptDiagnosticName {
					p.Defs[i].Body.(*core.Handle).CaptureTrace = false
				}
			}
		}},
		{"misplaced diagnostic capture policy", "diagnostic capture policy", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == types.FailAttemptReportName {
					p.Defs[i].Body.(*core.Handle).CaptureTrace = true
				}
			}
		}},
		{"missing diagnostic trace binding", "diagnostic trace binding", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == types.FailAttemptDiagnosticName {
					p.Defs[i].Body.(*core.Handle).Clauses[0].TraceParam = ""
				}
			}
		}},
		{"wrong diagnostic trace type", "diagnostic frame list type", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == types.FailAttemptDiagnosticName {
					clause := &p.Defs[i].Body.(*core.Handle).Clauses[0]
					clause.TraceType = clause.ParamTypes[0]
				}
			}
		}},
		{"missing trace inspection packaging", "Maybe packaging proof", func(p *core.Prog) {
			for i := range p.Defs {
				if e, ok := p.Defs[i].Body.(*core.FailureInspect); ok && e.Name == types.FailureTraceName {
					e.Result = nil
				}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			p, ck, ok := compileFile(filepath.Join("..", "..", "testdata", "run", "failure_reports.fango"), &diagnostics)
			if !ok {
				t.Fatal(diagnostics.String())
			}
			test.damage(p)
			if got := fmt.Sprint(core.Lint(p, ck.B)); !strings.Contains(got, test.want) {
				t.Fatalf("errors %s, want %q", got, test.want)
			}
		})
	}
}

func TestFailureDiagnosticCaptureEmission(t *testing.T) {
	files := emittedProject(t, filepath.Join("..", "..", "testdata", "suite", "FailDiagnostic.fango"))
	fail := string(generatedFile(t, files, "modules/Fail/module.go"))
	if !strings.Contains(fail, "CaptureTrace: true") || !strings.Contains(fail, "CaptureTrace: false") || !strings.Contains(fail, "fangort.TraceList[") {
		t.Fatal("diagnostic and ordinary handlers lost their independent capture policies")
	}
	entry := string(generatedFile(t, files, "entries/FailDiagnostic/main.go"))
	if !strings.Contains(entry, "fangort.CaptureExit(") || strings.Contains(entry, "fangort.Capture(") {
		t.Fatal("abort emission bypasses the conditional capture helper")
	}
}
