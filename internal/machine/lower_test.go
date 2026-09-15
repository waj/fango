package machine

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func TestLowerSplitsSuspensionsAndComputesMinimalFrame(t *testing.T) {
	_, b := testBuiltins()
	x := &core.VarRef{Name: "x", Local: true, Ty: b.Int}
	body := &core.Let{Name: "x", Rhs: suspend(b, intLit(b, 1)), Ty: b.Int,
		Body: &core.Let{Name: "y", Rhs: suspend(b, x), Ty: b.Int, Body: x}}
	p := &core.Prog{Defs: []core.Def{{Name: "main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: body}}}

	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("Lower rejected checked machine Core:\n%s", errorsText(errs))
	}
	if len(mp.Workers) != 1 {
		t.Fatalf("got %d workers, want 1", len(mp.Workers))
	}
	w := mp.Workers[0]
	if w.Entry != 3 || len(w.Blocks) != 4 {
		t.Fatalf("entry/blocks = %d/%d, want 3/4", w.Entry, len(w.Blocks))
	}
	if got := localNames(w.Frame); len(got) != 1 || got[0] != "x" {
		t.Fatalf("frame locals = %v, want [x]", got)
	}
	first, ok := w.Blocks[w.Entry].Term.(*Suspend)
	if !ok || first.Bind.Name != "x" || first.Next != 2 {
		t.Fatalf("entry terminator = %#v, want suspension defining x -> block 2", w.Blocks[w.Entry].Term)
	}
	second, ok := w.Blocks[2].Term.(*Suspend)
	if !ok || second.Bind.Name != "y" || second.Next != 1 {
		t.Fatalf("second terminator = %#v, want suspension defining y -> block 1", w.Blocks[2].Term)
	}
	if got := w.Blocks[2].LiveOut; len(got) != 1 || got[0] != "x" {
		t.Fatalf("second suspension LiveOut = %v, want [x]", got)
	}
}

func TestLowerSharesBranchContinuation(t *testing.T) {
	_, b := testBuiltins()
	body := &core.Let{Name: "x", Rhs: &core.If{
		Cond: &core.BoolLit{Val: true, Ty: b.Bool},
		Then: suspend(b, intLit(b, 1)), Else: suspend(b, intLit(b, 2)), Ty: b.Int,
	}, Body: &core.VarRef{Name: "x", Local: true, Ty: b.Int}, Ty: b.Int}
	p := &core.Prog{Defs: []core.Def{{Name: "main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: body}}}

	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("Lower: %s", errorsText(errs))
	}
	w := mp.Workers[0]
	branch, ok := w.Blocks[w.Entry].Term.(*Branch)
	if !ok {
		t.Fatalf("entry = %T, want Branch", w.Blocks[w.Entry].Term)
	}
	thenSuspend := w.Blocks[branch.Then].Term.(*Suspend)
	elseSuspend := w.Blocks[branch.Else].Term.(*Suspend)
	if thenSuspend.Next != elseSuspend.Next {
		t.Fatalf("branch continuations differ: %d and %d", thenSuspend.Next, elseSuspend.Next)
	}
}

func TestLowerClosesMachineIslandThroughPolymorphicWorker(t *testing.T) {
	sup, b := testBuiltins()
	poly := types.Control{Polymorphic: true}
	helperTy := &types.TFun{Arg: b.Int, Eff: types.Row{}, Ret: b.Int, Control: poly}
	helper := core.Def{Name: "helper", Type: helperTy, Params: []string{"n"},
		ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: poly,
		Body: suspend(b, &core.VarRef{Name: "n", Local: true, Ty: b.Int})}
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: "helper", Ty: helperTy},
		Args: []core.Expr{intLit(b, 7)}, Ty: b.Int, Control: poly}
	main := core.Def{Name: "main", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: call}
	p := &core.Prog{Defs: []core.Def{helper, main}}

	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("Lower: %s", errorsText(errs))
	}
	if len(mp.Workers) != 2 || mp.Workers[0].Name != "helper" || mp.Workers[1].Name != "main" {
		t.Fatalf("selected workers = %v, want [helper main]", workerNames(mp))
	}
	mainWorker := mp.Workers[1]
	callTerm, ok := mainWorker.Blocks[mainWorker.Entry].Term.(*Call)
	if !ok || callTerm.Callee != "helper" || !callTerm.Tail {
		t.Fatalf("main entry = %#v, want tail call to helper", mainWorker.Blocks[mainWorker.Entry].Term)
	}
}

func TestLowerLeavesDirectProgramOutsideMachineIR(t *testing.T) {
	_, b := testBuiltins()
	p := &core.Prog{Defs: []core.Def{{Name: "main", Type: b.Int, Body: intLit(b, 1)}}}
	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("Lower: %s", errorsText(errs))
	}
	if len(mp.Workers) != 0 {
		t.Fatalf("lowered %d direct workers", len(mp.Workers))
	}
}

