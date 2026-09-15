package machine

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/core/coretest"
	"github.com/waj/fango/internal/types"
)

func TestCursorAdvanceProofAndLiveness(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	a := sup.FreshRigid(types.General)
	con := &types.TCon{Unique: sup.NextUnique(), Name: "Maybe.Maybe", Args: []types.Type{a}}
	nothing := &types.CtorInfo{Name: "Maybe.Nothing", Index: 0, Result: con}
	just := &types.CtorInfo{Name: "Maybe.Just", Index: 1, Fields: []types.Type{a}, Result: con}
	adt := &types.ADTInfo{Con: con, Params: []*types.TVar{a}, Ctors: []*types.CtorInfo{nothing, just}}
	result := &types.TCon{Unique: con.Unique, Name: con.Name, Args: []types.Type{b.Int}}
	cursor := &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName, Args: []types.Type{b.Int, b.Unit}}
	control := types.Control{Transport: types.Machine}
	fn := &types.TFun{Arg: cursor, Ret: result, Control: control}
	p := &core.Prog{Intrinsics: map[string]bool{types.IteratorNextName: true}, ADTs: []*types.ADTInfo{adt}, Defs: []core.Def{
		{Name: "producer", Type: b.Unit, Control: control, Body: &core.Seq{
			First: &core.Suspend{Request: &core.IntLit{Val: 42, Ty: b.Int}, Ty: b.Unit}, Then: &core.UnitLit{Ty: b.Unit}, Ty: b.Unit}},
		{Name: types.IteratorNextName, Type: fn, Params: []string{"cursor"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: control,
			Body: &core.IteratorNext{Cursor: &core.VarRef{Name: "cursor", Local: true, Ty: cursor}, Result: adt, Access: types.ExclusiveAdvance, Ty: result}},
	}}
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
			c.Cursor = &core.VarRef{Name: "cursor", Local: true, Ty: &types.TCon{Name: types.IteratorTypeName, Args: []types.Type{b.String, b.Unit}}}
		}, "invalid Maybe"},
		{"missing descriptor", func(c *CursorAdvance) { c.Result = nil }, "invalid Maybe"},
		{"missing constructor", func(c *CursorAdvance) { copy := *adt; copy.Ctors = []*types.CtorInfo{nil, just}; c.Result = &copy }, "invalid Maybe constructors"},
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
	node := p.Defs[1].Body.(*core.IteratorNext)
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
		damage func(*core.Prog, *core.IteratorScope)
		want   string
	}{
		{"missing ownership", func(_ *core.Prog, s *core.IteratorScope) { s.Traversal = core.EffectInstance{} }, "disagrees with consumer"},
		{"stale scope", func(_ *core.Prog, s *core.IteratorScope) { s.Traversal.Captures = types.ScopeCapture(s.Scope + 1) }, "invalid Traversal ownership"},
		{"unhandled residual", func(p *core.Prog, s *core.IteratorScope) {
			failure := &types.EffectInfo{Unique: 1000, Name: "Test.Fail"}
			p.Effects = append(p.Effects, failure)
			fn := s.Consumer.Type().(*types.TFun)
			fn.Eff.Labels = append(fn.Eff.Labels, types.EffLabel{Unique: failure.Unique, Name: failure.Name, Abort: true})
		}, "stale residual Traversal control"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, b := coretest.SynchronousCursorScope()
			if errs := core.InferCaptures(p, b); len(errs) != 0 {
				t.Fatal(errs)
			}
			scope := p.Defs[1].Body.(*core.IteratorScope)
			test.damage(p, scope)
			if got := errorsText(core.LintMachineInput(p, b)); !strings.Contains(got, test.want) {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}
