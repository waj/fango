package main

import (
	"bytes"
	"fmt"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletionCoreProofs(t *testing.T) {
	for _, test := range []struct {
		name, want string
		damage     func(*core.Prog)
	}{
		{"missing declaration", "outside its declared intrinsic", func(p *core.Prog) { delete(p.Intrinsics, types.CompletionCaptureName) }},
		{"missing result packaging", "completion Maybe proof", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == types.CompletionFailureName {
					p.Defs[i].Body.(*core.Completion).Result = nil
				}
			}
		}},
		{"missing current evidence", "residual row argument", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == types.CompletionReplayName {
					p.Defs[i].Body.(*core.Completion).Row = nil
				}
			}
		}},
		{"stale control", "completion control proof", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == types.CompletionCaptureName {
					p.Defs[i].Body.(*core.Completion).Control = types.Control{Transport: types.Exit}
				}
			}
		}},
		{"stale capture flow", "capture contract is stale", func(p *core.Prog) {
			for i := range p.Defs {
				if p.Defs[i].Name == types.CompletionCaptureName {
					p.Defs[i].CaptureContract.Body.Kind = "scalar"
				}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			p, ck, ok := compileFile(filepath.Join("..", "..", "testdata", "run", "completion_protocol.fango"), &diagnostics)
			if !ok {
				t.Fatal(diagnostics.String())
			}
			test.damage(p)
			if got := fmt.Sprint(core.Lint(p, ck.B)); !strings.Contains(got, test.want) {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}

func TestCompletionMachineProof(t *testing.T) {
	var diagnostics bytes.Buffer
	p, ck, ok := compileFile(filepath.Join("..", "..", "testdata", "run", "completion_machine.fango"), &diagnostics)
	if !ok {
		t.Fatal(diagnostics.String())
	}
	program, errs := machine.Lower(p, ck.B)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	found := false
	for i := range program.Workers {
		worker := &program.Workers[i]
		if worker.Name == types.CompletionReplayName {
			for _, block := range worker.Blocks {
				if evaluation, ok := block.Term.(*machine.Eval); ok {
					if node, ok := evaluation.Value.(*core.Completion); ok {
						row := node.Row
						node.Row = nil
						if got := fmt.Sprint(machine.Lint(program)); !strings.Contains(got, "row argument") {
							t.Fatalf("missing Machine replay evidence accepted: %s", got)
						}
						node.Row = row
					}
				}
			}
		}
		if worker.Name == types.CompletionCaptureName {
			for _, block := range worker.Blocks {
				if call, ok := block.Term.(*machine.Call); ok && call.Capture {
					call.Tail = true
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("no Machine completion boundary")
	}
	if got := fmt.Sprint(machine.Lint(program)); !strings.Contains(got, "result disagrees") {
		t.Fatalf("invalid tail-consuming completion accepted: %s", got)
	}
}