func TestLowerDoesNotRootUnselectedPolymorphicClosures(t *testing.T) {
	sup, b := testBuiltins()
	callback := &types.TFun{Arg: b.Int, Ret: b.Int, Control: types.Control{Polymorphic: true}}
	factory := core.Def{Name: "Library.factory", Owner: "Library", Type: &types.TFun{Arg: b.Int, Ret: callback},
		Params: []string{"value"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()},
		Body: &core.Lambda{Param: "ignored", ParamCapture: sup.FreshCapture(), Ty: callback,
			Body: &core.VarRef{Name: "value", Local: true, Ty: b.Int}}}
	entry := core.Def{Name: "main", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: suspend(b, intLit(b, 1))}
	p := &core.Prog{Defs: []core.Def{factory, entry}}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(mp.Workers) != 1 || mp.Workers[0].Name != "main" || len(mp.Closures) != 0 {
		t.Fatalf("unrelated factory gained Machine definitions: %v, %v", workerNames(mp), mp.Closures)
	}
}

func TestLowerRootsMachineLambdaInsideDirectIteratorOwnerCall(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	iterator := &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName, Args: []types.Type{b.Int}}
	label := types.EffLabel{Unique: sup.NextUnique(), Name: types.GeneratorEffectName, Args: []types.Type{b.Int}, Suspension: true}
	ownerScope := sup.FreshScope()
	yieldOwner := core.EffectInstance{Unique: label.Unique, Name: label.Name, Args: label.Args, Captures: types.ScopeCapture(ownerScope), Control: types.Control{Transport: types.Machine}}
	yieldParam := yieldOwner
	yieldParam.Captures = types.VarCapture(sup.FreshCapture())
	yieldEffect := &types.EffectInfo{Unique: label.Unique, Name: label.Name, Params: []*types.TVar{sup.FreshRigid(types.General)}, Suspension: true}
	producerTy := &types.TFun{Arg: b.Unit, Eff: types.Row{Labels: []types.EffLabel{label}}, Ret: b.Unit}
	consumerTy := &types.TFun{Arg: iterator, Ret: b.Unit}
	ownerTy := &types.TFun{Arg: producerTy, Ret: &types.TFun{Arg: consumerTy, Ret: b.Unit}}
	owner := core.Def{Name: types.GeneratorWithIteratorName, Type: ownerTy,
		Params: []string{"producer", "consumer"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()},
		Body: &core.IteratorScope{Scope: ownerScope, Yield: yieldOwner,
			Producer: &core.VarRef{Name: "producer", Local: true, Ty: producerTy},
			Consumer: &core.VarRef{Name: "consumer", Local: true, Ty: consumerTy},
			CursorTy: iterator, Ty: b.Unit,
		}}
	captured := &core.VarRef{Name: "captured", Local: true, Ty: b.Int}
	producer := &core.Lambda{Param: "_", ParamCapture: sup.FreshCapture(), Ty: producerTy, EffectParams: []core.EffectInstance{yieldParam},
		Body: &core.Suspend{Owner: yieldParam, Request: captured, Ty: b.Unit}}
	consumer := &core.Lambda{Param: "cursor", ParamCapture: sup.FreshCapture(), Ty: consumerTy, Body: &core.UnitLit{Ty: b.Unit}}
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: owner.Name, Ty: ownerTy},
		Args: []core.Expr{producer, consumer}, Ty: b.Unit}
	main := core.Def{Name: "Main.main", Owner: "Main", Type: b.Unit,
		Body: &core.Let{Name: "captured", Rhs: &core.IntLit{Val: 7, Ty: b.Int}, Body: call, Ty: b.Unit}}
	p := &core.Prog{Intrinsics: map[string]bool{types.GeneratorWithIteratorName: true}, Defs: []core.Def{owner, main}}
	p.Effects = append(p.Effects, yieldEffect)
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatalf("capture inference: %v", errs)
	}
	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("machine lowering: %v", errs)
	}
	if len(mp.Closures) != 1 || len(mp.Workers) != 1 {
		t.Fatalf("machine roots = %d closures / %d workers, want 1 / 1", len(mp.Closures), len(mp.Workers))
	}
	closure := mp.Closures[0]
	if closure.Expr != producer || len(closure.Captures) != 1 || closure.Captures[0].Name != "captured" {
		t.Fatalf("rooted closure = %+v", closure)
	}
	if mp.Workers[0].Name != closure.Worker || len(mp.Workers[0].Params) != 2 {
		t.Fatalf("rooted worker = %+v", mp.Workers[0])
	}
	for _, damage := range []string{"missing", "identity", "capture", "type"} {
		t.Run(damage+" owner", func(t *testing.T) {
			broken := *mp
			broken.Workers = append([]Worker(nil), mp.Workers...)
			worker := &broken.Workers[0]
			worker.Blocks = append([]Block(nil), worker.Blocks...)
			term := *worker.Blocks[worker.Entry].Term.(*Suspend)
			switch damage {
			case "missing":
				term.Owner = core.EffectInstance{}
			case "identity":
				term.Owner.Unique++
			case "capture":
				term.Owner.Captures = types.ScopeCapture(sup.FreshScope())
			case "type":
				term.Owner.Args = []types.Type{b.String}
			}
			worker.Blocks[worker.Entry].Term = &term
			if got := errorsText(Lint(&broken)); !strings.Contains(got, "owner") {
				t.Fatalf("malformed Machine owner accepted: %s", got)
			}
			// Reconstruct the same obligation from semantic Core, without
			// trusting the graph already inferred before the mutation.
			original := producer.Body.(*core.Suspend).Owner
			producer.Body.(*core.Suspend).Owner = term.Owner
			if errs := core.LintMachineInput(p, b); len(errs) == 0 {
				t.Fatal("malformed Core owner accepted")
			}
			producer.Body.(*core.Suspend).Owner = original
		})
	}
}

