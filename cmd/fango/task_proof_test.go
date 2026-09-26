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

func TestTaskCoreProofs(t *testing.T) {
	for _, test := range []struct {
		name, want string
		damage     func(*core.Prog, *core.TaskSpawn)
	}{
		{"missing intrinsic", "intrinsic is not declared", func(p *core.Prog, e *core.TaskSpawn) { delete(p.Intrinsics, types.TaskSpawnName) }},
		{"unknown worker", "closed named worker", func(p *core.Prog, e *core.TaskSpawn) { e.Worker = "unknown" }},
		{"forged worker type", "worker type mismatch", func(p *core.Prog, e *core.TaskSpawn) { e.WorkerType = e.Input.Type() }},
		{"forged context constructor", "constructor representation mismatch", func(p *core.Prog, e *core.TaskSpawn) {
			c := *e.ContextCtor
			c.Name = "Forged.Context"
			e.ContextCtor = &c
		}},
		{"missing constructor type", "constructor type mismatch", func(p *core.Prog, e *core.TaskSpawn) {
			c := *e.ScopeCtor
			c.Result = nil
			e.ScopeCtor = &c
		}},
		{"function result", "result type mismatch", func(p *core.Prog, e *core.TaskSpawn) {
			con := *e.Ty.(*types.TCon)
			con.Args = []types.Type{e.WorkerType}
			e.Ty = &con
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			p, ck, ok := compileFile(filepath.Join("..", "..", "testdata", "run", "task_named_workers.fango"), &diagnostics)
			if !ok {
				t.Fatal(diagnostics.String())
			}
			var spawn *core.TaskSpawn
			for _, d := range p.Defs {
				core.Inspect(d.Body, func(e core.Expr) {
					if e, ok := e.(*core.TaskSpawn); ok {
						spawn = e
					}
				})
			}
			if spawn == nil {
				t.Fatal("missing spawn")
			}
			test.damage(p, spawn)
			if got := fmt.Sprint(core.Lint(p, ck.B)); !strings.Contains(got, test.want) {
				t.Fatalf("errors %s, want %q", got, test.want)
			}
		})
	}
}
