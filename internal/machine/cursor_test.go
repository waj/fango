package machine

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/core/coretest"
	"github.com/waj/fango/internal/types"
)

func TestCursorAdvanceProofAndLiveness(t *testing.T) {
	p, b := coretest.CursorScope()
	adt := p.ADTs[0]
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	var block *Block
	for wi := range mp.Workers {
		for bi := range mp.Workers[wi].Blocks {
			candidate := &mp.Workers[wi].Blocks[bi]
			if _, ok := candidate.Term.(*CursorAdvance); ok {
				block = candidate
			}
		}
	}
	if block == nil {
		t.Fatal("no advancement instruction")
	}
	original := *block.Term.(*CursorAdvance)
	for _, test := range []struct {
		name   string
		damage func(*CursorAdvance)
		want   string
	}{
		{"missing access", func(c *CursorAdvance) { c.Access = 0 }, "exclusive"},
		{"wrong element", func(c *CursorAdvance) {
			c.Cursor = &core.VarRef{Name: "cursor", Local: true, Ty: &types.TCon{Name: types.CoroutineTypeName, Args: []types.Type{b.String, b.Unit, b.Unit, b.Unit}}}
		}, "invalid coroutine Step result"},
		{"missing descriptor", func(c *CursorAdvance) { c.Result = nil }, "invalid coroutine Step result"},
		{"missing constructor", func(c *CursorAdvance) {
			copy := *adt
			copy.Ctors = []*types.CtorInfo{nil, adt.Ctors[1], adt.Ctors[2]}
			c.Result = &copy
		}, "invalid coroutine Step constructors"},
	} {
		t.Run(test.name, func(t *testing.T) {
			damaged := original
			test.damage(&damaged)
			block.Term = &damaged
			if got := errorsText(Lint(mp)); !strings.Contains(got, test.want) {
				t.Fatalf("got %s, want %s", got, test.want)
			}
			block.Term = &original
		})
	}
	old := block.LiveOut
	block.LiveOut = []string{"cursor"}
	if got := errorsText(Lint(mp)); !strings.Contains(got, "LiveOut") {
		t.Fatalf("stale live storage accepted: %s", got)
	}
	block.LiveOut = old
	node := p.Defs[0].Body.(*core.CoroutineAdvance)
	node.Access = 0
	if got := errorsText(core.LintMachineInput(p, b)); !strings.Contains(got, "exclusive access proof") || !strings.Contains(got, "capture contract is stale") {
		t.Fatalf("Core accepted stale access: %s", got)
	}
}

func TestCursorScopeChecksCleanupOwnerAndSetupProof(t *testing.T) {
	p, b := coretest.CursorScope()
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	var openBlock, closeBlock *Block
	for wi := range mp.Workers {
		for bi := range mp.Workers[wi].Blocks {
			block := &mp.Workers[wi].Blocks[bi]
			switch block.Term.(type) {
			case *CursorOpen:
				openBlock = block
			case *CursorClose:
				closeBlock = block
			}
		}
	}
	if openBlock == nil || closeBlock == nil {
		t.Fatal("missing setup or closure transition")
	}
	originalOpen := *openBlock.Term.(*CursorOpen)
	originalClose := *closeBlock.Term.(*CursorClose)
	badClose := originalClose
	badClose.Scope++
	closeBlock.Term = &badClose
	if got := errorsText(Lint(mp)); !strings.Contains(got, "cleanup owner mismatch") {
		t.Fatalf("wrong owner accepted: %s", got)
	}
	closeBlock.Term = &PopCleanup{Next: originalClose.Next}
	if got := errorsText(Lint(mp)); !strings.Contains(got, "cleanup owner mismatch") {
		t.Fatalf("ordinary cleanup closed cursor: %s", got)
	}
	closeBlock.Term = &originalClose
	badOpen := originalOpen
	badOpen.Scope = 0
	openBlock.Term = &badOpen
	if got := errorsText(Lint(mp)); !strings.Contains(got, "invalid or reused cursor scope") {
		t.Fatalf("missing scope accepted: %s", got)
	}
	openBlock.Term = &originalOpen
}

func TestCoreSynchronousTraversalBoundaryRequiresProof(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage func(*core.Prog, *core.CoroutineScope)
		want   string
	}{
		{"missing ownership", func(_ *core.Prog, s *core.CoroutineScope) { s.Traversal = core.EffectInstance{} }, "invalid coroutine control owner"},
		{"stale scope", func(_ *core.Prog, s *core.CoroutineScope) { s.Traversal.Captures = types.ScopeCapture(s.Scope + 1) }, "invalid coroutine control owner"},
		{"unhandled residual", func(p *core.Prog, s *core.CoroutineScope) {
			failure := &types.EffectInfo{Unique: 1000, Name: "Test.Fail"}
			p.Effects = append(p.Effects, failure)
			fn := s.Consumer.Type().(*types.TFun)
			fn.Eff.Labels = append(fn.Eff.Labels, types.EffLabel{Unique: failure.Unique, Name: failure.Name, Abort: true})

			s.Control = types.Control{Transport: types.Exit}
		}, "stale coroutine boundary control"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, b := coretest.SynchronousCursorScope()
			if errs := core.InferCaptures(p, b); len(errs) != 0 {
				t.Fatal(errs)
			}
			scope := p.Defs[1].Body.(*core.CoroutineScope)
			test.damage(p, scope)
			if got := errorsText(core.LintMachineInput(p, b)); !strings.Contains(got, test.want) {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}