func TestMachineLintRecomputesLivenessAndRejectsBadEdges(t *testing.T) {
	_, b := testBuiltins()
	x := &core.VarRef{Name: "x", Local: true, Ty: b.Int}
	body := &core.Let{Name: "x", Rhs: suspend(b, intLit(b, 1)), Ty: b.Int,
		Body: &core.Let{Name: "y", Rhs: suspend(b, x), Ty: b.Int, Body: x}}
	p := &core.Prog{Defs: []core.Def{{Name: "main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: body}}}
	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("Lower: %s", errorsText(errs))
	}

	brokenLive := *mp
	brokenLive.Workers = append([]Worker(nil), mp.Workers...)
	brokenLive.Workers[0].Blocks = append([]Block(nil), mp.Workers[0].Blocks...)
	brokenLive.Workers[0].Blocks[2].LiveOut = nil
	if got := errorsText(Lint(&brokenLive)); !strings.Contains(got, "LiveOut") {
		t.Fatalf("missing liveness error:\n%s", got)
	}

	brokenEdge := *mp
	brokenEdge.Workers = append([]Worker(nil), mp.Workers...)
	brokenEdge.Workers[0].Blocks = append([]Block(nil), mp.Workers[0].Blocks...)
	term := *brokenEdge.Workers[0].Blocks[3].Term.(*Suspend)
	term.Next = 99
	brokenEdge.Workers[0].Blocks[3].Term = &term
	if got := errorsText(Lint(&brokenEdge)); !strings.Contains(got, "invalid successor 99") {
		t.Fatalf("missing successor error:\n%s", got)
	}
}

func TestMachineLintChecksCleanupOwnership(t *testing.T) {
	_, b := testBuiltins()
	result := Local{Name: "result", Ty: b.Int}
	base := Worker{Name: "main", Result: b.Int, Entry: 0, Locals: []Local{result}, Blocks: []Block{
		{ID: 0, Term: &Return{Value: intLit(b, 1)}},
	}}
	underflow := Prog{Workers: []Worker{base}}
	underflow.Workers[0].Blocks[0].Term = &PopCleanup{Next: 1}
	underflow.Workers[0].Blocks = append(underflow.Workers[0].Blocks,
		Block{ID: 1, Term: &Return{Value: intLit(b, 1)}})
	analyze(&underflow.Workers[0])
	if got := errorsText(Lint(&underflow)); !strings.Contains(got, "cleanup stack underflow") {
		t.Fatalf("missing cleanup underflow error:\n%s", got)
	}

	pending := Prog{Workers: []Worker{base}}
	pending.Workers[0].Blocks[0].Term = &PushCleanup{
		Acquire: intLit(b, 1), Resource: result, Release: &core.UnitLit{Ty: b.Unit}, Next: 1,
	}
	pending.Workers[0].Blocks = append(pending.Workers[0].Blocks,
		Block{ID: 1, Term: &Return{Value: intLit(b, 1)}})
	analyze(&pending.Workers[0])
	if got := errorsText(Lint(&pending)); !strings.Contains(got, "returns with 1 pending cleanup") {
		t.Fatalf("missing pending cleanup error:\n%s", got)
	}
}

func TestOrdinaryCoreLintRejectsPrivateSuspension(t *testing.T) {
	_, b := testBuiltins()
	p := &core.Prog{Defs: []core.Def{{Name: "main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: suspend(b, intLit(b, 1))}}}
	if got := errorsText(core.Lint(p, b)); !strings.Contains(got, "compiler-only suspension reached ordinary Core") {
		t.Fatalf("ordinary lint did not reject suspension:\n%s", got)
	}
	if got := core.LintMachineInput(p, b); len(got) != 0 {
		t.Fatalf("machine-input lint rejected fixture:\n%s", errorsText(got))
	}
}

func testBuiltins() (*types.Supply, *types.Builtins) {
	sup := &types.Supply{}
	return sup, types.NewBuiltins(sup)
}

func intLit(b *types.Builtins, value int64) core.Expr {
	return &core.IntLit{Val: value, Ty: b.Int}
}

func suspend(b *types.Builtins, request core.Expr) core.Expr {
	return &core.Suspend{Request: request, Ty: b.Int}
}

func errorsText(errs []error) string {
	var out strings.Builder
	for _, err := range errs {
		out.WriteString(err.Error())
		out.WriteByte('\n')
	}
	return out.String()
}

func workerNames(p *Prog) []string {
	out := make([]string, len(p.Workers))
	for i := range p.Workers {
		out[i] = p.Workers[i].Name
	}
	return out
}
