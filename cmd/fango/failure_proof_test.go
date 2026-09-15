package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
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
		{"stale report contract", "capture contract is stale", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == types.FailAttemptReportName {
					p.Defs[i].CaptureContract.Body.Clauses[0].Suppressed = ""
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

func TestFailureReportMachineProof(t *testing.T) {
	var diagnostics bytes.Buffer
	p, ck, ok := compileFile(filepath.Join("..", "..", "testdata", "run", "failure_reports.fango"), &diagnostics)
	if !ok {
		t.Fatal(diagnostics.String())
	}
	mp, errs := machineir.Lower(p, ck.B)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	found := false
	for _, w := range mp.Workers {
		for _, block := range w.Blocks {
			if h, ok := block.Term.(*machineir.Handle); ok && w.Name == types.FailAttemptReportName {
				for _, cl := range h.Clauses {
					for i := range mp.Workers {
						worker := &mp.Workers[i]
						if worker.Name == cl.Worker {
							worker.Params[len(worker.Params)-1].Ty = ck.B.Int
							found = true
						}
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("no Machine report clause")
	}
	if got := fmt.Sprint(machineir.Lint(mp)); !strings.Contains(got, "suppressed payload binding") {
		t.Fatalf("errors %s", got)
	}
}
