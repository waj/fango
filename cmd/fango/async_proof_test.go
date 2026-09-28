package main

import (
	"bytes"
	"fmt"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"path/filepath"
	"strings"
	"testing"
)

func TestAsyncCoreProofs(t *testing.T) {
	for _, test := range []struct {
		name, want string
		damage     func(*core.Prog, *core.AsyncLaunch, *core.AsyncRebase)
	}{
		{"missing supervisor intrinsic", "malformed Async supervisor", func(p *core.Prog, _ *core.AsyncLaunch, _ *core.AsyncRebase) {
			delete(p.Intrinsics, types.AsyncSuperviseName)
		}},
		{"missing launch intrinsic", "malformed Async launch", func(p *core.Prog, _ *core.AsyncLaunch, _ *core.AsyncRebase) {
			delete(p.Intrinsics, types.AsyncLaunchName)
		}},
		{"forged scope", "scope representation mismatch", func(_ *core.Prog, e *core.AsyncLaunch, _ *core.AsyncRebase) {
			c := *e.ScopeCtor
			c.Name = "Forged"
			e.ScopeCtor = &c
		}},
		{"forged task", "task representation mismatch", func(_ *core.Prog, e *core.AsyncLaunch, _ *core.AsyncRebase) {
			c := *e.TaskCtor
			c.Name = "Forged"
			e.TaskCtor = &c
		}},
		{"wrong result constructor", "constructor role mismatch", func(_ *core.Prog, e *core.AsyncLaunch, _ *core.AsyncRebase) { e.CompletedCtor = e.CancelledCtor }},
		{"missing rebase intrinsic", "malformed Async rebase", func(p *core.Prog, _ *core.AsyncLaunch, _ *core.AsyncRebase) {
			delete(p.Intrinsics, types.AsyncRebaseName)
		}},
		{"missing failure evidence", "matching child Async/Fail evidence", func(_ *core.Prog, _ *core.AsyncLaunch, e *core.AsyncRebase) { e.Call.EvidenceArgs = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			p, ck, ok := compileFile(filepath.Join("..", "..", "testdata", "run", "async_outcomes.fango"), &diagnostics)
			if !ok {
				t.Fatal(diagnostics.String())
			}
			var launch *core.AsyncLaunch
			var rebase *core.AsyncRebase
			for _, d := range p.Defs {
				core.Inspect(d.Body, func(e core.Expr) {
					switch e := e.(type) {
					case *core.AsyncLaunch:
						launch = e
					case *core.AsyncRebase:
						rebase = e
					}
				})
			}
			if launch == nil || rebase == nil {
				t.Fatal("missing Async boundary")
			}
			test.damage(p, launch, rebase)
			if got := fmt.Sprint(core.Lint(p, ck.B)); !strings.Contains(got, test.want) {
				t.Fatalf("errors %s, want %s", got, test.want)
			}
		})
	}
}
